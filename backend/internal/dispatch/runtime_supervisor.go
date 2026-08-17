package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/liveroute/liveroute/backend/internal/persistence"
)

var (
	ErrRuntimeCapacity = errors.New("active trip runtime capacity exhausted")
	ErrRuntimeClosed   = errors.New("trip runtime supervisor is closed")
	ErrRuntimeInactive = errors.New("trip runtime is not active")
)

type runtimeLeaseManager interface {
	Acquire(context.Context, string, string, time.Duration) (persistence.RuntimeLease, error)
	Renew(context.Context, string, string, uint64, time.Duration) (persistence.RuntimeLease, error)
	Current(context.Context, string, string) (persistence.RuntimeLease, error)
	Release(context.Context, string, string, uint64) error
}

type runtimeBootstrapper interface {
	Bootstrap(context.Context, string) (persistence.ResolvedCanonicalBootstrap, error)
}

type RuntimeVersions struct {
	RuntimeEpoch                uint64
	PlannerStateVersion         uint64
	AcceptedMutationSequence    uint64
	AcceptedObservationSequence uint64
}

type activationWait struct {
	done   chan struct{}
	result error
}

type RuntimeSupervisor struct {
	holderID       string
	leaseDuration  time.Duration
	renewalMargin  time.Duration
	maxActiveTrips int
	leases         runtimeLeaseManager
	bootstrapper   runtimeBootstrapper
	onLeaseLost    func(string, error)

	mu              sync.Mutex
	active          map[string]context.CancelFunc
	activating      map[string]*activationWait
	state           map[string]RuntimeVersions
	nextObservation map[string]uint64
	closed          bool
	wait            sync.WaitGroup
}

func NewRuntimeSupervisor(
	holderID string,
	leaseDuration time.Duration,
	renewalMargin time.Duration,
	maxActiveTrips int,
	leases runtimeLeaseManager,
	bootstrapper runtimeBootstrapper,
	onLeaseLost func(string, error),
) (*RuntimeSupervisor, error) {
	if !canonicalUUID(holderID) || leaseDuration <= 0 || renewalMargin <= 0 ||
		renewalMargin >= leaseDuration || maxActiveTrips <= 0 || leases == nil ||
		bootstrapper == nil {
		return nil, errors.New("invalid runtime supervisor configuration")
	}
	if onLeaseLost == nil {
		onLeaseLost = func(string, error) {}
	}
	return &RuntimeSupervisor{
		holderID: holderID, leaseDuration: leaseDuration, renewalMargin: renewalMargin,
		maxActiveTrips: maxActiveTrips, leases: leases, bootstrapper: bootstrapper,
		onLeaseLost: onLeaseLost, active: make(map[string]context.CancelFunc),
		activating:      make(map[string]*activationWait),
		state:           make(map[string]RuntimeVersions),
		nextObservation: make(map[string]uint64),
	}, nil
}

// Activate acquires the current PostgreSQL lease and completes planner
// bootstrap before the trip is admitted as active. Failed bootstrap never
// starts renewal or dispatch work; the lease naturally expires and can be
// retried by the caller.
func (supervisor *RuntimeSupervisor) Activate(
	parent context.Context,
	tripID string,
) error {
	if parent == nil || !canonicalUUID(tripID) {
		return errors.New("runtime activation input is invalid")
	}
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		return ErrRuntimeClosed
	}
	if _, exists := supervisor.active[tripID]; exists {
		if wait, activating := supervisor.activating[tripID]; activating {
			supervisor.mu.Unlock()
			select {
			case <-parent.Done():
				return parent.Err()
			case <-wait.done:
				return wait.result
			}
		}
		supervisor.mu.Unlock()
		return nil
	}
	if len(supervisor.active) >= supervisor.maxActiveTrips {
		supervisor.mu.Unlock()
		return ErrRuntimeCapacity
	}
	ctx, cancel := context.WithCancel(parent)
	supervisor.active[tripID] = cancel
	wait := &activationWait{done: make(chan struct{})}
	supervisor.activating[tripID] = wait
	supervisor.mu.Unlock()

	lease, err := supervisor.leases.Acquire(ctx, tripID, supervisor.holderID, supervisor.leaseDuration)
	var bootstrap persistence.ResolvedCanonicalBootstrap
	if err == nil {
		bootstrap, err = supervisor.bootstrapper.Bootstrap(ctx, tripID)
	}
	if err != nil {
		cancel()
		supervisor.mu.Lock()
		if current, ok := supervisor.activating[tripID]; ok && current == wait {
			delete(supervisor.activating, tripID)
			delete(supervisor.active, tripID)
			wait.result = err
			close(wait.done)
		}
		supervisor.mu.Unlock()
		return err
	}
	supervisor.mu.Lock()
	if current, ok := supervisor.activating[tripID]; !ok || current != wait {
		supervisor.mu.Unlock()
		cancel()
		return ErrRuntimeClosed
	}
	if supervisor.closed {
		delete(supervisor.activating, tripID)
		delete(supervisor.active, tripID)
		wait.result = ErrRuntimeClosed
		close(wait.done)
		supervisor.mu.Unlock()
		cancel()
		return ErrRuntimeClosed
	}
	supervisor.state[tripID] = RuntimeVersions{
		RuntimeEpoch:             lease.RuntimeEpoch,
		AcceptedMutationSequence: bootstrap.AcceptedMutationSequence,
	}
	supervisor.nextObservation[tripID] = 0
	delete(supervisor.activating, tripID)
	wait.result = nil
	close(wait.done)
	supervisor.mu.Unlock()
	supervisor.wait.Add(1)
	go supervisor.renew(ctx, tripID, lease)
	return nil
}

func (supervisor *RuntimeSupervisor) renew(
	ctx context.Context,
	tripID string,
	lease persistence.RuntimeLease,
) {
	defer supervisor.wait.Done()
	interval := supervisor.leaseDuration - supervisor.renewalMargin
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := supervisor.leases.Renew(
				ctx, tripID, supervisor.holderID, lease.RuntimeEpoch, supervisor.leaseDuration,
			)
			if err != nil {
				supervisor.deactivate(tripID)
				supervisor.onLeaseLost(tripID, err)
				return
			}
			lease = renewed
		}
	}
}

func (supervisor *RuntimeSupervisor) deactivate(tripID string) {
	supervisor.mu.Lock()
	if cancel, exists := supervisor.active[tripID]; exists {
		delete(supervisor.active, tripID)
		delete(supervisor.state, tripID)
		delete(supervisor.nextObservation, tripID)
		cancel()
	}
	supervisor.mu.Unlock()
}

func (supervisor *RuntimeSupervisor) Deactivate(ctx context.Context, tripID string) error {
	if ctx == nil || !canonicalUUID(tripID) {
		return errors.New("runtime deactivation input is invalid")
	}
	supervisor.mu.Lock()
	state, exists := supervisor.state[tripID]
	_, activating := supervisor.activating[tripID]
	supervisor.mu.Unlock()
	if exists {
		if err := supervisor.leases.Release(ctx, tripID, supervisor.holderID, state.RuntimeEpoch); err != nil && !errors.Is(err, persistence.ErrLeaseLost) {
			return err
		}
	} else if activating {
		if lease, err := supervisor.leases.Current(ctx, tripID, supervisor.holderID); err == nil {
			if err := supervisor.leases.Release(ctx, tripID, supervisor.holderID, lease.RuntimeEpoch); err != nil && !errors.Is(err, persistence.ErrLeaseLost) {
				return err
			}
		} else if !errors.Is(err, persistence.ErrLeaseLost) {
			return err
		}
	}
	supervisor.deactivate(tripID)
	return nil
}

func (supervisor *RuntimeSupervisor) ActiveTripCount() int {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return len(supervisor.active)
}

func (supervisor *RuntimeSupervisor) RuntimeState(tripID string) (RuntimeVersions, bool) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	state, exists := supervisor.state[tripID]
	return state, exists
}

func (supervisor *RuntimeSupervisor) ReserveObservation(tripID string) (RuntimeVersions, uint64, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	state, exists := supervisor.state[tripID]
	if !exists {
		return RuntimeVersions{}, 0, ErrRuntimeInactive
	}
	next := supervisor.nextObservation[tripID] + 1
	supervisor.nextObservation[tripID] = next
	return state, next, nil
}

func (supervisor *RuntimeSupervisor) CommitObservation(
	tripID string,
	runtimeEpoch uint64,
	observationSequence uint64,
	acceptedMutationSequence uint64,
	plannerStateVersion uint64,
) error {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	state, exists := supervisor.state[tripID]
	if !exists {
		return ErrRuntimeInactive
	}
	if state.RuntimeEpoch != runtimeEpoch {
		return persistence.ErrLeaseLost
	}
	if observationSequence > state.AcceptedObservationSequence {
		state.AcceptedObservationSequence = observationSequence
	}
	if acceptedMutationSequence > state.AcceptedMutationSequence {
		state.AcceptedMutationSequence = acceptedMutationSequence
	}
	if plannerStateVersion > state.PlannerStateVersion {
		state.PlannerStateVersion = plannerStateVersion
	}
	supervisor.state[tripID] = state
	return nil
}

func (supervisor *RuntimeSupervisor) CommitMutation(
	tripID string,
	runtimeEpoch uint64,
	acceptedMutationSequence uint64,
	plannerStateVersion uint64,
) error {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	state, exists := supervisor.state[tripID]
	if !exists {
		return ErrRuntimeInactive
	}
	if state.RuntimeEpoch != runtimeEpoch {
		return persistence.ErrLeaseLost
	}
	if acceptedMutationSequence > state.AcceptedMutationSequence {
		state.AcceptedMutationSequence = acceptedMutationSequence
	}
	if plannerStateVersion > state.PlannerStateVersion {
		state.PlannerStateVersion = plannerStateVersion
	}
	supervisor.state[tripID] = state
	return nil
}

func (supervisor *RuntimeSupervisor) Close() {
	supervisor.mu.Lock()
	if !supervisor.closed {
		supervisor.closed = true
		for tripID, cancel := range supervisor.active {
			delete(supervisor.active, tripID)
			delete(supervisor.state, tripID)
			delete(supervisor.nextObservation, tripID)
			cancel()
		}
		for tripID, wait := range supervisor.activating {
			delete(supervisor.activating, tripID)
			wait.result = ErrRuntimeClosed
			close(wait.done)
		}
	}
	supervisor.mu.Unlock()
	supervisor.wait.Wait()
}
