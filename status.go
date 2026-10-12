package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Convert both event-port and status-change-port messages to application syntax.
// Security status on the SCP is commented out because it is not replayable.
func normalizeEvent(line string) (string, map[string]any) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#security ") || strings.HasPrefix(line, "# security ") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	}
	metadata := map[string]any{}
	body, comment, _ := strings.Cut(line, "#")
	f := strings.Fields(body)
	for _, field := range append(strings.Fields(comment), f...) {
		key, value, ok := strings.Cut(field, "=")
		if ok && strings.EqualFold(key, "sourceunit") {
			if unit, err := strconv.Atoi(value); err == nil && unit >= 0 && unit <= 255 {
				metadata["source_unit"] = unit
			}
		}
	}
	if len(f) < 4 || !strings.HasPrefix(f[2], "//") || !validEventTimestamp(f[0]) {
		return line, metadata
	}
	address := f[2]
	var canonical string
	if f[1] == "702" && len(f) >= 5 && strings.HasPrefix(f[3], "[") && strings.HasSuffix(f[3], "]") {
		application := strings.Trim(f[3], "[]")
		if application != "lighting" && application != "clock" && application != "measurement" && application != "temperature" && application != "security" {
			return line, metadata
		}
		args := []string{}
		for _, arg := range f[5:] {
			if strings.Contains(arg, "=") {
				key, value, _ := strings.Cut(arg, "=")
				if key == "level" || key == "value" || key == "ramptime" {
					args = append(args, value)
				}
				if strings.EqualFold(key, "sourceunit") || strings.EqualFold(key, "sessionid") || strings.EqualFold(key, "commandid") || key == "level" || key == "value" || key == "ramptime" {
					continue
				}
			}
			args = append(args, arg)
		}
		canonical = strings.Join(append([]string{application, f[4], address}, args...), " ")
	} else if f[1] == "701" || f[1] == "730" || f[1] == "738" {
		parts, ok := deviceAddress(address)
		if !ok || len(parts) != 4 || applicationType(parts[2]) != "lighting" {
			return line, metadata
		}
		level, ramp := "", ""
		for _, arg := range f[3:] {
			key, value, _ := strings.Cut(arg, "=")
			if key == "level" {
				level = value
			}
			if strings.EqualFold(key, "ramptime") {
				ramp = value
			}
		}
		if level != "" {
			canonical = "lighting level " + address + " " + level
			if ramp != "" {
				canonical = "lighting ramp " + address + " " + level + " " + ramp
			}
		}
	}
	if canonical == "" {
		return line, metadata
	}
	metadata["timestamp"] = f[0]
	return canonical, metadata
}

func validEventTimestamp(s string) bool {
	if len(s) < 15 || s[8] != '-' {
		return false
	}
	for i, c := range s {
		if i == 8 || (i == 15 && c == '.') {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Addresses used in generated commands are strictly validated, including every
// numeric component, so topics cannot inject additional C-Gate arguments.
func deviceAddress(address string) ([]string, bool) {
	if !strings.HasPrefix(address, "//") {
		return nil, false
	}
	parts := strings.Split(address[2:], "/")
	if len(parts) < 2 || len(parts) > 5 || parts[0] == "" || strings.ContainsAny(parts[0], " \t\r\n\x00+#") {
		return nil, false
	}
	for _, c := range parts[0] {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return nil, false
		}
	}
	for _, part := range parts[1:] {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 || strconv.Itoa(n) != part {
			return nil, false
		}
	}
	return parts, true
}

func applicationType(app string) string {
	n, _ := strconv.Atoi(app)
	switch {
	case n >= 48 && n <= 95, n == 38, n == 136:
		return "lighting"
	case n == 25:
		return "temperature"
	case n == 228:
		return "measurement"
	case n == 208:
		return "security"
	}
	return ""
}

func (m *eventMapper) statusTopic(address string) (string, error) {
	if m.refresh != nil {
		if err := m.refresh(); err != nil {
			return "", fmt.Errorf("load devices: %w", err)
		}
	}
	path := normalizeName(m.names[address])
	if path == "" {
		path = strings.TrimPrefix(address, "//")
	}
	return strings.TrimRight(m.baseTopic, "/") + "/" + path, nil
}

var measurementUnits = []string{
	"°C", "A", "degrees", "C", "boolean", "F", "H", "Hz", "J", "kat", "kg/m³", "kg", "L", "L/h", "L/min", "L/s", "lx",
	"m", "m/min", "m/s", "m/s²", "mol", "N·m", "N", "Ω", "Pa", "%", "dB", "ppm", "rpm", "s", "min", "h", "Sv", "sr", "T", "V", "Wh", "W", "Wb",
}

func measurementStatus(args []string) (map[string]any, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("measurement requires mantissa, exponent and unit code")
	}
	mantissa, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid measurement mantissa")
	}
	exponent, err := strconv.Atoi(args[1])
	if err != nil || exponent < -128 || exponent > 127 {
		return nil, fmt.Errorf("invalid measurement exponent")
	}
	code, err := strconv.Atoi(args[2])
	if err != nil || code < 0 || code > 255 {
		return nil, fmt.Errorf("invalid measurement unit code")
	}
	value := float64(mantissa) * math.Pow10(exponent)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("nonfinite measurement")
	}
	status := map[string]any{"type": "measurement", "command": "data", "value": value, "mantissa": mantissa, "exponent": exponent, "unit_code": code}
	if code < len(measurementUnits) {
		status["unit"] = measurementUnits[code]
	} else if code == 254 {
		status["unit"] = "unitless"
	} else if code == 255 {
		status["unit"] = "custom"
	}
	return status, nil
}

func (m *eventMapper) deviceStatus(line string, f []string) (string, string, error) {
	parts, valid := deviceAddress(f[2])
	if !valid {
		return m.baseTopic, line, nil
	}
	var status map[string]any
	var err error
	suffix := ""
	switch f[0] {
	case "measurement":
		if f[1] != "data" || len(parts) != 5 {
			return m.baseTopic, line, nil
		}
		status, err = measurementStatus(f[3:])
	case "temperature":
		if f[1] != "broadcast" || len(parts) != 4 {
			return m.baseTopic, line, nil
		}
		if len(f) != 4 {
			return "", "", fmt.Errorf("temperature broadcast requires one reading")
		}
		value, parseErr := strconv.ParseFloat(f[3], 64)
		if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return "", "", fmt.Errorf("invalid temperature reading")
		}
		status = map[string]any{"type": "temperature", "command": "broadcast", "value": value, "unit": "°C"}
	case "security":
		if len(parts) != 3 && len(parts) != 4 {
			return m.baseTopic, line, nil
		}
		status, suffix, err = securityStatus(f[1], f[3:])
		if status == nil && err == nil {
			return m.baseTopic, line, nil
		}
	}
	if err != nil {
		return "", "", err
	}
	topic, err := m.statusTopic(f[2])
	if err != nil {
		return "", "", err
	}
	encoded, err := json.Marshal(status)
	return topic + suffix, string(encoded), err
}

func securityStatus(command string, args []string) (map[string]any, string, error) {
	status := map[string]any{"type": "security", "command": command}
	suffix := ""
	flags := map[string]struct {
		category string
		value    bool
	}{
		"system_disarmed": {"armed", false}, "alarm_on": {"alarm", true}, "alarm_off": {"alarm", false},
		"tamper_on": {"tamper", true}, "tamper_off": {"tamper", false}, "panic_activated": {"panic", true}, "panic_cleared": {"panic", false},
		"zone_unsealed": {"zone", true}, "zone_sealed": {"zone", false}, "zone_open": {"zone", true}, "zone_short": {"zone", true}, "zone_isolated": {"zone", true},
		"low_battery_detected": {"battery", true}, "low_battery_corrected": {"battery", false},
		"exit_delay_started": {"exit-delay", true}, "entry_delay_started": {"entry-delay", true},
		"mains_failure": {"mains", false}, "mains_restored": {"mains", true}, "arm_ready": {"ready", true}, "arm_not_ready": {"ready", false},
	}
	if flag, ok := flags[command]; ok {
		if flag.category == "zone" && len(args) == 1 {
			zone, err := strconv.Atoi(args[0])
			if err != nil || zone < 0 || zone > 255 {
				return nil, "", fmt.Errorf("invalid security zone number")
			}
			status["zone"] = zone
		} else if len(args) != 0 {
			return nil, "", fmt.Errorf("unexpected arguments for security %s", command)
		}
		suffix = "/" + flag.category
		status["value"] = flag.value
		if command == "system_disarmed" {
			status["value"] = 0 // Same numeric scale as system_armed and ArmState.
		}
	} else {
		switch command {
		case "status_request":
			if len(args) != 1 || (args[0] != "1" && args[0] != "2") {
				return nil, "", fmt.Errorf("security status_request requires report 1 or 2")
			}
			value, _ := strconv.Atoi(args[0])
			status["value"] = value
			suffix = "/status/request/" + args[0]
		case "system_armed", "current_alarm_type", "password_entry_status":
			if len(args) != 1 {
				return nil, "", fmt.Errorf("security %s requires one value", command)
			}
			value, err := strconv.Atoi(args[0])
			if err != nil || value < 0 || value > 255 {
				return nil, "", fmt.Errorf("invalid security %s value", command)
			}
			status["value"] = value
			suffix = "/" + map[string]string{"system_armed": "armed", "current_alarm_type": "alarm-type", "password_entry_status": "password", "exit_delay_started": "exit-delay"}[command]
		case "zone_name":
			if len(args) == 0 {
				return nil, "", fmt.Errorf("missing security zone name")
			}
			status["value"] = strings.Trim(strings.Join(args, " "), "\"")
			suffix = "/zone-name"
		case "battery_charging":
			if len(args) != 1 || (args[0] != "started" && args[0] != "stopped") {
				return nil, "", fmt.Errorf("battery_charging requires started or stopped")
			}
			status["value"] = args[0] == "started"
			suffix = "/charging"
		case "status_report_1", "status_report_2":
			count, offset := 35, 3
			start := 1
			if command == "status_report_2" {
				count, offset, start = 48, 0, 33
			}
			if len(args) != count {
				return nil, "", fmt.Errorf("security %s requires %d values", command, count)
			}
			values := make([]int, count)
			for i, arg := range args {
				n, err := strconv.Atoi(arg)
				max := 3
				if i < offset {
					max = 255
				}
				if err != nil || n < 0 || n > max {
					return nil, "", fmt.Errorf("invalid security status value")
				}
				values[i] = n
			}
			if offset != 0 {
				status["armed_state"], status["tamper_state"], status["panic_state"] = values[0], values[1], values[2]
			}
			status["zone_start"], status["zones"] = start, values[offset:]
			suffix = "/status/" + strings.TrimPrefix(command, "status_report_")
		default:
			return nil, "", nil
		}
	}
	return status, suffix, nil
}
