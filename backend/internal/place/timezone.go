package place

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

type point struct {
	lon float64
	lat float64
}

type ring []point

type polygon struct {
	rings  []ring
	minLon float64
	maxLon float64
	minLat float64
	maxLat float64
}

type zoneGeometry struct {
	name     string
	polygons []polygon
}

// TimeZoneResolver owns an immutable, in-memory index of only the US features
// from the locked timezone-boundary artifact. It is safe for concurrent use.
type TimeZoneResolver struct {
	zones []zoneGeometry
}

type boundaryFeature struct {
	Type       string `json:"type"`
	Properties struct {
		TZID string `json:"tzid"`
	} `json:"properties"`
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

// LoadTimeZoneResolver verifies the immutable GeoJSON before parsing it and
// admits only zones assigned to the US by the matching tzdata zone table.
func LoadTimeZoneResolver(geoJSONPath, expectedSHA256, zoneTablePath string) (*TimeZoneResolver, error) {
	if geoJSONPath == "" || len(expectedSHA256) != sha256.Size*2 || zoneTablePath == "" {
		return nil, errors.New("timezone boundary paths and SHA-256 are required")
	}
	allowed, err := loadUSZones(zoneTablePath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(geoJSONPath)
	if err != nil {
		return nil, fmt.Errorf("open timezone boundaries: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(file, hash))
	zones, err := decodeBoundaries(decoder, allowed)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA256) {
		return nil, errors.New("timezone boundary SHA-256 differs from lock")
	}
	if len(zones) == 0 {
		return nil, errors.New("timezone boundary artifact contains no US polygons")
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].name < zones[j].name })
	return &TimeZoneResolver{zones: zones}, nil
}

func loadUSZones(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open tzdata zone table: %w", err)
	}
	defer file.Close()
	result := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			return nil, errors.New("tzdata zone table contains an invalid row")
		}
		for _, country := range strings.Split(fields[0], ",") {
			if country == "US" {
				result[fields[2]] = struct{}{}
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read tzdata zone table: %w", err)
	}
	if len(result) == 0 {
		return nil, errors.New("tzdata zone table contains no US zones")
	}
	return result, nil
}

func decodeBoundaries(decoder *json.Decoder, allowed map[string]struct{}) ([]zoneGeometry, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("timezone boundary artifact is not a GeoJSON object")
	}
	var result []zoneGeometry
	foundFeatures := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read timezone boundary key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("timezone boundary object has a non-string key")
		}
		if key != "features" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return nil, fmt.Errorf("skip timezone boundary field: %w", err)
			}
			continue
		}
		if foundFeatures {
			return nil, errors.New("timezone boundary artifact repeats features")
		}
		foundFeatures = true
		start, err := decoder.Token()
		if err != nil || start != json.Delim('[') {
			return nil, errors.New("timezone boundary features is not an array")
		}
		for decoder.More() {
			var feature boundaryFeature
			if err := decoder.Decode(&feature); err != nil {
				return nil, fmt.Errorf("decode timezone boundary feature: %w", err)
			}
			if _, ok := allowed[feature.Properties.TZID]; !ok {
				continue
			}
			polygons, err := decodeGeometry(feature.Geometry.Type, feature.Geometry.Coordinates)
			if err != nil {
				return nil, fmt.Errorf("decode timezone %s: %w", feature.Properties.TZID, err)
			}
			result = append(result, zoneGeometry{name: feature.Properties.TZID, polygons: polygons})
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, errors.New("timezone boundary features is not terminated")
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || !foundFeatures {
		return nil, errors.New("timezone boundary artifact is incomplete")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("timezone boundary artifact has trailing data")
	}
	return result, nil
}

func decodeGeometry(kind string, raw json.RawMessage) ([]polygon, error) {
	switch kind {
	case "Polygon":
		var coordinates [][][]float64
		if err := strictUnmarshal(raw, &coordinates); err != nil {
			return nil, err
		}
		item, err := makePolygon(coordinates)
		if err != nil {
			return nil, err
		}
		return []polygon{item}, nil
	case "MultiPolygon":
		var coordinates [][][][]float64
		if err := strictUnmarshal(raw, &coordinates); err != nil {
			return nil, err
		}
		result := make([]polygon, 0, len(coordinates))
		for _, coordinate := range coordinates {
			item, err := makePolygon(coordinate)
			if err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported geometry %q", kind)
	}
}

func strictUnmarshal(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("geometry has trailing data")
	}
	return nil
}

func makePolygon(coordinates [][][]float64) (polygon, error) {
	if len(coordinates) == 0 {
		return polygon{}, errors.New("polygon has no rings")
	}
	result := polygon{minLon: math.Inf(1), maxLon: math.Inf(-1), minLat: math.Inf(1), maxLat: math.Inf(-1)}
	for _, coordinateRing := range coordinates {
		if len(coordinateRing) < 4 {
			return polygon{}, errors.New("polygon ring has fewer than four points")
		}
		converted := make(ring, 0, len(coordinateRing))
		for _, coordinate := range coordinateRing {
			if len(coordinate) < 2 || !finiteCoordinate(coordinate[1], coordinate[0]) {
				return polygon{}, errors.New("polygon contains an invalid coordinate")
			}
			item := point{lon: coordinate[0], lat: coordinate[1]}
			converted = append(converted, item)
			result.minLon = math.Min(result.minLon, item.lon)
			result.maxLon = math.Max(result.maxLon, item.lon)
			result.minLat = math.Min(result.minLat, item.lat)
			result.maxLat = math.Max(result.maxLat, item.lat)
		}
		result.rings = append(result.rings, converted)
	}
	return result, nil
}

// Resolve returns the lexicographically lowest matching US IANA timezone.
func (resolver *TimeZoneResolver) Resolve(latitude, longitude float64) (string, bool) {
	if resolver == nil || !finiteCoordinate(latitude, longitude) {
		return "", false
	}
	query := point{lon: longitude, lat: latitude}
	for _, zone := range resolver.zones {
		for _, polygon := range zone.polygons {
			if polygon.contains(query) {
				return zone.name, true
			}
		}
	}
	return "", false
}

func (polygon polygon) contains(query point) bool {
	if query.lon < polygon.minLon || query.lon > polygon.maxLon || query.lat < polygon.minLat || query.lat > polygon.maxLat {
		return false
	}
	inside, boundary := ringContains(polygon.rings[0], query)
	if boundary {
		return true
	}
	if !inside {
		return false
	}
	for _, hole := range polygon.rings[1:] {
		inHole, onHole := ringContains(hole, query)
		if onHole {
			return true
		}
		if inHole {
			return false
		}
	}
	return true
}

func ringContains(value ring, query point) (bool, bool) {
	inside := false
	for index, current := range value {
		previous := value[(index+len(value)-1)%len(value)]
		if onSegment(previous, current, query) {
			return false, true
		}
		if (current.lat > query.lat) != (previous.lat > query.lat) {
			crossing := (previous.lon-current.lon)*(query.lat-current.lat)/(previous.lat-current.lat) + current.lon
			if query.lon < crossing {
				inside = !inside
			}
		}
	}
	return inside, false
}

func onSegment(a, b, query point) bool {
	cross := (query.lat-a.lat)*(b.lon-a.lon) - (query.lon-a.lon)*(b.lat-a.lat)
	scale := math.Max(1, math.Abs(b.lon-a.lon)+math.Abs(b.lat-a.lat))
	if math.Abs(cross) > 1e-10*scale {
		return false
	}
	return query.lon >= math.Min(a.lon, b.lon)-1e-10 && query.lon <= math.Max(a.lon, b.lon)+1e-10 &&
		query.lat >= math.Min(a.lat, b.lat)-1e-10 && query.lat <= math.Max(a.lat, b.lat)+1e-10
}

func finiteCoordinate(latitude, longitude float64) bool {
	return !math.IsNaN(latitude) && !math.IsInf(latitude, 0) && latitude >= -90 && latitude <= 90 &&
		!math.IsNaN(longitude) && !math.IsInf(longitude, 0) && longitude >= -180 && longitude <= 180
}
