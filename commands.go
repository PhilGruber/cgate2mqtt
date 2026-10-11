package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Read a separate snapshot: the event worker owns the live device store.
func mqttCommand(baseTopic, topic string, payload []byte, devicesPath string) (string, error) {
	if topic == baseTopic {
		return validateCommand(string(payload))
	}
	prefix := strings.TrimRight(baseTopic, "/") + "/"
	if !strings.HasPrefix(topic, prefix) {
		return "", fmt.Errorf("unexpected command topic %q", topic)
	}
	target := strings.TrimPrefix(topic, prefix)
	var request struct {
		Command string   `json:"command"`
		Value   *int     `json:"value"`
		Time    *float64 `json:"time"`
	}
	if len(payload) > 4096 {
		return "", fmt.Errorf("command payload exceeds 4096 bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", fmt.Errorf("invalid command JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("expected one JSON command")
	}
	switch request.Command {
	case "on", "off":
		if request.Value != nil || request.Time != nil {
			return "", fmt.Errorf("%s does not accept value or time", request.Command)
		}
	case "ramp":
		if request.Value == nil || *request.Value < 0 || *request.Value > 255 {
			return "", fmt.Errorf("ramp requires an integer value from 0 to 255")
		}
		if request.Time != nil && (*request.Time < 0 || *request.Time > 1020) {
			return "", fmt.Errorf("ramp time must be between 0 and 1020 seconds")
		}
	default:
		return "", fmt.Errorf("unsupported command %q", request.Command)
	}
	store := &deviceStore{path: devicesPath}
	if err := store.reload(); err != nil {
		return "", fmt.Errorf("load command devices: %w", err)
	}
	var address string
	for _, d := range store.devices {
		parts := strings.Split(strings.TrimPrefix(d.Address, "//"), "/")
		if !strings.HasPrefix(d.Address, "//") || len(parts) != 4 {
			continue
		}
		name := normalizeName(d.FriendlyName)
		if (name == "" || target != parts[0]+"/"+name) && target != strings.Join(parts, "/") {
			continue
		}
		if address != "" && address != d.Address {
			return "", fmt.Errorf("ambiguous device topic %q", topic)
		}
		address = d.Address
	}
	if address == "" {
		return "", fmt.Errorf("unknown device topic %q", topic)
	}
	command := request.Command + " " + address
	if request.Value != nil {
		command += " " + strconv.Itoa(*request.Value)
	}
	if request.Time != nil {
		command += " " + strconv.FormatFloat(*request.Time, 'f', -1, 64)
	}
	return validateCommand(command)
}
