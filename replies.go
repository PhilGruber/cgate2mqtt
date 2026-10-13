package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type commandResult struct {
	Command string   `json:"command,omitempty"`
	OK      bool     `json:"ok"`
	Code    int      `json:"code,omitempty"`
	Replies []string `json:"replies,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type mqttPublish func(topic, payload string, retained bool) error

func commandCompletion(command deviceCommand, resultTopic, eventsTopic, devicesPath string, publish mqttPublish) func([]string) error {
	return func(lines []string) error {
		code, replyErr := successfulReplies(lines)
		result := commandResult{Command: command.wire, Code: code, Replies: lines, OK: replyErr == nil}
		if replyErr == nil {
			messages, err := queryMessages(command, lines)
			store := &deviceStore{path: devicesPath}
			if err == nil && len(messages) > 0 {
				err = store.reload()
			}
			if err == nil {
				mapper := eventMapper{baseTopic: eventsTopic, names: store.names}
				for _, message := range messages {
					topic, payload, mapErr := mapper.message(message)
					if mapErr != nil {
						err = mapErr
						break
					}
					if publishErr := publish(topic, payload, true); publishErr != nil {
						return publishErr
					}
				}
			}
			replyErr = err
		}
		if replyErr != nil {
			result.OK, result.Error = false, replyErr.Error()
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return publish(resultTopic, string(payload), false)
	}
}

// Keep independently changing clock fields in separate retained topics.
func statePublication(topic, payload string) (string, bool) {
	var status struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(payload), &status) != nil {
		return topic, false
	}
	switch status.Type {
	case "security":
		return topic, status.Command != "status_request"
	case "lighting", "measurement", "temperature":
		return topic, true
	case "clock":
		return topic + "/" + status.Command, true
	}
	return topic, false
}

func replyCode(line string) (int, error) {
	if len(line) < 4 || (line[3] != ' ' && line[3] != '-') {
		return 0, fmt.Errorf("invalid C-Gate response")
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("invalid C-Gate response code")
	}
	return code, nil
}

func successfulReplies(lines []string) (int, error) {
	if len(lines) == 0 {
		return 0, fmt.Errorf("missing C-Gate response")
	}
	code := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "000 ") {
			return 0, fmt.Errorf("C-Gate command failed: %s", line[4:])
		}
		var err error
		code, err = replyCode(line)
		if err != nil {
			return 0, err
		}
		if code >= 400 {
			return code, fmt.Errorf("C-Gate rejected command: %s", line)
		}
	}
	return code, nil
}

// Translate successful read replies to the same application messages used by
// live events. No writes or optimistic state updates follow control commands.
func queryMessages(command deviceCommand, lines []string) ([]string, error) {
	if command.parameter == "tree" {
		var messages []string
		for _, line := range lines {
			fields := strings.Fields(strings.TrimLeft(line[4:], " "))
			if len(fields) < 2 {
				continue
			}
			parts, ok := deviceAddress(fields[0])
			if !ok || len(parts) != 4 || applicationType(parts[2]) != "lighting" {
				continue
			}
			for _, field := range fields[1:] {
				if strings.HasPrefix(field, "level=") {
					value := strings.TrimPrefix(field, "level=")
					level, err := strconv.Atoi(value)
					if err != nil || level < 0 || level > 255 {
						continue // Unknown or unsynchronised levels are not states.
					}
					messages = append(messages, "lighting level "+fields[0]+" "+value)
				}
			}
		}
		return messages, nil
	}
	if command.parameter == "" || command.address == "" {
		return nil, nil
	}
	value := ""
	for _, line := range lines {
		key := command.parameter + "="
		body := strings.ToLower(line[4:])
		if pos := strings.Index(body, key); pos >= 0 {
			// Parameter values may be comma-separated measurements.
			value = strings.TrimSpace(line[4+pos+len(key):])
			value, _, _ = strings.Cut(value, " ")
		}
	}
	if value == "" {
		return nil, fmt.Errorf("response did not contain %s", command.parameter)
	}
	address := command.address
	switch command.parameter {
	case "level":
		level, err := strconv.Atoi(value)
		if err != nil || level < 0 || level > 255 {
			return nil, fmt.Errorf("lighting brightness is unknown or invalid")
		}
		return []string{"lighting level " + address + " " + value}, nil
	case "temperature":
		return []string{"temperature broadcast " + address + " " + value}, nil
	case "armstate":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 127 {
			return nil, fmt.Errorf("security arm state is unknown or invalid")
		}
		if n == 0 {
			return []string{"security system_disarmed " + address}, nil
		}
		return []string{"security system_armed " + address + " " + value}, nil
	case "data":
		args := strings.Split(value, ",")
		if len(args) != 4 {
			return nil, fmt.Errorf("measurement data requires four fields")
		}
		age, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil || age < 0 {
			return nil, fmt.Errorf("no measurement has been received")
		}
		if _, err := measurementStatus(args[:3]); err != nil {
			return nil, err
		}
		return []string{"measurement data " + address + " " + strings.Join(args[:3], " ")}, nil
	}
	return nil, fmt.Errorf("unsupported query parameter")
}

func startupCommands(devices []device, configuredNetworks string) ([]deviceCommand, error) {
	networks := map[string]bool{}
	commands := map[string]deviceCommand{}
	add := func(command deviceCommand) { commands[command.wire] = command }
	for _, network := range strings.Split(configuredNetworks, ",") {
		network = strings.TrimSpace(network)
		if network == "" {
			continue
		}
		parts, ok := deviceAddress(network)
		if !ok || len(parts) != 2 {
			return nil, fmt.Errorf("startup-networks requires comma-separated //PROJECT/network addresses")
		}
		networks[network] = true
	}
	for _, d := range devices {
		p, ok := deviceAddress(d.Address)
		if !ok {
			continue
		}
		networks["//"+strings.Join(p[:2], "/")] = true
		if len(p) < 3 {
			continue
		}
		request := deviceRequest{}
		address := d.Address
		switch applicationType(p[2]) {
		case "lighting":
			request.Command = "get_brightness"
		case "temperature":
			request.Command = "get_temperature"
		case "measurement":
			request.Command = "get_data"
		case "security":
			address = "//" + strings.Join(p[:3], "/")
			request.Command = "get_arm_state"
			for _, report := range []int{1, 2} {
				command, _ := buildDeviceCommand(address, deviceRequest{Command: "status_request", Value: &report})
				add(command)
			}
		default:
			continue
		}
		command, err := buildDeviceCommand(address, request)
		if err == nil {
			add(command)
		}
	}
	for network := range networks {
		add(deviceCommand{wire: "getstate " + network, address: network})
		add(deviceCommand{wire: "tree " + network, address: network, parameter: "tree"})
	}
	keys := make([]string, 0, len(commands))
	for key := range commands {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]deviceCommand, 0, len(keys))
	for _, key := range keys {
		result = append(result, commands[key])
	}
	return result, nil
}
