package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDevicePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.yaml")
	store, err := loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	m := eventMapper{baseTopic: "cbus", names: map[string]string{}, record: store.record, lookup: func(string) (string, error) { return "", errors.New("no name") }}
	for i := 0; i < 2; i++ {
		if _, _, err := m.message("lighting on //HOME/1/56/1"); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.devices) != 1 || loaded.devices[0].FriendlyName != "" {
		t.Fatalf("unexpected devices: %+v", loaded.devices)
	}
	if err := os.WriteFile(path, []byte("- address: //HOME/1/56/1\n  friendly_name: My Kitchen\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	m.names[loaded.devices[0].Address] = loaded.devices[0].FriendlyName
	m.record = loaded.record
	m.lookup = func(string) (string, error) { t.Fatal("looked up custom name"); return "", nil }
	topic, _, err := m.message("lighting off //HOME/1/56/1")
	if err != nil || topic != "cbus/HOME/my-kitchen" {
		t.Fatalf("topic %q: %v", topic, err)
	}
	if err := loaded.record("//HOME/1/56/2", "living-room"); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.devices) != 2 || loaded.devices[0].FriendlyName != "My Kitchen" || loaded.devices[1].FriendlyName != "living-room" {
		t.Fatalf("unexpected devices: %+v", loaded.devices)
	}
}

func TestInvalidDevicesFile(t *testing.T) {
	for _, data := range []string{"invalid: [", "- address: //HOME/1/56/1\n  friendly_nam: typo\n", "- address: a\n- address: a\n"} {
		path := filepath.Join(t.TempDir(), "devices.yaml")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadDevices(path); err == nil {
			t.Fatalf("accepted %q", data)
		}
		got, _ := os.ReadFile(path)
		if string(got) != data {
			t.Fatal("modified invalid file")
		}
	}
}

func TestAllEventDevicesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.yaml")
	store, err := loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	mapper := eventMapper{baseTopic: "cbus", names: store.names, record: store.record, refresh: store.reload}
	for _, line := range []string{
		"clock date //HOME/1/223 2026-10-11 6",
		"clock time //HOME/1/223 17:06:39 1",
		"measurement value //HOME/1/228/2 21",
		"virtual update //HOME/virtual/sensor/temperature/inside 22",
		"network ready //HOME/1",
		"project ready //HOME",
		"20261011-170639 730 //HOME/1/p/7: changed",
		"unknown update //HOME/virtual/sensor/temperature/inside 23",
	} {
		if _, _, err := mapper.message(line); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
	loaded, err := loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"//HOME/1/223", "//HOME/1/228/2", "//HOME/virtual/sensor/temperature/inside", "//HOME/1", "//HOME", "//HOME/1/p/7"}
	if len(loaded.devices) != len(want) {
		t.Fatalf("unexpected devices: %+v", loaded.devices)
	}
	for i, address := range want {
		if loaded.devices[i].Address != address || loaded.devices[i].FriendlyName != "" {
			t.Fatalf("device %d: %+v", i, loaded.devices[i])
		}
	}
	if err := loaded.record(want[0], "House Clock"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mapper.message("clock time //HOME/1/223 18:00:00"); err != nil {
		t.Fatal(err)
	}
	if store.names[want[0]] != "House Clock" {
		t.Fatal("overwrote custom clock name")
	}
	mapper.record = func(string, string) error { return errors.New("write failed") }
	if _, _, err := mapper.message("virtual update //HOME/virtual/device 1"); err == nil {
		t.Fatal("ignored persistence failure for non-lighting event")
	}
}

func TestDeviceWriteFailure(t *testing.T) {
	store := &deviceStore{path: filepath.Join(t.TempDir(), "missing", "devices.yaml")}
	m := eventMapper{baseTopic: "events", names: map[string]string{}, record: store.record, lookup: func(string) (string, error) { return "Kitchen", nil }}
	if _, _, err := m.message("lighting on //HOME/1/56/1"); err == nil {
		t.Fatal("ignored persistence failure")
	}
}

func TestDeviceReloadBeforeRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.yaml")
	store, err := loadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.record("a", "Old Name"); err != nil {
		t.Fatal(err)
	}
	data := "- address: a\n  friendly_name: Edited Name\n- address: b\n  friendly_name: Added Name\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.record("c", "New Name"); err != nil {
		t.Fatal(err)
	}
	if len(store.devices) != 3 || store.names["a"] != "Edited Name" || store.names["b"] != "Added Name" || store.names["c"] != "New Name" {
		t.Fatalf("lost updates: %+v", store.devices)
	}
	mapper := eventMapper{baseTopic: "events", names: store.names, record: store.record, refresh: store.reload, lookup: func(string) (string, error) { t.Fatal("looked up saved name"); return "", nil }}
	if err := os.WriteFile(path, []byte("- address: //HOME/1/56/1\n  friendly_name: My Kitchen\n"), 0600); err != nil {
		t.Fatal(err)
	}
	topic, _, err := mapper.message("lighting on //HOME/1/56/1")
	if err != nil || topic != "events/HOME/my-kitchen" {
		t.Fatalf("topic %q: %v", topic, err)
	}
	invalid := "invalid: ["
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.record("d", "Another"); err == nil {
		t.Fatal("overwrote invalid file")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != invalid {
		t.Fatalf("modified invalid file: %q, %v", got, err)
	}
}

func TestDeviceFriendlyNameMQTTTopics(t *testing.T) {
	for _, tc := range []struct {
		name, savedName, lookupName, wantTopic string
	}{
		{"saved friendly name", "My Kitchen", "C-Gate Kitchen", "cbus/HOME/my-kitchen"},
		{"saved topic path", "bedroom/tv", "", "cbus/HOME/bedroom/tv"},
		{"discovered topic path", "", "Bedroom/TV", "cbus/HOME/bedroom/tv"},
		{"discovered friendly name", "", "Living Room", "cbus/HOME/living-room"},
		{"address fallback", "", "", "cbus/HOME/1/56/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devices.yaml")
			if err := os.WriteFile(path, []byte("- address: //HOME/1/56/1\n  friendly_name: "+tc.savedName+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := loadDevices(path)
			if err != nil {
				t.Fatal(err)
			}
			mapper := eventMapper{baseTopic: "cbus", names: store.names, refresh: store.reload, record: store.record, lookup: func(string) (string, error) {
				if tc.savedName != "" {
					t.Fatal("looked up device with saved friendly name")
				}
				return tc.lookupName, nil
			}}
			for _, state := range []string{"on", "off"} {
				topic, payload, err := mapper.message("lighting " + state + " //HOME/1/56/1")
				if err != nil || topic != tc.wantTopic || payload != `{"command":"`+state+`"}` {
					t.Fatalf("got %q %q, %v; want topic %q", topic, payload, err, tc.wantTopic)
				}
			}
		})
	}
}
