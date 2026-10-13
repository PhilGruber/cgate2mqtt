package main

import (
	"bufio"
	"context"
	"encoding/json"
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
const defaultDevicesPath = "/etc/cbus2mqtt/devices.yaml"

type config struct {
	eventsAddr, commandsAddr, broker, clientID, eventsTopic, commandsTopic string
	verbose                                                                bool
	mqttUsername, mqttPassword                                             string
	startupNetworks                                                        string
	startupState, retainState                                              bool
}

func main() {
	log.SetOutput(os.Stdout)
	cfg, err := parseConfig(os.Args[1:], "/etc/cbus2mqtt/cbus2mqtt.conf", os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Print(err)
		os.Exit(1)
	}
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
	return runWithDevices(ctx, cfg, defaultDevicesPath)
}

func runWithDevices(ctx context.Context, cfg config, devicesPath string) error {
	cfg.eventsTopic = strings.TrimRight(cfg.eventsTopic, "/")
	cfg.commandsTopic = strings.TrimRight(cfg.commandsTopic, "/")
	if cfg.eventsTopic == "" || cfg.commandsTopic == "" || strings.ContainsAny(cfg.eventsTopic+cfg.commandsTopic, "+#\x00") {
		return errors.New("event and command topic prefixes must be nonempty MQTT topics without wildcards")
	}
	devices, err := loadDevices(devicesPath)
	if err != nil {
		return fmt.Errorf("load devices: %w", err)
	}
	initial, err := startupCommands(devices.devices, cfg.startupNetworks)
	if err != nil {
		return err
	}
	names := devices.names
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	commands := make(chan commandRequest, 64)
	failures := make(chan error, 3)
	opts := mqtt.NewClientOptions().AddBroker(cfg.broker).SetClientID(cfg.clientID).
		SetAutoReconnect(false).SetConnectTimeout(operationTimeout).SetWriteTimeout(operationTimeout)
	opts.SetUsername(cfg.mqttUsername).SetPassword(cfg.mqttPassword)
	if cfg.clientID == "" || strings.ContainsAny(cfg.clientID, "+#\x00") {
		return errors.New("client-id must be nonempty and contain no MQTT wildcards")
	}
	availabilityTopic := cfg.eventsTopic + "/bridge/" + cfg.clientID + "/availability"
	opts.SetWill(availabilityTopic, "offline", 1, true)
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
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), operationTimeout)
		defer stop()
		if client.IsConnected() {
			if err := waitToken(shutdown, client.Publish(availabilityTopic, 1, true, "offline")); err != nil {
				log.Printf("publish offline availability: %v", err)
			}
		}
		client.Disconnect(250)
	}()
	publish := func(topic, payload string, retained bool) error {
		log.Printf("MQTT publish: topic=%q payload=%q", topic, payload)
		return waitToken(ctx, client.Publish(topic, 1, retained, payload))
	}
	statePublish := func(topic, payload string, retained bool) error {
		return publish(topic, payload, retained && cfg.retainState)
	}
	if err := publish(availabilityTopic, "offline", true); err != nil {
		return err
	}
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
	mapper := eventMapper{baseTopic: cfg.eventsTopic, names: names, record: devices.record, refresh: devices.reload, lookup: func(address string) (string, error) {
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
			log.Printf("C-Gate event received: %s", line)
			topic, payload, err := mapper.message(line)
			if err != nil {
				return err
			}
			if payload == "" {
				return nil
			}
			topic, retained := statePublication(topic, payload)
			return statePublish(topic, payload, retained)
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
				log.Printf("MQTT subscribe: topic=%q", cfg.commandsTopic+"/#")
			}
			err := waitToken(ctx, client.Subscribe(cfg.commandsTopic+"/#", 1, func(_ mqtt.Client, msg mqtt.Message) {
				// MQTT filters cannot match a suffix after a variable-depth device path.
				if !strings.HasSuffix(msg.Topic(), "/set") {
					return
				}
				log.Printf("MQTT command received: topic=%q payload=%q", msg.Topic(), string(msg.Payload()))
				// Retained commands must never actuate devices on startup.
				if msg.Retained() {
					log.Print("ignoring retained command")
					return
				}
				command, err := planCommand(cfg.commandsTopic, msg.Topic(), msg.Payload(), devicesPath)
				resultTopic := strings.TrimSuffix(msg.Topic(), "/set") + "/result"
				request := commandRequest{command: command.wire, complete: commandCompletion(command, resultTopic, cfg.eventsTopic, devicesPath, statePublish)}
				if err != nil {
					log.Printf("ignoring command: %v", err)
					payload, _ := json.Marshal(commandResult{OK: false, Error: err.Error()})
					request = commandRequest{complete: func([]string) error { return publish(resultTopic, string(payload), false) }}
				}
				select {
				case commands <- request:
				case <-ctx.Done():
				default:
					select {
					case failures <- errors.New("command queue full"):
					default:
					}
				}
			}))
			if err != nil {
				return err
			}
			// Enqueue snapshots without blocking the socket worker on a full queue.
			go func() {
				if cfg.startupState {
					for _, command := range initial {
						request := commandRequest{command: command.wire, complete: commandCompletion(command, cfg.eventsTopic+"/startup/result", cfg.eventsTopic, devicesPath, statePublish)}
						select {
						case commands <- request:
						case <-ctx.Done():
							return
						}
					}
				}
				select {
				case commands <- commandRequest{complete: func([]string) error { return publish(availabilityTopic, "online", true) }}:
				case <-ctx.Done():
				}
			}()
			return nil
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
		line, err := reader.ReadSlice('\n')
		if len(line) > 0 {
			log.Printf("C-Gate reply: %s", strings.TrimRight(string(line), "\r\n"))
		}
		return strings.TrimSpace(string(line)), err
	}
	if err := conn.SetReadDeadline(time.Now().Add(operationTimeout)); err != nil {
		return err
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
	execute := func(command string) ([]string, error) {
		if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
			return nil, err
		}
		if verbose {
			log.Printf("C-Gate command: %s", command)
		}
		if _, err := io.WriteString(conn, command+"\r\n"); err != nil {
			return nil, fmt.Errorf("write C-Gate command: %w", err)
		}
		var replies []string
		for {
			reply, err := readReply()
			if err != nil {
				return replies, fmt.Errorf("read C-Gate reply: %w", err)
			}
			replies = append(replies, reply)
			if len(replies) > 4096 {
				return replies, errors.New("C-Gate reply exceeds 4096 lines")
			}
			if _, err := replyCode(reply); err != nil {
				return replies, err
			}
			if reply[3] != '-' {
				return replies, nil
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case request, ok := <-commands:
			if !ok {
				return nil
			}
			if request.command == "" && request.complete != nil {
				if err := request.complete(nil); err != nil {
					return err
				}
				continue
			}
			replies, err := execute(request.command)
			// Some builds spell this TERMINATERAMP. Retry only a syntax rejection.
			if err == nil && strings.HasPrefix(request.command, "terminate_ramp ") && len(replies) == 1 && strings.HasPrefix(replies[0], "400 ") {
				replies, err = execute(strings.Replace(request.command, "terminate_ramp", "terminateramp", 1))
			}
			if request.complete != nil {
				reported := replies
				if err != nil {
					reported = append(append([]string(nil), replies...), "000 "+err.Error())
				}
				if completeErr := request.complete(reported); completeErr != nil && err == nil {
					return completeErr
				}
			}
			if err != nil {
				return err
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
