package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func decodeStatus(t *testing.T, payload string) map[string]any {
	t.Helper()
	var status map[string]any
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		t.Fatalf("invalid JSON %q: %v", payload, err)
	}
	return status
}

func TestSensorAndStructuredEvents(t *testing.T) {
	m := eventMapper{baseTopic: "cbus", names: map[string]string{"//JERV/254/56/29": "room", "//JERV/254/228/1/2": "sensor"}}
	for _, tc := range []struct {
		line, topic, kind string
		value             any
		unit              string
	}{
		{"measurement data //JERV/254/228/1/2 -1234 -2 0", "cbus/sensor", "measurement", -12.34, "°C"},
		{"measurement data //JERV/254/228/1/3 42 0 254", "cbus/JERV/254/228/1/3", "measurement", float64(42), "unitless"},
		{"temperature broadcast //JERV/254/25/1 -4.5", "cbus/JERV/254/25/1", "temperature", -4.5, "°C"},
		{"20261012-100000.123 702 //JERV/254/228/1/2 [measurement] data 235 -1 0 sourceUnit=10", "cbus/sensor", "measurement", 23.5, "°C"},
		{"20261012-100000 730 //JERV/254/56/29 New level=128 sourceunit=9 ramptime=4", "cbus/room", "lighting", float64(128), ""},
		{"20261012-100000 701 //JERV/254/56/29 level=0", "cbus/room", "lighting", float64(0), ""},
	} {
		t.Run(tc.line, func(t *testing.T) {
			topic, payload, err := m.message(tc.line)
			if err != nil || topic != tc.topic {
				t.Fatalf("got %q %q, %v", topic, payload, err)
			}
			status := decodeStatus(t, payload)
			if status["type"] != tc.kind || status["value"] != tc.value || (tc.unit != "" && status["unit"] != tc.unit) {
				t.Fatalf("got %#v", status)
			}
			if strings.HasPrefix(tc.line, "2026") && status["timestamp"] == nil {
				t.Fatal("timestamp was lost")
			}
		})
	}
	for _, line := range []string{
		"measurement data //JERV/254/228/1/2 1 999 0", "measurement data //JERV/254/228/1/2 x 0 0", "measurement data //JERV/254/228/1/2 1 0 256", "measurement data //JERV/254/228/1/2 1 0",
		"temperature broadcast //JERV/254/25/1 NaN", "temperature broadcast //JERV/254/25/1 +Inf", "temperature broadcast //JERV/254/25/1",
	} {
		if _, _, err := m.message(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
	_, payload, err := m.message("20261012-100000 702 //JERV/254/56/29 [lighting] off sourceUnit=10")
	if err != nil || decodeStatus(t, payload)["source_unit"] != float64(10) {
		t.Fatalf("metadata: %q, %v", payload, err)
	}
}

func TestSecurityStatus(t *testing.T) {
	m := eventMapper{baseTopic: "cbus", names: map[string]string{"//JERV/254/208": "alarm"}}
	for _, tc := range []struct {
		line, suffix string
		value        any
	}{
		{"#security system_armed //JERV/254/208 1 #sourceunit=10", "/armed", float64(1)},
		{"# security system_disarmed //JERV/254/208", "/armed", float64(0)},
		{"20261012-100000 702 //JERV/254/208 [security] alarm_on sourceUnit=10", "/alarm", true},
		{"security alarm_off //JERV/254/208", "/alarm", false},
		{"security low_battery_detected //JERV/254/208", "/battery", true},
		{"security low_battery_corrected //JERV/254/208", "/battery", false},
		{"security battery_charging //JERV/254/208 started", "/charging", true},
		{"security battery_charging //JERV/254/208 stopped", "/charging", false},
		{"security exit_delay_started //JERV/254/208", "/exit-delay", true},
		{"security mains_failure //JERV/254/208", "/mains", false},
		{"security mains_restored //JERV/254/208", "/mains", true},
		{"security arm_ready //JERV/254/208", "/ready", true},
		{"security arm_not_ready //JERV/254/208", "/ready", false},
	} {
		topic, payload, err := m.message(tc.line)
		if err != nil || topic != "cbus/alarm"+tc.suffix || decodeStatus(t, payload)["value"] != tc.value {
			t.Fatalf("%s: %q %q, %v", tc.line, topic, payload, err)
		}
	}
	for _, command := range []string{"zone_unsealed", "zone_sealed", "zone_open", "zone_short", "zone_isolated"} {
		topic, payload, err := m.message("#security " + command + " //JERV/254/208/7")
		if err != nil || topic != "cbus/JERV/254/208/7/zone" || decodeStatus(t, payload)["command"] != command {
			t.Fatal(topic, payload, err)
		}
	}
	for _, tc := range []struct {
		command, args, suffix string
		start, count          int
	}{
		{"status_report_1", "1 0 255" + strings.Repeat(" 0", 32), "/status/1", 1, 32},
		{"status_report_2", strings.TrimSpace(strings.Repeat(" 1", 48)), "/status/2", 33, 48},
	} {
		topic, payload, err := m.message("#security " + tc.command + " //JERV/254/208 " + tc.args + " #sourceunit=10")
		if err != nil || topic != "cbus/alarm"+tc.suffix {
			t.Fatal(topic, payload, err)
		}
		status := decodeStatus(t, payload)
		if status["zone_start"] != float64(tc.start) || len(status["zones"].([]any)) != tc.count {
			t.Fatal(status)
		}
	}
	for _, line := range []string{"security status_report_1 //JERV/254/208 1 0 0", "security system_armed //JERV/254/208 bad", "security battery_charging //JERV/254/208 yes", "security alarm_on //JERV/254/208 1"} {
		if _, _, err := m.message(line); err == nil {
			t.Errorf("accepted %s", line)
		}
	}
}

func TestSecurityStatusRequestEvent(t *testing.T) {
	line := "security status_request //JERV/254/208 2 #sourceunit=255 OID= sessionId=cmd5 commandId={none}"
	for _, name := range []string{"", "alarm"} {
		m := eventMapper{baseTopic: "cbus", names: map[string]string{"//JERV/254/208": name}}
		topic, payload, err := m.message(line)
		path := "JERV/254/208"
		if name != "" {
			path = name
		}
		if err != nil || topic != "cbus/"+path+"/status/request/2" {
			t.Fatalf("got %q %q, %v", topic, payload, err)
		}
		status := decodeStatus(t, payload)
		want := map[string]any{"type": "security", "command": "status_request", "value": float64(2), "source_unit": float64(255)}
		if !reflect.DeepEqual(status, want) {
			t.Fatalf("got %#v, want %#v", status, want)
		}
		if _, retained := statePublication(topic, payload); retained {
			t.Fatal("request event retained as device state")
		}
	}
	m := eventMapper{baseTopic: "cbus"}
	for _, args := range []string{"", "3", "2 extra"} {
		if _, _, err := m.message("security status_request //JERV/254/208 " + args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}

func TestDeviceStatusCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.yaml")
	if err := os.WriteFile(path, []byte("- address: //JERV/254/56/29\n  friendly_name: room\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ target, payload, want string }{
		{"room", `{"command":"get_level"}`, "get //JERV/254/56/29 level"},
		{"room", `{"type":"lighting","command":"terminate_ramp"}`, "terminate_ramp //JERV/254/56/29"},
		{"JERV/254/228/1/2", `{"command":"get_data"}`, "get //JERV/254/228/1/2 data"},
		{"JERV/254/25/1", `{"command":"get_temperature"}`, "get //JERV/254/25/1 temperature"},
		{"JERV/254/208", `{"command":"get_arm_state"}`, "get //JERV/254/208 armstate"},
		{"JERV/254/208", `{"command":"status_request","value":2}`, "security status_request //JERV/254/208 2"},
		{"JERV/254/208", `{"command":"arm","mode":"night"}`, "security arm //JERV/254/208 night"},
		{"JERV/254/208/7", `{"command":"request_zone_name"}`, "security request_zone_name //JERV/254/208/7"},
	} {
		got, err := mqttCommand("cbus", "cbus/"+tc.target+"/set", []byte(tc.payload), path)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q %v", tc.payload, got, err)
		}
	}
	for _, tc := range []struct{ target, payload string }{
		{"room", `{"command":"get_level","value":1}`}, {"room", `{"command":"arm","mode":"away"}`},
		{"JERV/254/208/7", `{"command":"arm","mode":"away"}`}, {"JERV/254/208", `{"command":"arm","mode":"disarm"}`},
		{"JERV/254/208", `{"command":"status_request","value":3}`}, {"JERV/254/228/1", `{"command":"get_data"}`},
		{"JERV/254/202/1", `{"command":"event","value":2}`}, {"room", `{"type":"security","command":"on"}`},
		{"JERV/254/56/29 on", `{"command":"on"}`},
	} {
		if _, err := mqttCommand("cbus", "cbus/"+tc.target+"/set", []byte(tc.payload), path); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	for _, raw := range []string{"set //JERV/254/56/29 level 1", "project start JERV", "trigger event //JERV/254/202/1 1", "scene play house evening", "net sync //JERV/254", "ramp //JERV/254/56/29 1 NaN", "security arm //JERV/254/208 away extra"} {
		if _, err := rawDeviceCommand(raw); err == nil {
			t.Errorf("accepted raw %q", raw)
		}
	}
}

func TestQueryRepliesAndCommandResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.yaml")
	if err := os.WriteFile(path, []byte("- address: //JERV/254/56/29\n  friendly_name: room\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := deviceCommand{wire: "get //JERV/254/56/29 level", address: "//JERV/254/56/29", parameter: "level"}
	var published []struct {
		topic, payload string
		retain         bool
	}
	publish := func(topic, payload string, retain bool) error {
		published = append(published, struct {
			topic, payload string
			retain         bool
		}{topic, payload, retain})
		return nil
	}
	complete := commandCompletion(command, "cbus/room/result", "cbus", path, publish)
	if err := complete([]string{"300-//JERV/254/56/29: level=128", "200 OK"}); err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || published[0].topic != "cbus/room" || !published[0].retain || published[1].retain || decodeStatus(t, published[1].payload)["ok"] != true {
		t.Fatal(published)
	}
	for _, replies := range [][]string{{"400 Bad object"}, {"300-//JERV/254/56/29: level=128", "408 Failed"}, {"300 //JERV/254/56/29: level=-1"}, {"200 OK"}} {
		published = nil
		if err := complete(replies); err != nil {
			t.Fatal(err)
		}
		if len(published) != 1 || published[0].topic != "cbus/room/result" || decodeStatus(t, published[0].payload)["ok"] != false {
			t.Fatal(published)
		}
	}
	for _, tc := range []struct{ parameter, value, want string }{
		{"data", "235,-1,0,100", "measurement data //JERV/254/228/1/2 235 -1 0"},
		{"temperature", "-4.5", "temperature broadcast //JERV/254/228/1/2 -4.5"},
		{"armstate", "0", "security system_disarmed //JERV/254/228/1/2"},
	} {
		messages, err := queryMessages(deviceCommand{address: "//JERV/254/228/1/2", parameter: tc.parameter}, []string{"300 //JERV/254/228/1/2: " + tc.parameter + "=" + tc.value})
		if err != nil || !reflect.DeepEqual(messages, []string{tc.want}) {
			t.Fatal(messages, err)
		}
	}
	if _, err := queryMessages(deviceCommand{address: "//JERV/254/228/1/2", parameter: "data"}, []string{"300 //JERV/254/228/1/2: data=0,0,0,-1"}); err == nil {
		t.Fatal("published a nonexistent measurement")
	}
	messages, err := queryMessages(deviceCommand{parameter: "tree"}, []string{"320-//JERV/254/p/1 type=RELAY", "320- //JERV/254/56/29 ($1d) level=128 state=ok", "320- //JERV/254/56/30 ($1e) level=-1 state=new", "320 -end-"})
	if err != nil || !reflect.DeepEqual(messages, []string{"lighting level //JERV/254/56/29 128"}) {
		t.Fatal(messages, err)
	}
}

func TestStartupCommandsAndRetention(t *testing.T) {
	commands, err := startupCommands([]device{{Address: "//JERV/254/56/29"}, {Address: "//JERV/254/208/7"}, {Address: "//JERV/254/228/1/2"}, {Address: "//JERV/254/202/1"}}, "//OTHER/1")
	if err != nil {
		t.Fatal(err)
	}
	wires := map[string]bool{}
	for _, command := range commands {
		wires[command.wire] = true
		if strings.Contains(command.wire, "trigger") {
			t.Fatal(command)
		}
	}
	for _, want := range []string{"tree //JERV/254", "getstate //OTHER/1", "get //JERV/254/56/29 level", "get //JERV/254/228/1/2 data", "get //JERV/254/208 armstate", "security status_request //JERV/254/208 1", "security status_request //JERV/254/208 2"} {
		if !wires[want] {
			t.Errorf("missing %s", want)
		}
	}
	if _, err := startupCommands(nil, "//JERV/254 set"); err == nil {
		t.Fatal("accepted unsafe network")
	}
	for _, tc := range []struct {
		payload, suffix string
		retained        bool
	}{
		{`{"type":"lighting","command":"off"}`, "", true}, {`{"type":"measurement","command":"data"}`, "", true},
		{`{"type":"clock","command":"date"}`, "/date", true}, {`{"type":"clock","command":"time"}`, "/time", true},
		{"raw diagnostic", "", false}, {`{"command":"off","ok":true}`, "", false},
	} {
		topic, retained := statePublication("cbus/device", tc.payload)
		if topic != "cbus/device"+tc.suffix || retained != tc.retained {
			t.Fatal(topic, retained)
		}
	}
}
