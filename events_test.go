package main

import (
	"errors"
	"testing"
)

func TestNamedEvents(t *testing.T) {
	lookups := 0
	m := eventMapper{baseTopic: "cbus", names: make(map[string]string), lookup: func(address string) (string, error) {
		lookups++
		if address != "//MYHOME/254/56/22" {
			t.Fatalf("lookup address %q", address)
		}
		return "Living Room", nil
	}}
	for _, state := range []string{"on", "off"} {
		topic, payload, err := m.message("lighting " + state + " //MYHOME/254/56/22")
		if err != nil {
			t.Fatal(err)
		}
		if topic != "cbus/living-room" || payload != `{"type":"lighting","command":"`+state+`"}` {
			t.Fatalf("got %q %q", topic, payload)
		}
	}
	if lookups != 1 {
		t.Fatalf("got %d lookups, want one cached lookup", lookups)
	}
}

func TestNameLookupFallbackAndRetry(t *testing.T) {
	lookups := 0
	m := eventMapper{baseTopic: "custom/events/", names: make(map[string]string), lookup: func(string) (string, error) {
		lookups++
		if lookups == 1 {
			return "", errors.New("name unavailable")
		}
		return "Kitchen", nil
	}}
	topic, payload, err := m.message("lighting on //MYHOME/254/56/22")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "custom/events/MYHOME/254/56/22" || payload != `{"type":"lighting","command":"on"}` {
		t.Fatalf("fallback: %q %q", topic, payload)
	}
	topic, _, err = m.message("lighting off //MYHOME/254/56/22")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "custom/events/kitchen" {
		t.Fatalf("retry topic: %q", topic)
	}
}

func TestOtherEventsUnchanged(t *testing.T) {
	m := eventMapper{baseTopic: "cbus", lookup: func(string) (string, error) {
		t.Fatal("unexpected lookup")
		return "", nil
	}}
	for _, line := range []string{"lighting on //MYHOME/254", "lighting on //MYHOME/+/56/22", "20261006-230000 730 //MYHOME/254/56/22 on"} {
		topic, payload, err := m.message(line)
		if err != nil {
			t.Fatal(err)
		}
		if topic != m.baseTopic || payload != line {
			t.Fatalf("changed unrecognized event: %q %q", topic, payload)
		}
	}
}

func TestParseNameReply(t *testing.T) {
	for _, lines := range [][]string{
		{"300 //MYHOME/254/56/22: name=Living Room"},
		{"300-//MYHOME/254/56/22: name=\"Living Room\"", "200 OK"},
	} {
		name, err := parseNameReply(lines)
		if err != nil || name != "Living Room" {
			t.Fatalf("got %q, %v", name, err)
		}
	}
	for _, lines := range [][]string{{"400 Bad object"}, {"300 //MYHOME/254/56/22: name="}, {"200 OK"}, {"300-//MYHOME/254/56/22: name=Room", "400 Failed"}} {
		if _, err := parseNameReply(lines); err == nil {
			t.Fatalf("accepted %q", lines)
		}
	}
}

func TestParsedEventArguments(t *testing.T) {
	m := eventMapper{baseTopic: "cbus", names: map[string]string{"//MYHOME/254/56/22": "room"}}
	for _, tc := range []struct{ line, want string }{
		{"lighting ramp //MYHOME/254/56/22 0 4 # source metadata", `{"type":"lighting","command":"ramp","value":0,"time":4}`},
		{"lighting ramp //MYHOME/254/56/22 128 0.5#comment", `{"type":"lighting","command":"ramp","value":128,"time":0.5}`},
		{"lighting on //MYHOME/254/56/22 # comment", `{"type":"lighting","command":"on"}`},
		{"lighting ramp //MYHOME/254/56/22 0", `{"type":"lighting","command":"ramp","value":0}`},
		{"# comment", ""},
	} {
		_, got, err := m.message(tc.line)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.line, got, err, tc.want)
		}
	}
	for _, line := range []string{
		"lighting ramp //MYHOME/254/56/22 bad 4",
		"lighting ramp //MYHOME/254/56/22 0 bad",
		"lighting ramp //MYHOME/254/56/22 0 4 extra",
		"lighting ramp //MYHOME/254/56/22 NaN 4",
	} {
		if _, _, err := m.message(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}

func TestFriendlyNameTopicPath(t *testing.T) {
	m := eventMapper{
		baseTopic: "cbus",
		names:     map[string]string{"//MYHOME/254/56/35": "bedroom/tv"},
		lookup: func(string) (string, error) {
			t.Fatal("looked up address with saved friendly name")
			return "", nil
		},
	}
	topic, payload, err := m.message("lighting on //MYHOME/254/56/35")
	if err != nil || topic != "cbus/bedroom/tv" || payload != `{"type":"lighting","command":"on"}` {
		t.Fatalf("got %q %q, %v", topic, payload, err)
	}
}

func TestNormalizeNameTopicPaths(t *testing.T) {
	if got := normalizeName(" Bedroom/TV "); got != "bedroom/tv" {
		t.Fatalf("got %q", got)
	}
	for _, name := range []string{"set", "bedroom/set", "bedroom/+", "bedroom/#", "bedroom\x00tv", "bedroom\ntv", "bedroom\rtv"} {
		if got := normalizeName(name); got != "" {
			t.Errorf("accepted invalid name %q as %q", name, got)
		}
	}
}

func TestClockEvents(t *testing.T) {
	m := eventMapper{baseTopic: "cbus/", lookup: func(string) (string, error) {
		t.Fatal("clock event triggered a name lookup")
		return "", nil
	}}
	for _, tc := range []struct{ line, want string }{
		{"clock date //MYHOME/254/223 2026-10-11 6", `{"type":"clock","command":"date","value":"2026-10-11","day_of_week":6}`},
		{"clock time //MYHOME/254/223 17:06:39 1", `{"type":"clock","command":"time","value":"17:06:39","daylight_saving":1}`},
		{"clock date //MYHOME/254/223 2026-10-12 0 # comment", `{"type":"clock","command":"date","value":"2026-10-12","day_of_week":0}`},
		{"clock time //MYHOME/254/223 00:00:00 0", `{"type":"clock","command":"time","value":"00:00:00","daylight_saving":0}`},
		{"clock time //MYHOME/254/223 17:06:39", `{"type":"clock","command":"time","value":"17:06:39"}`},
	} {
		topic, payload, err := m.message(tc.line)
		if err != nil || topic != "cbus/MYHOME/254/223" || payload != tc.want {
			t.Errorf("%q: got %q %q, %v", tc.line, topic, payload, err)
		}
	}
	for _, line := range []string{
		"clock date //MYHOME/254/223 2026-02-30 6",
		"clock date //MYHOME/254/223 2026-10-11 7",
		"clock time //MYHOME/254/223 24:00:00 1",
		"clock time //MYHOME/254/223 17:06:39 2",
		"clock time //MYHOME/254/223 17:06:39 bad",
		"clock time //MYHOME/254/223 17:06:39 1 extra",
		"clock date //MYHOME/254/223",
	} {
		if _, _, err := m.message(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}
