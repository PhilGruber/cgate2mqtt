package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
)

func testListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func TestBridgeStatusControlIntegration(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("devices.yaml", []byte("- address: //JERV/254/56/29\n  friendly_name: room\n- address: //JERV/254/228/1/2\n  friendly_name: sensor\n- address: //JERV/254/208\n  friendly_name: alarm\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, events, cgate := testListener(t), testListener(t), testListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var brokerWrite sync.Mutex
	connected := make(chan *packets.ConnectPacket, 1)
	brokerConn := make(chan net.Conn, 1)
	publications := make(chan *packets.PublishPacket, 128)
	errors := make(chan error, 3)
	writePacket := func(conn net.Conn, packet packets.ControlPacket) error {
		brokerWrite.Lock()
		defer brokerWrite.Unlock()
		return packet.Write(conn)
	}
	go func() {
		conn, err := broker.Accept()
		if err != nil {
			errors <- err
			return
		}
		defer conn.Close()
		brokerConn <- conn
		for {
			packet, err := packets.ReadPacket(conn)
			if err != nil {
				return
			}
			switch packet := packet.(type) {
			case *packets.ConnectPacket:
				connected <- packet
				err = writePacket(conn, packets.NewControlPacket(packets.Connack))
			case *packets.PublishPacket:
				publications <- packet
				if packet.Qos == 1 {
					ack := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
					ack.MessageID = packet.MessageID
					err = writePacket(conn, ack)
				}
			case *packets.SubscribePacket:
				ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
				ack.MessageID, ack.ReturnCodes = packet.MessageID, []byte{1}
				err = writePacket(conn, ack)
			case *packets.PingreqPacket:
				err = writePacket(conn, packets.NewControlPacket(packets.Pingresp))
			case *packets.DisconnectPacket:
				return
			}
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	eventConn := make(chan net.Conn, 1)
	go func() {
		conn, err := events.Accept()
		if err != nil {
			errors <- err
			return
		}
		eventConn <- conn
	}()
	wires := make(chan string, 128)
	go func() {
		conn, err := cgate.Accept()
		if err != nil {
			errors <- err
			return
		}
		defer conn.Close()
		if _, err := fmt.Fprint(conn, "201 Service ready\r\n"); err != nil {
			errors <- err
			return
		}
		reader := bufio.NewScanner(conn)
		for reader.Scan() {
			command := reader.Text()
			wires <- command
			reply := "200 OK\r\n"
			switch command {
			case "get //JERV/254/56/29 level":
				reply = "300-//JERV/254/56/29: level=128\r\n200 OK\r\n"
			case "get //JERV/254/228/1/2 data":
				reply = "300 //JERV/254/228/1/2: data=235,-1,0,100\r\n"
			case "get //JERV/254/208 armstate":
				reply = "300 //JERV/254/208: ArmState=0\r\n"
			case "tree //JERV/254":
				reply = "320-//JERV/254/56/29 ($1d) level=128 state=ok\r\n320 -end-\r\n"
			case "terminate_ramp //JERV/254/56/29":
				reply = "400 Syntax error\r\n"
			case "on //JERV/254/56/29":
				reply = "408 Operation failed\r\n"
			}
			if _, err := fmt.Fprint(conn, reply); err != nil {
				errors <- err
				return
			}
		}
	}()
	cfg := config{eventsAddr: events.Addr().String(), commandsAddr: cgate.Addr().String(), broker: "tcp://" + broker.Addr().String(), clientID: "test", eventsTopic: "cbus", commandsTopic: "cbus", startupState: true, retainState: true}
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() { defer close(stopped); done <- run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("bridge did not stop during cleanup")
		}
	})
	awaitPublish := func(topic string, match func(*packets.PublishPacket) bool) *packets.PublishPacket {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case packet := <-publications:
				if packet.TopicName == topic && match(packet) {
					return packet
				}
			case err := <-errors:
				t.Fatalf("test server: %v", err)
			case err := <-done:
				t.Fatalf("bridge stopped: %v", err)
			case <-timer.C:
				t.Fatalf("no publication on %s", topic)
			}
		}
	}
	awaitPublish("cbus/bridge/test/availability", func(p *packets.PublishPacket) bool { return string(p.Payload) == "online" && p.Retain })
	select {
	case packet := <-connected:
		if !packet.WillFlag || !packet.WillRetain || packet.WillTopic != "cbus/bridge/test/availability" || string(packet.WillMessage) != "offline" {
			t.Fatal("incorrect MQTT will")
		}
	default:
		t.Fatal("missing CONNECT")
	}
	startup := map[string]bool{}
	for len(wires) > 0 {
		startup[<-wires] = true
	}
	for _, command := range []string{"get //JERV/254/56/29 level", "get //JERV/254/228/1/2 data", "get //JERV/254/208 armstate", "getstate //JERV/254", "tree //JERV/254", "security status_request //JERV/254/208 1", "security status_request //JERV/254/208 2"} {
		if !startup[command] {
			t.Errorf("missing startup command %s", command)
		}
	}
	var mqttConn, stream net.Conn
	select {
	case mqttConn = <-brokerConn:
	case <-time.After(5 * time.Second):
		t.Fatal("no broker connection")
	}
	select {
	case stream = <-eventConn:
	case <-time.After(5 * time.Second):
		t.Fatal("no event connection")
	}
	defer stream.Close()
	send := func(topic, payload string, retained bool) {
		t.Helper()
		packet := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
		packet.TopicName, packet.Payload, packet.Retain = topic, []byte(payload), retained
		if err := writePacket(mqttConn, packet); err != nil {
			t.Fatal(err)
		}
	}
	send("cbus/room/set", `{"command":"on"}`, true)
	send("cbus/room/set", `{"command":"get_level"}`, false)
	p := awaitPublish("cbus/room", func(p *packets.PublishPacket) bool { return decodeStatus(t, string(p.Payload))["command"] == "level" })
	if !p.Retain || decodeStatus(t, string(p.Payload))["value"] != float64(128) {
		t.Fatal("query state not retained")
	}
	p = awaitPublish("cbus/room/result", func(p *packets.PublishPacket) bool { return decodeStatus(t, string(p.Payload))["ok"] == true })
	if p.Retain {
		t.Fatal("command result retained")
	}
	if command := <-wires; command != "get //JERV/254/56/29 level" {
		t.Fatalf("retained command was executed: %s", command)
	}
	send("cbus/room/set", `{"command":"terminate_ramp"}`, false)
	awaitPublish("cbus/room/result", func(p *packets.PublishPacket) bool {
		return strings.HasPrefix(decodeStatus(t, string(p.Payload))["command"].(string), "terminate_ramp ")
	})
	if command := <-wires; command != "terminate_ramp //JERV/254/56/29" {
		t.Fatal(command)
	}
	if command := <-wires; command != "terminateramp //JERV/254/56/29" {
		t.Fatal(command)
	}
	send("cbus/room/set", `{"command":"on"}`, false)
	p = awaitPublish("cbus/room/result", func(p *packets.PublishPacket) bool { return decodeStatus(t, string(p.Payload))["ok"] == false })
	if decodeStatus(t, string(p.Payload))["code"] != float64(408) {
		t.Fatal(string(p.Payload))
	}
	send("cbus/room/set", `{"command":"scene"}`, false)
	awaitPublish("cbus/room/result", func(p *packets.PublishPacket) bool { return strings.Contains(string(p.Payload), "unsupported") })
	for _, tc := range []struct{ line, topic, kind string }{
		{"lighting off //JERV/254/56/29", "cbus/room", "lighting"},
		{"measurement data //JERV/254/228/1/2 241 -1 0", "cbus/sensor", "measurement"},
		{"temperature broadcast //JERV/254/25/1 18.5", "cbus/JERV/254/25/1", "temperature"},
		{"#security alarm_on //JERV/254/208 #sourceunit=10", "cbus/alarm/alarm", "security"},
		{"20261012-100000 702 //JERV/254/208 [security] mains_restored sourceUnit=10", "cbus/alarm/mains", "security"},
		{"clock date //JERV/254/223 2026-10-12 0", "cbus/JERV/254/223/date", "clock"},
	} {
		if _, err := fmt.Fprintln(stream, tc.line); err != nil {
			t.Fatal(err)
		}
		packet := awaitPublish(tc.topic, func(p *packets.PublishPacket) bool { return decodeStatus(t, string(p.Payload))["type"] == tc.kind })
		if !packet.Retain {
			t.Fatal("state not retained")
		}
	}
	cancel()
	// Shutdown publishes retained offline before disconnecting from MQTT.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	offline := false
	for !offline {
		select {
		case p := <-publications:
			offline = p.TopicName == "cbus/bridge/test/availability" && string(p.Payload) == "offline" && p.Retain
		case <-timer.C:
			t.Fatal("missing shutdown availability")
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not stop")
	}
}
