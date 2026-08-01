package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOSRMTableServingCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/table/v1/driving/"+osrmReadinessCoordinates ||
			request.URL.Query().Get("annotations") != "duration,distance" {
			t.Fatalf("unexpected readiness request: %s", request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":"Ok","durations":[[0,1.2],[1.1,0]],"distances":[[0,12],[11,0]]}`))
	}))
	defer server.Close()

	check := OSRMTableServingCheck(server.Client(), server.URL, "driving")
	if err := check(context.Background()); err != nil {
		t.Fatalf("readiness check failed: %v", err)
	}
}

func TestOSRMTableServingCheckRejectsIncompatibleResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"code":"Ok","durations":[[0,null],[1,0]],"distances":[[0,12],[1,0]]}`))
	}))
	defer server.Close()

	check := OSRMTableServingCheck(server.Client(), server.URL, "walking")
	if err := check(context.Background()); err == nil {
		t.Fatal("mismatched null cells passed readiness")
	}
}

func TestOSRMTableServingCheckRejectsInvalidConfiguration(t *testing.T) {
	for _, check := range []ReadinessCheck{
		OSRMTableServingCheck(nil, "http://osrm-car:5000", "driving"),
		OSRMTableServingCheck(http.DefaultClient, "http://osrm-car:5000?bad=1", "driving"),
		OSRMTableServingCheck(http.DefaultClient, "http://osrm-car:5000", "cycling"),
	} {
		if err := check(context.Background()); err == nil {
			t.Fatal("invalid OSRM readiness configuration was accepted")
		}
	}
}
