package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// Only the command worker reads replies from the command socket.
type commandRequest struct {
	command string
	replies chan []string
}

// Used by the event worker only.
type eventMapper struct {
	baseTopic string
	names     map[string]string
	lookup    func(string) (string, error)
	record    func(string, string) error
	refresh   func() error
}

func (m *eventMapper) message(line string) (string, string, error) {
	line, _, _ = strings.Cut(line, "#")
	line = strings.TrimSpace(line)
	fields := strings.Fields(line)
	// Status-change events have the form: lighting on //PROJECT/net/app/group.
	// Preserve other event formats on the base topic.
	if len(fields) < 3 || fields[0] != "lighting" || !strings.HasPrefix(fields[2], "//") {
		return m.baseTopic, line, nil
	}
	address := fields[2]
	parts := strings.Split(strings.TrimPrefix(address, "//"), "/")
	if len(parts) != 4 {
		return m.baseTopic, line, nil
	}
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, "+#\x00") {
			return m.baseTopic, line, nil
		}
	}
	status := struct {
		Command string   `json:"command"`
		Value   *float64 `json:"value,omitempty"`
		Time    *float64 `json:"time,omitempty"` // Seconds.
	}{Command: fields[1]}
	if len(fields) > 5 {
		return "", "", fmt.Errorf("too many lighting event arguments: %s", line)
	}
	for i, argument := range fields[3:] {
		value, err := strconv.ParseFloat(argument, 64)
		if err != nil {
			return "", "", fmt.Errorf("invalid lighting event argument %q: %w", argument, err)
		}
		if i == 0 {
			status.Value = &value
		} else {
			status.Time = &value
		}
	}
	payload, err := json.Marshal(status)
	if err != nil {
		return "", "", fmt.Errorf("encode lighting event: %w", err)
	}
	if m.refresh != nil {
		if err := m.refresh(); err != nil {
			return "", "", fmt.Errorf("load devices: %w", err)
		}
	}
	name := normalizeName(m.names[address])
	if name == "" {
		// Keep the project qualifier so lookups cannot resolve in another project.
		resolved, err := m.lookup(address)
		if err == nil {
			name = normalizeName(resolved)
			if name == "" {
				err = fmt.Errorf("name is empty or unsuitable for an MQTT topic")
				name = ""
			} else {
				m.names[address] = name
			}
		}
		if err != nil {
			log.Printf("C-Gate name lookup for %s: %v; using address topic", address, err)
		}
	}
	if m.record != nil {
		if err := m.record(address, name); err != nil {
			return "", "", fmt.Errorf("save devices: %w", err)
		}
	}
	name = normalizeName(m.names[address])
	path := parts[0] + "/" + name
	if name == "" {
		path = strings.Join(parts, "/")
	}
	return strings.TrimRight(m.baseTopic, "/") + "/" + path, string(payload), nil
}

func parseNameReply(lines []string) (string, error) {
	var name string
	for _, line := range lines {
		if len(line) < 4 || (line[:3] != "300" && line[:3] != "200") {
			return "", fmt.Errorf("unexpected name reply: %s", line)
		}
		if _, value, ok := strings.Cut(line[4:], " name="); ok {
			name = strings.TrimSpace(value)
			if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
				name = name[1 : len(name)-1]
			}
		}
	}
	if name == "" {
		return "", fmt.Errorf("C-Gate returned no name")
	}
	return name, nil
}

func normalizeName(name string) string {
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "-")
	if strings.ContainsAny(name, "+#\x00\r\n") {
		return ""
	}
	return name
}
