package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// File settings replace defaults; explicit command-line flags replace file settings.
func parseConfig(args []string, path string, output io.Writer) (config, error) {
	var cfg config
	flags := flag.NewFlagSet("cbus2mqtt", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&cfg.verbose, "v", false, "also log outgoing C-Gate commands and MQTT subscriptions to stdout")
	flags.StringVar(&cfg.eventsAddr, "cgate-events", "localhost:20025", "C-Gate event address")
	flags.StringVar(&cfg.commandsAddr, "cgate-commands", "localhost:20023", "C-Gate command address")
	flags.StringVar(&cfg.broker, "mqtt", "tcp://localhost:1883", "MQTT broker URL")
	flags.StringVar(&cfg.clientID, "client-id", "cbus2mqtt", "unique MQTT client ID")
	flags.StringVar(&cfg.eventsTopic, "events-topic", "cbus", "MQTT event base topic")
	flags.StringVar(&cfg.commandsTopic, "commands-topic", "cbus", "MQTT command base topic (commands end in /set; no wildcards)")
	flags.StringVar(&cfg.mqttUsername, "mqtt-username", os.Getenv("MQTT_USERNAME"), "MQTT username (defaults to MQTT_USERNAME)")
	flags.StringVar(&cfg.mqttPassword, "mqtt-password", os.Getenv("MQTT_PASSWORD"), "MQTT password (defaults to MQTT_PASSWORD)")
	flags.BoolVar(&cfg.retainState, "retain-state", true, "retain device state on MQTT")
	flags.BoolVar(&cfg.startupState, "startup-state", true, "query known devices and networks on startup")
	flags.StringVar(&cfg.startupNetworks, "startup-networks", "", "additional comma-separated //PROJECT/network addresses to query on startup")
	file, err := os.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for line := 1; scanner.Scan(); line++ {
			entry := strings.TrimSpace(scanner.Text())
			if entry == "" || strings.HasPrefix(entry, "#") {
				continue
			}
			key, value, ok := strings.Cut(entry, "=")
			key = strings.TrimSpace(key)
			if !ok || flags.Lookup(key) == nil {
				return cfg, fmt.Errorf("config %s:%d: expected a known key=value setting", path, line)
			}
			if err := flags.Set(key, strings.TrimSpace(value)); err != nil {
				// Do not include values: they may contain credentials.
				return cfg, fmt.Errorf("config %s:%d: invalid value for %s", path, line, key)
			}
		}
		if err := scanner.Err(); err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
	}
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	return cfg, nil
}
