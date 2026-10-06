package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestForwardEvents(t *testing.T) {
	var got []string
	err := forwardEvents(context.Background(), strings.NewReader("event one\r\nevent two\r\n"), func(s string) error { got = append(got, s); return nil })
	if err == nil || !reflect.DeepEqual(got, []string{"event one", "event two"}) {
		t.Fatalf("got %q, err %v", got, err)
	}
	want := errors.New("publish failed")
	if err := forwardEvents(context.Background(), strings.NewReader("event\n"), func(string) error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

// Events must be published while the socket stays open, including when TCP
// splits a line across reads or delivers several lines in one read.
func TestForwardEventsLiveStream(t *testing.T) {
	bridge, server := net.Pipe()
	defer bridge.Close()
	defer server.Close()
	received := make(chan string, 3)
	done := make(chan error, 1)
	go func() {
		done <- forwardEvents(context.Background(), bridge, func(line string) error {
			received <- line
			return nil
		})
	}()
	server.SetWriteDeadline(time.Now().Add(2 * time.Second))
	for _, fragment := range []string{"20261006-230000 730 //HOME/1/56/1 ", "on\r", "\nsecond event\r\nthird event\n"} {
		if _, err := io.WriteString(server, fragment); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"20261006-230000 730 //HOME/1/56/1 on", "second event", "third event"} {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded while connection remained open")
		}
	}
	server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected connection closed error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not stop after connection closed")
	}
}

func TestValidateCommand(t *testing.T) {
	for _, value := range []string{"", "  ", "on 1\noff 1", "on 1\roff 1", "on\x00", strings.Repeat("x", 4097)} {
		if _, err := validateCommand(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if got, err := validateCommand(" on 1/56/1\r\n"); err != nil || got != "on 1/56/1" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestForwardCommands(t *testing.T) {
	bridge, server := net.Pipe()
	defer bridge.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands := make(chan commandRequest, 3)
	replies := make(chan []string, 1)
	commands <- commandRequest{command: "noop"}
	commands <- commandRequest{command: "get //JERV/254/56/22 name", replies: replies}
	commands <- commandRequest{command: "on 1/56/1"}
	done := make(chan error, 1)
	go func() { done <- forwardCommands(ctx, bridge, commands, false, func() error { return nil }) }()
	server.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(server, "201 Service ready\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(server)
	for _, want := range []string{"noop\r\n", "get //JERV/254/56/22 name\r\n", "on 1/56/1\r\n"} {
		got, err := reader.ReadString('\n')
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
		reply := "200-first line\r\n200 OK\r\n"
		if strings.HasPrefix(want, "get ") {
			reply = "300-//JERV/254/56/22: name=Living Room\r\n200 OK\r\n"
		}
		if _, err := io.WriteString(server, reply); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case lines := <-replies:
		name, err := parseNameReply(lines)
		if err != nil || name != "Living Room" {
			t.Fatalf("lookup replies mixed with command replies: %q, %v", lines, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lookup reply was not delivered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestCommandGreetingFailure(t *testing.T) {
	bridge, server := net.Pipe()
	defer bridge.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		done <- forwardCommands(context.Background(), bridge, make(chan commandRequest), false, func() error { t.Error("subscribed before valid greeting"); return nil })
	}()
	server.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(server, "400 Access denied\r\n")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected greeting error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
