package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

type fakeTripEditor struct {
	reorder persistence.ReorderActivitiesRequest
}

func (fake *fakeTripEditor) ReorderActivities(_ context.Context, request persistence.ReorderActivitiesRequest) (persistence.RecordedCommand, error) {
	fake.reorder = request
	revision := request.ExpectedTripRevision + 1
	return persistence.RecordedCommand{TripID: request.TripID, MessageID: request.MessageID, MutationSequence: 9, ResultingTripRevision: &revision}, nil
}

func (fake *fakeTripEditor) RemoveActivity(_ context.Context, request persistence.RemoveActivityRequest) (persistence.RecordedCommand, error) {
	revision := request.ExpectedTripRevision + 1
	return persistence.RecordedCommand{TripID: request.TripID, MessageID: request.MessageID, MutationSequence: 9, ResultingTripRevision: &revision}, nil
}

func (fake *fakeTripEditor) ReplaceActivity(_ context.Context, request persistence.ReplaceActivityRequest) (persistence.RecordedCommand, error) {
	revision := request.ExpectedTripRevision + 1
	return persistence.RecordedCommand{TripID: request.TripID, MessageID: request.MessageID, MutationSequence: 9, ResultingTripRevision: &revision}, nil
}

func (fake *fakeTripEditor) AddActivity(_ context.Context, request persistence.AddActivityRequest) (persistence.RecordedCommand, error) {
	revision := request.ExpectedTripRevision + 1
	return persistence.RecordedCommand{TripID: request.TripID, MessageID: request.MessageID, MutationSequence: 9, ResultingTripRevision: &revision}, nil
}

func TestTripEditedAdapterBuildsCompleteReorderMirrorEvent(t *testing.T) {
	fake := &fakeTripEditor{}
	adapter, err := NewTripEditedAdapter(fake, 2)
	if err != nil {
		t.Fatal(err)
	}
	message := AuthenticatedMessage{
		UserID: "11111111-1111-1111-1111-111111111111",
		Raw:    []byte(`{"protocol_version":"liveroute.v1","message_id":"22222222-2222-2222-2222-222222222222","kind":"trip_command","trip_id":"33333333-3333-3333-3333-333333333333","payload":{"command_kind":"trip_edited","command":{"expected_trip_revision":"4","operation":{"reorder":{"activity_ids":["55555555-5555-5555-5555-555555555555","66666666-6666-6666-6666-666666666666"]}},"current_plan":{"plan_id":"44444444-4444-4444-4444-444444444444","segments":[{"activity_id":"55555555-5555-5555-5555-555555555555","state":"omitted"},{"activity_id":"66666666-6666-6666-6666-666666666666","state":"omitted"}]}}}}`),
	}
	if _, err := adapter.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(fake.reorder.ActivityIDs) != 2 || fake.reorder.ExpectedTripRevision != 4 || fake.reorder.EventPayloadBuilder == nil {
		t.Fatalf("unexpected reorder request: %+v", fake.reorder)
	}
	payload, err := fake.reorder.EventPayloadBuilder(persistence.CanonicalPlanMetadata{
		ID: "44444444-4444-4444-4444-444444444444", Revision: 5,
		CreatedAt: time.UnixMilli(1_784_000_123_456).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := plannerwire.DecodeStoredEvent(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.CommandExpiresAtUnixMs != nil ||
		event.GetTripEdited().GetReorder().GetActivityIds()[0] != "55555555-5555-5555-5555-555555555555" ||
		event.GetTripEdited().GetResultingCurrentPlan().GetPlanRevision() != 5 {
		t.Fatalf("unexpected trip-edited event: %v", event)
	}
}
