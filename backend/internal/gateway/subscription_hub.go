package gateway

import (
	"errors"
	"sync"
)

var ErrSubscriptionCapacity = errors.New("subscription capacity exhausted")

// SubscriptionHub owns only the trip-to-session fanout index. It does not
// authenticate users, load PostgreSQL state, or interpret server envelopes.
// Each sink remains responsible for its own bounded outbound queue.
type SubscriptionHub struct {
	mu               sync.Mutex
	maxSubscriptions int
	byTrip           map[string]map[MessageSink]struct{}
	bySink           map[MessageSink]map[string]struct{}
}

func NewSubscriptionHub(maxSubscriptions int) (*SubscriptionHub, error) {
	if maxSubscriptions <= 0 {
		return nil, errors.New("subscription capacity must be positive")
	}
	return &SubscriptionHub{
		maxSubscriptions: maxSubscriptions,
		byTrip:           make(map[string]map[MessageSink]struct{}),
		bySink:           make(map[MessageSink]map[string]struct{}),
	}, nil
}

func (hub *SubscriptionHub) Subscribe(tripID string, sink MessageSink) error {
	if hub == nil || !canonicalUUID(tripID) || sink == nil {
		return errors.New("subscription identity is invalid")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	trip := hub.byTrip[tripID]
	if trip == nil {
		trip = make(map[MessageSink]struct{})
		hub.byTrip[tripID] = trip
	}
	if _, exists := trip[sink]; exists {
		return nil
	}
	if len(hub.bySink[sink]) >= hub.maxSubscriptions {
		return ErrSubscriptionCapacity
	}
	trip[sink] = struct{}{}
	if hub.bySink[sink] == nil {
		hub.bySink[sink] = make(map[string]struct{})
	}
	hub.bySink[sink][tripID] = struct{}{}
	return nil
}

func (hub *SubscriptionHub) Unsubscribe(tripID string, sink MessageSink) {
	if hub == nil || !canonicalUUID(tripID) || sink == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.removeLocked(tripID, sink)
}

func (hub *SubscriptionHub) RemoveSink(sink MessageSink) {
	if hub == nil || sink == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for tripID := range hub.bySink[sink] {
		hub.removeLocked(tripID, sink)
	}
}

func (hub *SubscriptionHub) PublishTrip(tripID string, payload []byte) error {
	if hub == nil || !canonicalUUID(tripID) || len(payload) == 0 {
		return errors.New("subscription publication is invalid")
	}
	hub.mu.Lock()
	sinks := make([]MessageSink, 0, len(hub.byTrip[tripID]))
	for sink := range hub.byTrip[tripID] {
		sinks = append(sinks, sink)
	}
	hub.mu.Unlock()
	var firstErr error
	for _, sink := range sinks {
		if err := sink.PublishServerEnvelope(payload); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// A closed/overloaded sink cannot recover this publication. Remove
			// it so later broadcasts do not retain a disconnected session.
			hub.Unsubscribe(tripID, sink)
		}
	}
	return firstErr
}

func (hub *SubscriptionHub) SubscriptionCount(tripID string) int {
	if hub == nil || !canonicalUUID(tripID) {
		return 0
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.byTrip[tripID])
}

func (hub *SubscriptionHub) removeLocked(tripID string, sink MessageSink) {
	trip := hub.byTrip[tripID]
	if trip == nil {
		return
	}
	delete(trip, sink)
	if len(trip) == 0 {
		delete(hub.byTrip, tripID)
	}
	trips := hub.bySink[sink]
	delete(trips, tripID)
	if len(trips) == 0 {
		delete(hub.bySink, sink)
	}
}
