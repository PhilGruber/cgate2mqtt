package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// Only the command worker reads replies from the command socket.
type commandRequest struct {
	command string
	replies chan []string
}

// Used by the event worker only; cached names last until the bridge restarts.
type eventMapper struct {
	baseTopic string
	names     map[string]string
	lookup    func(string) (string, error)
}

func (m *eventMapper) message(line string) (string, string) {
	fields := strings.Fields(line)
	// Status-change events have the form: lighting on //PROJECT/net/app/group.
	// Preserve other event formats on the base topic.
	if len(fields) < 3 || fields[0] != "lighting" || !strings.HasPrefix(fields[2], "//") {
		return m.baseTopic, line
	}
	address := fields[2]
	parts := strings.Split(strings.TrimPrefix(address, "//"), "/")
	if len(parts) != 4 {
		return m.baseTopic, line
	}
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, "+#\x00") {
			return m.baseTopic, line
		}
	}
	name := m.names[address]
	if name == "" {
		// Keep the project qualifier so lookups cannot resolve in another project.
		resolved, err := m.lookup(address)
		if err == nil {
			name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(resolved)), " ", "-")
			if name == "" || strings.ContainsAny(name, "/+#\x00\r\n") {
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
	path := parts[0] + "/" + name
	if name == "" {
		path = strings.Join(parts, "/")
	}
	value := strings.Join(append([]string{fields[1]}, fields[3:]...), " ")
	payload, _ := json.Marshal(map[string]string{fields[0]: value})
	return strings.TrimRight(m.baseTopic, "/") + "/" + path, string(payload)
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
