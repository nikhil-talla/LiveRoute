package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzDoesNotCallDependencies(t *testing.T) {
	called := false
	check := func(context.Context) error {
		called = true
		return nil
	}
	handler, err := NewHealthHandler(ReadinessChecks{
		MigrationsCurrent: check, PostgreSQLPing: check, PlannerStreamReady: check,
		OSRMCarServing: check, OSRMFootServing: check,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.Healthz(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || called {
		t.Fatalf("healthz status=%d dependency_called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
}

func TestReadyzRequiresEveryDependency(t *testing.T) {
	checks := []ReadinessCheck{
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
	}
	handler, err := NewHealthHandler(ReadinessChecks{
		MigrationsCurrent: checks[0], PostgreSQLPing: checks[1], PlannerStreamReady: checks[2],
		OSRMCarServing: checks[3], OSRMFootServing: checks[4],
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.Readyz(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready dependencies unexpectedly failed: %d", recorder.Code)
	}

	failed, err := NewHealthHandler(ReadinessChecks{
		MigrationsCurrent: checks[0], PostgreSQLPing: checks[1], PlannerStreamReady: checks[2],
		OSRMCarServing: checks[3], OSRMFootServing: func(context.Context) error { return errors.New("private failure") },
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	failed.Readyz(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != `{"status":"not_ready"}
` {
		t.Fatalf("unexpected unavailable response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestReadinessConfigurationRequiresAllChecks(t *testing.T) {
	if _, err := NewHealthHandler(ReadinessChecks{}); err == nil {
		t.Fatal("incomplete readiness checks were accepted")
	}
}
