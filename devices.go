package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type device struct {
	Address      string `yaml:"address"`
	FriendlyName string `yaml:"friendly_name"`
}

// The event worker exclusively owns the store after startup.
type deviceStore struct {
	path    string
	devices []device
	names   map[string]string
}

func loadDevices(path string) (*deviceStore, error) {
	s := &deviceStore{path: path, devices: []device{}, names: map[string]string{}}
	if err := s.reload(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s, s.save()
	}
	return s, nil
}

func (s *deviceStore) reload() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var devices []device
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&devices); err != nil && err != io.EOF {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	seen := make(map[string]bool)
	for _, d := range devices {
		if d.Address == "" || seen[d.Address] {
			return fmt.Errorf("%s: empty or duplicate address %q", s.path, d.Address)
		}
		seen[d.Address] = true
	}
	s.devices = devices
	if s.names == nil {
		s.names = make(map[string]string)
	}
	clear(s.names)
	for _, d := range devices {
		s.names[d.Address] = d.FriendlyName
	}
	return nil
}

func (s *deviceStore) record(address, name string) error {
	if err := s.reload(); err != nil {
		return err
	}
	for i, d := range s.devices {
		if d.Address == address {
			if d.FriendlyName != "" || name == "" {
				return nil
			}
			s.devices[i].FriendlyName = name
			return s.save()
		}
	}
	s.devices = append(s.devices, device{Address: address, FriendlyName: name})
	return s.save()
}

func (s *deviceStore) save() error {
	data, err := yaml.Marshal(s.devices)
	if err != nil {
		return err
	}
	// Replace atomically so an interrupted write does not truncate the registry.
	f, err := os.CreateTemp(filepath.Dir(s.path), ".devices-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	return s.reload()
}
