package main

import (
	"errors"
	"testing"
)

func TestNamedEvents(t *testing.T) {
	lookups := 0
	m := eventMapper{baseTopic: "cbus/events", names: make(map[string]string), lookup: func(address string) (string, error) {
		lookups++
		if address != "//JERV/254/56/22" {
			t.Fatalf("lookup address %q", address)
		}
		return "Living Room", nil
	}}
	for _, state := range []string{"on", "off"} {
		topic, payload, err := m.message("lighting " + state + " //JERV/254/56/22")
		if err != nil {
			t.Fatal(err)
		}
		if topic != "cbus/events/JERV/living-room" || payload != `{"command":"`+state+`"}` {
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
	topic, payload, err := m.message("lighting on //JERV/254/56/22")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "custom/events/JERV/254/56/22" || payload != `{"command":"on"}` {
		t.Fatalf("fallback: %q %q", topic, payload)
	}
	topic, _, err = m.message("lighting off //JERV/254/56/22")
	if err != nil {
		t.Fatal(err)
	}
	if topic != "custom/events/JERV/kitchen" {
		t.Fatalf("retry topic: %q", topic)
	}
}

func TestOtherEventsUnchanged(t *testing.T) {
	m := eventMapper{baseTopic: "cbus/events", lookup: func(string) (string, error) {
		t.Fatal("unexpected lookup")
		return "", nil
	}}
	for _, line := range []string{"lighting on //JERV/254", "lighting on //JERV/+/56/22", "20261006-230000 730 //JERV/254/56/22 on"} {
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
		{"300 //JERV/254/56/22: name=Living Room"},
		{"300-//JERV/254/56/22: name=\"Living Room\"", "200 OK"},
	} {
		name, err := parseNameReply(lines)
		if err != nil || name != "Living Room" {
			t.Fatalf("got %q, %v", name, err)
		}
	}
	for _, lines := range [][]string{{"400 Bad object"}, {"300 //JERV/254/56/22: name="}, {"200 OK"}, {"300-//JERV/254/56/22: name=Room", "400 Failed"}} {
		if _, err := parseNameReply(lines); err == nil {
			t.Fatalf("accepted %q", lines)
		}
	}
}

func TestParsedEventArguments(t *testing.T) {
	m := eventMapper{baseTopic: "cbus/events", names: map[string]string{"//JERV/254/56/22": "room"}}
	for _, tc := range []struct{ line, want string }{
		{"lighting ramp //JERV/254/56/22 0 4 # source metadata", `{"command":"ramp","value":0,"time":4}`},
		{"lighting ramp //JERV/254/56/22 128 0.5#comment", `{"command":"ramp","value":128,"time":0.5}`},
		{"lighting on //JERV/254/56/22 # comment", `{"command":"on"}`},
		{"lighting ramp //JERV/254/56/22 0", `{"command":"ramp","value":0}`},
		{"# comment", ""},
	} {
		_, got, err := m.message(tc.line)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.line, got, err, tc.want)
		}
	}
	for _, line := range []string{
		"lighting ramp //JERV/254/56/22 bad 4",
		"lighting ramp //JERV/254/56/22 0 bad",
		"lighting ramp //JERV/254/56/22 0 4 extra",
		"lighting ramp //JERV/254/56/22 NaN 4",
	} {
		if _, _, err := m.message(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}

func TestFriendlyNameTopicPath(t *testing.T) {
	m := eventMapper{
		baseTopic: "cbus/events",
		names:     map[string]string{"//JERV/254/56/35": "bedroom/tv"},
		lookup: func(string) (string, error) {
			t.Fatal("looked up address with saved friendly name")
			return "", nil
		},
	}
	topic, payload, err := m.message("lighting on //JERV/254/56/35")
	if err != nil || topic != "cbus/events/JERV/bedroom/tv" || payload != `{"command":"on"}` {
		t.Fatalf("got %q %q, %v", topic, payload, err)
	}
}

func TestNormalizeNameTopicPaths(t *testing.T) {
	if got := normalizeName(" Bedroom/TV "); got != "bedroom/tv" {
		t.Fatalf("got %q", got)
	}
	for _, name := range []string{"bedroom/+", "bedroom/#", "bedroom\x00tv", "bedroom\ntv", "bedroom\rtv"} {
		if got := normalizeName(name); got != "" {
			t.Errorf("accepted invalid name %q as %q", name, got)
		}
	}
}
