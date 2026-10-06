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
		topic, payload := m.message("lighting " + state + " //JERV/254/56/22")
		if topic != "cbus/events/JERV/living-room" || payload != `{"lighting":"`+state+`"}` {
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
	topic, payload := m.message("lighting on //JERV/254/56/22")
	if topic != "custom/events/JERV/254/56/22" || payload != `{"lighting":"on"}` {
		t.Fatalf("fallback: %q %q", topic, payload)
	}
	topic, _ = m.message("lighting off //JERV/254/56/22")
	if topic != "custom/events/JERV/kitchen" {
		t.Fatalf("retry topic: %q", topic)
	}
}

func TestOtherEventsUnchanged(t *testing.T) {
	m := eventMapper{baseTopic: "cbus/events", lookup: func(string) (string, error) {
		t.Fatal("unexpected lookup")
		return "", nil
	}}
	for _, line := range []string{"# comment", "lighting on //JERV/254", "lighting on //JERV/+/56/22", "20261006-230000 730 //JERV/254/56/22 on"} {
		topic, payload := m.message(line)
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
