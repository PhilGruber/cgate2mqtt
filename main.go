package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const operationTimeout = 10 * time.Second

type config struct {
	eventsAddr, commandsAddr, broker, clientID, eventsTopic, commandsTopic string
	verbose                                                                bool
}

func main() {
	log.SetOutput(os.Stdout)
	var cfg config
	flag.BoolVar(&cfg.verbose, "v", false, "log C-Gate events and commands and MQTT subscriptions, accepted commands, and event publishes to stdout")
	flag.StringVar(&cfg.eventsAddr, "cgate-events", "localhost:20024", "C-Gate event address (20024 for events, 20025 for status changes)")
	flag.StringVar(&cfg.commandsAddr, "cgate-commands", "localhost:20023", "C-Gate command address")
	flag.StringVar(&cfg.broker, "mqtt", "tcp://localhost:1883", "MQTT broker URL")
	flag.StringVar(&cfg.clientID, "client-id", "cbus2mqtt", "unique MQTT client ID")
	flag.StringVar(&cfg.eventsTopic, "events-topic", "cbus/events", "MQTT event topic")
	flag.StringVar(&cfg.commandsTopic, "commands-topic", "cbus/commands", "MQTT command topic (exact topic, no wildcards)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func waitToken(ctx context.Context, token mqtt.Token) error {
	timer := time.NewTimer(operationTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("MQTT operation timed out")
	case <-token.Done():
		return token.Error()
	}
}

func run(ctx context.Context, cfg config) error {
	if cfg.eventsTopic == "" || cfg.commandsTopic == "" || strings.ContainsAny(cfg.eventsTopic+cfg.commandsTopic, "+#\x00") || cfg.eventsTopic == cfg.commandsTopic || strings.HasPrefix(cfg.commandsTopic, strings.TrimRight(cfg.eventsTopic, "/")+"/") {
		return errors.New("event and command topics must be nonempty exact MQTT topics; command topic must be outside the event topic subtree")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	commands := make(chan commandRequest, 64)
	failures := make(chan error, 3)
	opts := mqtt.NewClientOptions().AddBroker(cfg.broker).SetClientID(cfg.clientID).
		SetAutoReconnect(false).SetConnectTimeout(operationTimeout).SetWriteTimeout(operationTimeout)
	opts.SetUsername(os.Getenv("MQTT_USERNAME")).SetPassword(os.Getenv("MQTT_PASSWORD"))
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		select {
		case failures <- fmt.Errorf("MQTT disconnected: %w", err):
		default:
		}
	})
	client := mqtt.NewClient(opts)
	if err := waitToken(ctx, client.Connect()); err != nil {
		client.Disconnect(0)
		return fmt.Errorf("connect MQTT: %w", err)
	}
	defer client.Disconnect(0)
	log.Print("connected to MQTT broker")
	dialer := net.Dialer{Timeout: operationTimeout}
	events, err := dialer.DialContext(ctx, "tcp", cfg.eventsAddr)
	if err != nil {
		return fmt.Errorf("connect C-Gate events: %w", err)
	}
	defer events.Close()
	log.Printf("connected to C-Gate events at %s", cfg.eventsAddr)
	commandConn, err := dialer.DialContext(ctx, "tcp", cfg.commandsAddr)
	if err != nil {
		return fmt.Errorf("connect C-Gate commands: %w", err)
	}
	defer commandConn.Close()
	log.Printf("connected to C-Gate commands at %s", cfg.commandsAddr)
	// Closing both sockets interrupts blocked reads and writes during shutdown.
	closeOnCancel := context.AfterFunc(ctx, func() { events.Close(); commandConn.Close() })
	defer closeOnCancel()
	mapper := eventMapper{baseTopic: cfg.eventsTopic, names: make(map[string]string), lookup: func(address string) (string, error) {
		replies := make(chan []string, 1)
		select {
		case commands <- commandRequest{command: "get " + address + " name", replies: replies}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		select {
		case lines := <-replies:
			return parseNameReply(lines)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		workerErr := forwardEvents(ctx, events, func(line string) error {
			if cfg.verbose {
				log.Printf("C-Gate event received: %s", line)
			}
			topic, payload := mapper.message(line)
			if cfg.verbose {
				log.Printf("MQTT publish: topic=%q payload=%q", topic, payload)
			}
			return waitToken(ctx, client.Publish(topic, 1, false, payload))
		})
		select {
		case failures <- workerErr:
		default:
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		workerErr := forwardCommands(ctx, commandConn, commands, cfg.verbose, func() error {
			if cfg.verbose {
				log.Printf("MQTT subscribe: topic=%q", cfg.commandsTopic)
			}
			return waitToken(ctx, client.Subscribe(cfg.commandsTopic, 1, func(_ mqtt.Client, msg mqtt.Message) {
				// Retained commands must never actuate devices on startup.
				if msg.Retained() {
					log.Print("ignoring retained command")
					return
				}
				command, err := validateCommand(string(msg.Payload()))
				if err != nil {
					log.Printf("ignoring command: %v", err)
					return
				}
				if cfg.verbose {
					log.Printf("MQTT command received: topic=%q payload=%q", msg.Topic(), string(msg.Payload()))
				}
				select {
				case commands <- commandRequest{command: command}:
				case <-ctx.Done():
				default:
					select {
					case failures <- errors.New("command queue full"):
					default:
					}
				}
			}))
		})
		select {
		case failures <- workerErr:
		default:
		}
	}()
	select {
	case <-ctx.Done():
		err = nil
	case err = <-failures:
	}
	cancel()
	<-done
	<-done
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func forwardEvents(ctx context.Context, reader io.Reader, publish func(string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := publish(scanner.Text()); err != nil {
			return fmt.Errorf("publish C-Gate event: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read C-Gate events: %w", err)
	}
	return errors.New("C-Gate event connection closed")
}

func validateCommand(payload string) (string, error) {
	command := strings.TrimSpace(payload)
	if command == "" || len(command) > 4096 || strings.ContainsAny(command, "\r\n\x00") {
		return "", errors.New("expected one nonempty C-Gate command of at most 4096 bytes")
	}
	return command, nil
}

func forwardCommands(ctx context.Context, conn net.Conn, commands <-chan commandRequest, verbose bool, subscribe func() error) error {
	reader := bufio.NewReaderSize(conn, 8192)
	readReply := func() (string, error) {
		if err := conn.SetReadDeadline(time.Now().Add(operationTimeout)); err != nil {
			return "", err
		}
		line, err := reader.ReadSlice('\n')
		return strings.TrimSpace(string(line)), err
	}
	greeting, err := readReply()
	if err != nil {
		return fmt.Errorf("read C-Gate greeting: %w", err)
	}
	if !strings.HasPrefix(greeting, "201 ") {
		return fmt.Errorf("unexpected C-Gate greeting: %s", greeting)
	}
	if err := subscribe(); err != nil {
		return fmt.Errorf("subscribe MQTT: %w", err)
	}
	log.Print("C-Gate ready and MQTT command subscription successful; ready to forward commands")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case request := <-commands:
			command := request.command
			if err := conn.SetWriteDeadline(time.Now().Add(operationTimeout)); err != nil {
				return err
			}
			if verbose {
				log.Printf("C-Gate command: %s", command)
			}
			if _, err := io.WriteString(conn, command+"\r\n"); err != nil {
				return fmt.Errorf("write C-Gate command: %w", err)
			}
			// Consume all continuation lines before accepting the next command.
			var replies []string
			for {
				reply, err := readReply()
				if err != nil {
					return fmt.Errorf("read C-Gate reply: %w", err)
				}
				log.Printf("C-Gate reply: %s", reply)
				replies = append(replies, reply)
				if len(reply) < 4 || reply[3] != '-' {
					break
				}
			}
			if request.replies != nil {
				select {
				case request.replies <- replies:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}
