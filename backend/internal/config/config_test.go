package config

import (
	"path/filepath"
	"testing"
)

func TestLoadLocalV1Configuration(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "local-v1.yaml")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ProtocolVersion != "liveroute.v1" || loaded.Runtime.MaxActiveTrips != 128 ||
		loaded.Limits.WebSocketFrameBytes != 262144 || loaded.Planner.BeamWidth != 32 {
		t.Fatalf("unexpected configuration: %+v", loaded)
	}
}

func TestConfigRejectsUnknownAndInconsistentValues(t *testing.T) {
	config := Config{ProtocolVersion: "liveroute.v2"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected protocol version rejection")
	}
	config = Config{ProtocolVersion: "liveroute.v1"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing positive configuration rejection")
	}
}
