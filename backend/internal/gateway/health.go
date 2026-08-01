package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const osrmReadinessCoordinates = "-71.4128,41.8240;-71.4150,41.8300"

type ReadinessCheck func(context.Context) error

// GRPCServingCheck adapts the standard gRPC health service used by the C++
// planner. The service name is kept explicit so the base planner and its car
// and foot profile services cannot be conflated.
func GRPCServingCheck(client healthpb.HealthClient, service string) ReadinessCheck {
	return func(ctx context.Context) error {
		if client == nil || service == "" {
			return errors.New("gRPC health dependency is not configured")
		}
		response, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		if err != nil {
			return errors.New("gRPC health check failed")
		}
		if response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return errors.New("gRPC dependency is not serving")
		}
		return nil
	}
}

// OSRMTableServingCheck performs the contract's fixed two-location Table
// request. It verifies the application response rather than treating an open
// TCP port as readiness.
func OSRMTableServingCheck(client *http.Client, endpoint, profile string) ReadinessCheck {
	return func(ctx context.Context) error {
		if client == nil || endpoint == "" || (profile != "driving" && profile != "walking") {
			return errors.New("OSRM readiness dependency is not configured")
		}
		base, err := url.Parse(endpoint)
		if err != nil || base.Scheme != "http" || base.Host == "" ||
			base.RawQuery != "" || base.Fragment != "" {
			return errors.New("OSRM readiness endpoint is invalid")
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/table/v1/" + profile + "/" + osrmReadinessCoordinates
		query := base.Query()
		query.Set("annotations", "duration,distance")
		base.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if err != nil {
			return errors.New("OSRM readiness request is invalid")
		}
		response, err := client.Do(request)
		if err != nil {
			return errors.New("OSRM readiness request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("OSRM readiness returned HTTP %d", response.StatusCode)
		}
		limited := io.LimitReader(response.Body, 64*1024+1)
		body, err := io.ReadAll(limited)
		if err != nil || len(body) > 64*1024 {
			return errors.New("OSRM readiness response is invalid")
		}
		var result struct {
			Code      string       `json:"code"`
			Durations [][]*float64 `json:"durations"`
			Distances [][]*float64 `json:"distances"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.Code != "Ok" ||
			!isTwoByTwo(result.Durations) || !isTwoByTwo(result.Distances) {
			return errors.New("OSRM readiness response is incompatible")
		}
		for row := range result.Durations {
			for column := range result.Durations[row] {
				if (result.Durations[row][column] == nil) != (result.Distances[row][column] == nil) {
					return errors.New("OSRM readiness response has mismatched cells")
				}
			}
		}
		return nil
	}
}

func isTwoByTwo(matrix [][]*float64) bool {
	return len(matrix) == 2 && len(matrix[0]) == 2 && len(matrix[1]) == 2
}

type ReadinessChecks struct {
	MigrationsCurrent  ReadinessCheck
	PostgreSQLPing     ReadinessCheck
	PlannerStreamReady ReadinessCheck
	OSRMCarServing     ReadinessCheck
	OSRMFootServing    ReadinessCheck
}

func (checks ReadinessChecks) validate() error {
	if checks.MigrationsCurrent == nil || checks.PostgreSQLPing == nil ||
		checks.PlannerStreamReady == nil || checks.OSRMCarServing == nil ||
		checks.OSRMFootServing == nil {
		return errors.New("all readiness checks are required")
	}
	return nil
}

type HealthHandler struct {
	checks ReadinessChecks
}

func NewHealthHandler(checks ReadinessChecks) (*HealthHandler, error) {
	if err := checks.validate(); err != nil {
		return nil, err
	}
	return &HealthHandler{checks: checks}, nil
}

// Healthz is deliberately dependency-free: a successful response means only
// that the HTTP event loop can serve requests.
func (h *HealthHandler) Healthz(writer http.ResponseWriter, _ *http.Request) {
	writeHealth(writer, http.StatusOK, "ok")
}

func (h *HealthHandler) Readyz(writer http.ResponseWriter, request *http.Request) {
	checks := []ReadinessCheck{
		h.checks.MigrationsCurrent,
		h.checks.PostgreSQLPing,
		h.checks.PlannerStreamReady,
		h.checks.OSRMCarServing,
		h.checks.OSRMFootServing,
	}
	for _, check := range checks {
		if err := check(request.Context()); err != nil {
			writeHealth(writer, http.StatusServiceUnavailable, "not_ready")
			return
		}
	}
	writeHealth(writer, http.StatusOK, "ready")
}

func writeHealth(writer http.ResponseWriter, status int, value string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": value})
}
