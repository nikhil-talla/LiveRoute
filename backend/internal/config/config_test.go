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
		loaded.Limits.WebSocketFrameBytes != 262144 || loaded.Planner.BeamWidth != 32 ||
		!loaded.RouteCache.Enabled || loaded.RouteCache.PolicyVersion != "liveroute-route-cache-v1" {
		t.Fatalf("unexpected configuration: %+v", loaded)
	}
}

func TestConfigRejectsRouteCachePolicyChanges(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "local-v1.yaml")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.RouteCache.MaxEntries++
	if err := loaded.Validate(); err == nil {
		t.Fatal("expected route-cache policy rejection")
	}
}

func TestOptionalHoursImporterConfiguration(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "local-v1.yaml")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Hours = Hours{}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("manual-hours serving must not require importer configuration: %v", err)
	}
	loaded.Hours.SeedFilePath = "seed.json"
	if err := loaded.Validate(); err == nil {
		t.Fatal("expected partial optional-hours configuration rejection")
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
