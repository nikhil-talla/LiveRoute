package place

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestTimeZoneResolverFiltersUSAndUsesLexicographicBoundaryTieBreak(t *testing.T) {
	directory := t.TempDir()
	zoneTable := filepath.Join(directory, "zone1970.tab")
	geoJSON := filepath.Join(directory, "boundaries.json")
	if err := os.WriteFile(zoneTable, []byte("US\t+0000+00000\tAmerica/New_York\nUS\t+0000+00000\tAmerica/Chicago\nCA\t+0000+00000\tAmerica/Toronto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"type":"FeatureCollection","features":[` +
		`{"type":"Feature","properties":{"tzid":"America/New_York"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}}` +
		`,{"type":"Feature","properties":{"tzid":"America/Chicago"},"geometry":{"type":"MultiPolygon","coordinates":[[[[2,0],[4,0],[4,2],[2,2],[2,0]]]]}}` +
		`,{"type":"Feature","properties":{"tzid":"America/Toronto"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[4,0],[4,2],[0,2],[0,0]]]}}]}`)
	if err := os.WriteFile(geoJSON, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	resolver, err := LoadTimeZoneResolver(geoJSON, hex.EncodeToString(digest[:]), zoneTable)
	if err != nil {
		t.Fatal(err)
	}
	if zone, ok := resolver.Resolve(1, 1); !ok || zone != "America/New_York" {
		t.Fatalf("first zone=(%q,%t)", zone, ok)
	}
	if zone, ok := resolver.Resolve(1, 3); !ok || zone != "America/Chicago" {
		t.Fatalf("second zone=(%q,%t)", zone, ok)
	}
	// Both US polygons include their shared boundary. Sorting makes the choice
	// independent of GeoJSON feature order.
	if zone, ok := resolver.Resolve(1, 2); !ok || zone != "America/Chicago" {
		t.Fatalf("tie-break zone=(%q,%t)", zone, ok)
	}
	if zone, ok := resolver.Resolve(3, 1); ok || zone != "" {
		t.Fatalf("outside zone=(%q,%t)", zone, ok)
	}
}

func TestTimeZoneResolverRejectsDigestMismatch(t *testing.T) {
	directory := t.TempDir()
	zoneTable := filepath.Join(directory, "zone1970.tab")
	geoJSON := filepath.Join(directory, "boundaries.json")
	if err := os.WriteFile(zoneTable, []byte("US\t+0000+00000\tAmerica/New_York\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(geoJSON, []byte(`{"type":"FeatureCollection","features":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTimeZoneResolver(geoJSON, string(make([]byte, 64)), zoneTable); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestLockedTimeZoneResolverFindsProvidence(t *testing.T) {
	geoJSON := os.Getenv("LIVEROUTE_TEST_TIMEZONE_BOUNDARIES_PATH")
	zoneTable := os.Getenv("LIVEROUTE_TEST_TZDATA_ZONE_TABLE_PATH")
	if geoJSON == "" || zoneTable == "" {
		t.Skip("locked timezone assets are not mounted")
	}
	resolver, err := LoadTimeZoneResolver(geoJSON, "17f0821bad87d7a44dcebff12ad70b82bf973c54942e29fe20f5f9f8be4b3db6", zoneTable)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		latitude  float64
		longitude float64
		zone      string
	}{
		"Providence": {41.824, -71.4128, "America/New_York"},
		"Chicago":    {41.8781, -87.6298, "America/Chicago"},
		"Phoenix":    {33.4484, -112.0740, "America/Phoenix"},
		"Honolulu":   {21.3099, -157.8581, "Pacific/Honolulu"},
		"Anchorage":  {61.2181, -149.9003, "America/Anchorage"},
		"Adak":       {51.88, -176.6581, "America/Adak"},
	} {
		t.Run(name, func(t *testing.T) {
			if zone, ok := resolver.Resolve(test.latitude, test.longitude); !ok || zone != test.zone {
				t.Fatalf("zone=(%q,%t), want %q", zone, ok, test.zone)
			}
		})
	}
}
