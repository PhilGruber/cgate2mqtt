package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMQTTJSONCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.yaml")
	data := "- address: //JERV/254/56/35\n  friendly_name: bedroom/tv\n- address: //OTHER/254/56/35\n  friendly_name: bedroom/tv\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ topic, payload, want string }{
		{"cbus/JERV/bedroom/tv/set", `{"command":"ramp","value":255,"time":4}`, "ramp //JERV/254/56/35 255 4"},
		{"cbus/JERV/bedroom/tv/set", `{"command":"on"}`, "on //JERV/254/56/35"},
		{"cbus/OTHER/bedroom/tv/set", `{"command":"off"}`, "off //OTHER/254/56/35"},
		{"cbus/JERV/254/56/35/set", `{"command":"ramp","value":0}`, "ramp //JERV/254/56/35 0"},
		{"cbus/set", "on 254/56/35", "on 254/56/35"},
	} {
		got, err := mqttCommand("cbus", tc.topic, []byte(tc.payload), path)
		if err != nil || got != tc.want {
			t.Fatalf("%s %s: got %q, %v; want %q", tc.topic, tc.payload, got, err, tc.want)
		}
	}
	for _, payload := range []string{
		`not json`, `null`, `{}`, `{"command":"ramp"}`, `{"command":"ramp","value":256}`, `{"command":"ramp","value":-1}`, `{"command":"ramp","value":1.5}`, `{"command":"ramp","value":1,"time":-1}`, `{"command":"ramp","value":1,"time":1021}`, `{"command":"on","time":4}`, `{"command":"off","value":0}`, `{"command":"noop"}`, `{"command":"on","extra":1}`, `{"command":"on"} {}`, `{"command":"on\noff"}`,
	} {
		if got, err := mqttCommand("cbus", "cbus/JERV/bedroom/tv/set", []byte(payload), path); err == nil {
			t.Errorf("accepted %s as %q", payload, got)
		}
	}
	for _, topic := range []string{"cbus/JERV/missing/set", "other/JERV/bedroom/tv/set", "cbus/JERV/bedroom/tv", "cbus", "cbus/JERV/bedroom/tv/set/extra"} {
		if _, err := mqttCommand("cbus", topic, []byte(`{"command":"on"}`), path); err == nil {
			t.Errorf("accepted %q", topic)
		}
	}
	data += "- address: //JERV/254/56/36\n  friendly_name: bedroom/tv\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := mqttCommand("cbus", "cbus/JERV/bedroom/tv/set", []byte(`{"command":"on"}`), path); err == nil {
		t.Fatal("accepted ambiguous name")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(data, "bedroom/tv", "new/name")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := mqttCommand("cbus", "cbus/JERV/bedroom/tv/set", []byte(`{"command":"on"}`), path); err == nil {
		t.Fatal("used stale names")
	}
}

func TestForwardCommandsVerboseLogging(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		var output bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&output)
		bridge, server := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		commands := make(chan commandRequest, 1)
		commands <- commandRequest{command: "ramp //JERV/254/56/35 255 4"}
		done := make(chan error, 1)
		go func() { done <- forwardCommands(ctx, bridge, commands, verbose, func() error { return nil }) }()
		server.SetDeadline(time.Now().Add(2 * time.Second))
		_, writeErr := io.WriteString(server, "201 Service ready\r\n")
		got, readErr := bufio.NewReader(server).ReadString('\n')
		_, replyErr := io.WriteString(server, "200 OK\r\n")
		cancel()
		bridge.Close()
		server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("command worker did not stop")
		}
		log.SetOutput(previous)
		if writeErr != nil || readErr != nil || replyErr != nil || got != "ramp //JERV/254/56/35 255 4\r\n" {
			t.Fatalf("command %q: %v %v %v", got, writeErr, readErr, replyErr)
		}
		if strings.Contains(output.String(), "C-Gate command: ramp //JERV/254/56/35 255 4") != verbose {
			t.Fatalf("verbose=%v logs=%q", verbose, output.String())
		}
	}
}
