package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFileAndOverrides(t *testing.T) {
	t.Setenv("MQTT_USERNAME", "environment-user")
	t.Setenv("MQTT_PASSWORD", "environment-password")
	path := filepath.Join(t.TempDir(), "cbus2mqtt.conf")
	contents := "# settings\n\nv = true\ncgate-events = host:20024\ncgate-commands=host:20023\nmqtt=tcp://broker:1883\nclient-id=test\nevents-topic=events\ncommands-topic=commands\nmqtt-username=file-user\nmqtt-password=secret=with#symbols\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]string{"-mqtt", "tcp://override:1883", "-v=false"}, path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := config{eventsAddr: "host:20024", commandsAddr: "host:20023", broker: "tcp://override:1883", clientID: "test", eventsTopic: "events", commandsTopic: "commands", mqttUsername: "file-user", mqttPassword: "secret=with#symbols"}
	if cfg != want {
		t.Fatal("config settings or command-line overrides did not match")
	}
}

func TestConfigMissingFile(t *testing.T) {
	t.Setenv("MQTT_USERNAME", "user")
	t.Setenv("MQTT_PASSWORD", "password")
	cfg, err := parseConfig(nil, filepath.Join(t.TempDir(), "missing.conf"), io.Discard)
	if err != nil || cfg.eventsAddr != "localhost:20025" || cfg.broker != "tcp://localhost:1883" || cfg.mqttUsername != "user" || cfg.mqttPassword != "password" {
		t.Fatal("missing file should preserve defaults and environment credentials", err)
	}
}

func TestConfigErrors(t *testing.T) {
	for _, contents := range []string{"mqtt broker", "unknown=secret", "v=secret"} {
		t.Run(contents, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cbus2mqtt.conf")
			if err := os.WriteFile(path, []byte("# comment\n"+contents), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := parseConfig(nil, path, io.Discard)
			if err == nil || !strings.Contains(err.Error(), path+":2:") || strings.Contains(strings.TrimPrefix(err.Error(), "config "+path), "secret") {
				t.Fatalf("expected line-numbered error without values, got %v", err)
			}
		})
	}
	if _, err := parseConfig(nil, t.TempDir(), io.Discard); err == nil {
		t.Fatal("expected error for unreadable config")
	}
}
