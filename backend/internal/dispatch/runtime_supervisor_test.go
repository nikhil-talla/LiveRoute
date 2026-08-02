package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

type supervisorLeaseFake struct {
	mu       sync.Mutex
	acquires int
	renews   int
	renewErr error
}

func (fake *supervisorLeaseFake) Acquire(context.Context, string, string, time.Duration) (persistence.RuntimeLease, error) {
	fake.mu.Lock()
	fake.acquires++
	fake.mu.Unlock()
	return persistence.RuntimeLease{RuntimeEpoch: 4}, nil
}

func (fake *supervisorLeaseFake) Renew(context.Context, string, string, uint64, time.Duration) (persistence.RuntimeLease, error) {
	fake.mu.Lock()
	fake.renews++
	err := fake.renewErr
	fake.mu.Unlock()
	if err != nil {
		return persistence.RuntimeLease{}, err
	}
	return persistence.RuntimeLease{RuntimeEpoch: 4}, nil
}

type supervisorBootstrapFake struct {
	mu      sync.Mutex
	calls   int
	err     error
	started chan struct{}
	release chan struct{}
}

func (fake *supervisorBootstrapFake) Bootstrap(context.Context, string) (persistence.ResolvedCanonicalBootstrap, error) {
	fake.mu.Lock()
	fake.calls++
	err := fake.err
	started, release := fake.started, fake.release
	fake.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return persistence.ResolvedCanonicalBootstrap{}, err
}

func TestRuntimeSupervisorBootstrapsBeforeAdmittingAndBoundsTrips(t *testing.T) {
	leases := &supervisorLeaseFake{}
	bootstrap := &supervisorBootstrapFake{}
	supervisor, err := NewRuntimeSupervisor(
		"550e8400-e29b-41d4-a716-446655440001", 50*time.Millisecond, 20*time.Millisecond,
		1, leases, bootstrap, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	tripID := "550e8400-e29b-41d4-a716-446655440002"
	if err := supervisor.Activate(context.Background(), tripID); err != nil {
		t.Fatal(err)
	}
	if supervisor.ActiveTripCount() != 1 || bootstrap.calls != 1 || leases.acquires != 1 {
		t.Fatalf("active=%d bootstrap=%d acquire=%d", supervisor.ActiveTripCount(), bootstrap.calls, leases.acquires)
	}
	if err := supervisor.Activate(context.Background(), "550e8400-e29b-41d4-a716-446655440003"); !errors.Is(err, ErrRuntimeCapacity) {
		t.Fatalf("second activation error=%v", err)
	}
	if err := supervisor.Activate(context.Background(), tripID); err != nil {
		t.Fatalf("duplicate activation error=%v", err)
	}
}

func TestRuntimeSupervisorRemovesTripAfterRenewalLoss(t *testing.T) {
	leases := &supervisorLeaseFake{renewErr: errors.New("lease lost")}
	bootstrap := &supervisorBootstrapFake{}
	lost := make(chan string, 1)
	supervisor, err := NewRuntimeSupervisor(
		"550e8400-e29b-41d4-a716-446655440001", 20*time.Millisecond, 5*time.Millisecond,
		1, leases, bootstrap, func(tripID string, _ error) { lost <- tripID },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	tripID := "550e8400-e29b-41d4-a716-446655440002"
	if err := supervisor.Activate(context.Background(), tripID); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-lost:
		if got != tripID {
			t.Fatalf("lost trip=%s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("lease loss was not reported")
	}
	if supervisor.ActiveTripCount() != 0 {
		t.Fatal("trip remained active after lease loss")
	}
}

func TestRuntimeSupervisorConcurrentActivationWaitsForBootstrap(t *testing.T) {
	leases := &supervisorLeaseFake{}
	bootstrap := &supervisorBootstrapFake{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	supervisor, err := NewRuntimeSupervisor(
		"550e8400-e29b-41d4-a716-446655440001", 50*time.Millisecond, 20*time.Millisecond,
		1, leases, bootstrap, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	tripID := "550e8400-e29b-41d4-a716-446655440002"
	first := make(chan error, 1)
	go func() { first <- supervisor.Activate(context.Background(), tripID) }()
	<-bootstrap.started
	second := make(chan error, 1)
	go func() { second <- supervisor.Activate(context.Background(), tripID) }()
	select {
	case err := <-second:
		t.Fatalf("duplicate activation completed during bootstrap: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(bootstrap.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if bootstrap.calls != 1 || leases.acquires != 1 {
		t.Fatalf("bootstrap=%d acquire=%d", bootstrap.calls, leases.acquires)
	}
}
