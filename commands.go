package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

type deviceCommand struct {
	wire, address, parameter string
}

type deviceRequest struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Value   *int     `json:"value"`
	Time    *float64 `json:"time"`
	Mode    string   `json:"mode"`
}

// Read a separate snapshot: the event worker owns the live device store.
func mqttCommand(baseTopic, topic string, payload []byte, devicesPath string) (string, error) {
	request, err := planCommand(baseTopic, topic, payload, devicesPath)
	return request.wire, err
}

func planCommand(baseTopic, topic string, payload []byte, devicesPath string) (deviceCommand, error) {
	baseTopic = strings.TrimRight(baseTopic, "/")
	if topic == baseTopic+"/set" {
		return rawDeviceCommand(string(payload))
	}
	prefix := baseTopic + "/"
	if !strings.HasPrefix(topic, prefix) || !strings.HasSuffix(topic, "/set") {
		return deviceCommand{}, fmt.Errorf("unexpected command topic %q", topic)
	}
	target := strings.TrimSuffix(strings.TrimPrefix(topic, prefix), "/set")
	var request deviceRequest
	if len(payload) > 4096 {
		return deviceCommand{}, fmt.Errorf("command payload exceeds 4096 bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return deviceCommand{}, fmt.Errorf("invalid command JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return deviceCommand{}, fmt.Errorf("expected one JSON command")
	}
	store := &deviceStore{path: devicesPath}
	if err := store.reload(); err != nil {
		return deviceCommand{}, fmt.Errorf("load command devices: %w", err)
	}
	address := ""
	for _, d := range store.devices {
		if _, ok := deviceAddress(d.Address); !ok {
			continue
		}
		name := normalizeName(d.FriendlyName)
		if (name == "" || target != name) && target != strings.TrimPrefix(d.Address, "//") {
			continue
		}
		if address != "" && address != d.Address {
			return deviceCommand{}, fmt.Errorf("ambiguous device topic %q", topic)
		}
		address = d.Address
	}
	// Fully qualified numeric addresses also work before a device emits an event.
	if address == "" {
		if _, ok := deviceAddress("//" + target); ok {
			address = "//" + target
		}
	}
	if address == "" {
		return deviceCommand{}, fmt.Errorf("unknown device topic %q", topic)
	}
	return buildDeviceCommand(address, request)
}

func buildDeviceCommand(address string, r deviceRequest) (deviceCommand, error) {
	p, ok := deviceAddress(address)
	if !ok || len(p) < 3 {
		return deviceCommand{}, fmt.Errorf("invalid device address")
	}
	kind := applicationType(p[2])
	if kind == "" || (r.Type != "" && r.Type != kind) {
		return deviceCommand{}, fmt.Errorf("unsupported or mismatched device type")
	}
	result := deviceCommand{address: address}
	noValues := func() bool { return r.Value == nil && r.Time == nil && r.Mode == "" }
	switch kind {
	case "lighting":
		if len(p) != 4 {
			return result, fmt.Errorf("lighting requires a group address")
		}
		switch r.Command {
		case "on", "off", "terminate_ramp", "get_brightness", "get_level":
			if !noValues() {
				return result, fmt.Errorf("%s does not accept value, time or mode", r.Command)
			}
			if r.Command == "get_brightness" || r.Command == "get_level" {
				result.wire, result.parameter = "get "+address+" level", "level"
			} else {
				result.wire = r.Command + " " + address
			}
		case "ramp":
			if r.Mode != "" || r.Value == nil || *r.Value < 0 || *r.Value > 255 {
				return result, fmt.Errorf("ramp requires an integer value from 0 to 255")
			}
			if r.Time != nil && (math.IsNaN(*r.Time) || math.IsInf(*r.Time, 0) || *r.Time < 0 || *r.Time > 1020) {
				return result, fmt.Errorf("ramp time must be between 0 and 1020 seconds")
			}
			result.wire = "ramp " + address + " " + strconv.Itoa(*r.Value)
			if r.Time != nil {
				result.wire += " " + strconv.FormatFloat(*r.Time, 'f', -1, 64)
			}
		default:
			return result, fmt.Errorf("unsupported lighting command %q", r.Command)
		}
	case "measurement", "temperature":
		size, verb, parameter := 5, "get_data", "data"
		if kind == "temperature" {
			size, verb, parameter = 4, "get_temperature", "temperature"
		}
		if len(p) != size || r.Command != verb || !noValues() {
			return result, fmt.Errorf("%s supports only %s on a valid sensor address", kind, verb)
		}
		result.wire, result.parameter = "get "+address+" "+parameter, parameter
	case "security":
		switch r.Command {
		case "arm":
			if len(p) != 3 || r.Value != nil || r.Time != nil {
				return result, fmt.Errorf("arm requires an application address and mode")
			}
			switch r.Mode {
			case "away", "night", "day", "vacation", "highest":
			default:
				return result, fmt.Errorf("unsupported security arm mode")
			}
			result.wire = "security arm " + address + " " + r.Mode
		case "status_request":
			if len(p) != 3 || r.Value == nil || (*r.Value != 1 && *r.Value != 2) || r.Time != nil || r.Mode != "" {
				return result, fmt.Errorf("status_request requires value 1 or 2 and an application address")
			}
			result.wire = "security status_request " + address + " " + strconv.Itoa(*r.Value)
		case "get_arm_state":
			if len(p) != 3 || !noValues() {
				return result, fmt.Errorf("get_arm_state requires an application address")
			}
			result.wire, result.parameter = "get "+address+" armstate", "armstate"
		case "request_zone_name":
			if len(p) != 4 || !noValues() {
				return result, fmt.Errorf("request_zone_name requires a zone address")
			}
			result.wire = "security request_zone_name " + address
		default:
			return result, fmt.Errorf("unsupported security command %q", r.Command)
		}
	}
	_, err := validateCommand(result.wire)
	return result, err
}

// Raw MQTT commands are limited to end-user operations, never commissioning,
// project management, arbitrary SET/DO calls, or scene/trigger operations.
func rawDeviceCommand(payload string) (deviceCommand, error) {
	line, err := validateCommand(payload)
	if err != nil {
		return deviceCommand{}, err
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return deviceCommand{}, fmt.Errorf("empty command")
	}
	verb := strings.ToLower(f[0])
	if (verb == "getstate" || verb == "tree") && len(f) == 2 {
		p, ok := deviceAddress(f[1])
		if !ok || len(p) != 2 {
			return deviceCommand{}, fmt.Errorf("getstate requires //PROJECT/network")
		}
		parameter := ""
		if verb == "tree" {
			parameter = "tree"
		}
		return deviceCommand{wire: verb + " " + f[1], address: f[1], parameter: parameter}, nil
	}
	if verb == "lighting" {
		f = f[1:]
		if len(f) == 0 {
			return deviceCommand{}, fmt.Errorf("missing lighting command")
		}
		verb = strings.ToLower(f[0])
	}
	if len(f) < 2 {
		return deviceCommand{}, fmt.Errorf("missing command address")
	}
	if verb == "security" {
		if len(f) < 3 {
			return deviceCommand{}, fmt.Errorf("missing security operation")
		}
		verb = strings.ToLower(f[1])
		f = append([]string{verb}, f[2:]...)
	}
	address := f[1]
	relative := !strings.HasPrefix(address, "//")
	if relative {
		address = "//DEFAULT/" + address
	}
	r := deviceRequest{Command: verb}
	if verb == "get" {
		if len(f) != 3 {
			return deviceCommand{}, fmt.Errorf("get requires one supported status parameter")
		}
		r.Command = map[string]string{"brightness": "get_brightness", "level": "get_brightness", "data": "get_data", "temperature": "get_temperature", "armstate": "get_arm_state"}[strings.ToLower(f[2])]
		if r.Command == "" {
			return deviceCommand{}, fmt.Errorf("unsupported status parameter")
		}
	} else if verb == "arm" {
		if len(f) != 3 {
			return deviceCommand{}, fmt.Errorf("arm requires mode")
		}
		r.Mode = strings.ToLower(f[2])
	} else if verb == "ramp" || verb == "status_request" {
		if len(f) < 3 || len(f) > 4 || (verb == "status_request" && len(f) != 3) {
			return deviceCommand{}, fmt.Errorf("invalid command arguments")
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return deviceCommand{}, err
		}
		r.Value = &n
		if len(f) == 4 {
			n, err := strconv.ParseFloat(f[3], 64)
			if err != nil {
				return deviceCommand{}, err
			}
			r.Time = &n
		}
	} else if len(f) != 2 {
		return deviceCommand{}, fmt.Errorf("unexpected command arguments")
	}
	if verb == "terminateramp" {
		r.Command = "terminate_ramp"
	}
	result, err := buildDeviceCommand(address, r)
	if err != nil {
		return result, err
	}
	if relative {
		result.wire = strings.Replace(result.wire, address, strings.TrimPrefix(address, "//DEFAULT/"), 1)
		result.address = ""
	}
	return result, nil
}
