package dispatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"google.golang.org/protobuf/proto"
)

type fakeRecoverySnapshots struct {
	snapshot persistence.StoredSnapshot
	err      error
}

func (store fakeRecoverySnapshots) LoadForRecovery(
	context.Context,
	string,
) (persistence.StoredSnapshot, error) {
	return store.snapshot, store.err
}

type fakeBootstrapState struct {
	state   persistence.CanonicalTripState
	resolve *persistence.ResolveCanonicalBootstrapRequest
}

func (store *fakeBootstrapState) Load(context.Context, string) (persistence.CanonicalTripState, error) {
	return store.state, nil
}
func (store *fakeBootstrapState) ResolveCanonicalBootstrap(_ context.Context, request persistence.ResolveCanonicalBootstrapRequest) (persistence.ResolvedCanonicalBootstrap, error) {
	store.resolve = &request
	return persistence.ResolvedCanonicalBootstrap{
		TripID: request.TripID, TripRevision: request.TripRevision,
		AcceptedMutationSequence:  request.AcceptedMutationSequence,
		FinalizedMutationSequence: request.FinalizedMutationSequence,
		CurrentPlanID:             request.CurrentPlanID,
	}, nil
}

type fakeBootstrapPlanner struct {
	request *liveroutev1.PlannerStreamRequest
}

func (planner *fakeBootstrapPlanner) Exchange(_ context.Context, request *liveroutev1.PlannerStreamRequest) (*liveroutev1.PlannerStreamResponse, error) {
	planner.request = request
	bootstrap := request.GetBootstrapTrip()
	currentPlanID := bootstrap.GetCurrentPlan().GetPlanId()
	if snapshot := bootstrap.GetSnapshot(); snapshot != nil {
		state := &liveroutev1.TripStateSnapshot{}
		if err := proto.Unmarshal(snapshot.GetPayload(), state); err != nil {
			return nil, err
		}
		currentPlanID = state.GetCurrentPlan().GetPlanId()
	}
	return &liveroutev1.PlannerStreamResponse{
		RequestId: request.GetRequestId(), TripId: request.GetTripId(),
		RuntimeEpoch: request.GetRuntimeEpoch(), TripRevision: bootstrap.GetTripRevision(),
		Payload: &liveroutev1.PlannerStreamResponse_TripBootstrapped{
			TripBootstrapped: &liveroutev1.TripBootstrapped{
				Status:                    liveroutev1.StatusCode_STATUS_CODE_OK,
				CurrentPlanId:             currentPlanID,
				AcceptedMutationSequence:  bootstrap.GetFinalizedMutationSequence(),
				FinalizedMutationSequence: bootstrap.GetFinalizedMutationSequence(),
			},
		},
	}, nil
}

func TestBootstrapperLoadsCanonicalFullStateAndConvergesMirrors(t *testing.T) {
	plan := &liveroutev1.CurrentPlan{
		PlanId: testPlanID, PlanRevision: 3,
		Origin:          liveroutev1.PlanOrigin_PLAN_ORIGIN_USER_AUTHORED,
		CreatedAtUnixMs: 1_800_000_000_000,
		Segments: []*liveroutev1.CurrentPlanSegment{{
			ActivityId:           testIntentID,
			State:                liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_SCHEDULED,
			ScheduledStartUnixMs: pointer(int64(1_800_000_100_000)),
			ScheduledEndUnixMs:   pointer(int64(1_800_000_160_000)),
		}},
	}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	state := &fakeBootstrapState{state: persistence.CanonicalTripState{
		TripID: testTripID, OwnerUserID: testOutboxID,
		DefaultTimeZoneName: "America/New_York", TripRevision: 3,
		FinalizedMutationSequence: 3, CurrentPlanID: testPlanID,
		Activities: []persistence.CanonicalActivity{{
			ID: testIntentID, PlaceID: "place", DisplayName: "Activity",
			Latitude: 40, Longitude: -74, TimeZoneName: "America/New_York",
			InboundTravelMode: "walking", ActivityClass: "flexible",
			ActivityState: persistence.ActivityStatePlanned,
			PriorityRank:  1, UtilityScore: 2,
			MinDurationSeconds: 60, PreferredDurationSeconds: 60,
			MaxDurationSeconds: 60, CanMove: true, CanSkip: true,
			OpenWindows: []persistence.CanonicalOpenWindow{{
				OpensAt:  time.UnixMilli(1_800_000_000_000),
				ClosesAt: time.UnixMilli(1_800_001_000_000),
			}},
		}},
		CurrentPlan: persistence.CanonicalCurrentPlan{
			ID: testPlanID, Revision: 3, Payload: wire,
		},
	}}
	planner := &fakeBootstrapPlanner{}
	bootstrapper, err := NewBootstrapper(
		"77777777-7777-4777-8777-777777777777", time.Second,
		state, fakeRecoverySnapshots{err: persistence.ErrSnapshotNotFound},
		fakeLeases{}, planner,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bootstrapper.Bootstrap(context.Background(), testTripID)
	if err != nil {
		t.Fatal(err)
	}
	request := planner.request.GetBootstrapTrip()
	if result.CurrentPlanID != testPlanID || state.resolve == nil ||
		request.GetFullTrip().GetActivities()[0].GetActivityId() != testIntentID ||
		request.GetCurrentPlan().GetPlanId() != testPlanID ||
		state.resolve.AcceptedMutationSequence != 3 {
		t.Fatalf("bootstrap did not converge: %#v %#v", request, state.resolve)
	}
}

func TestBootstrapperPrefersCompatibleSnapshot(t *testing.T) {
	plan := &liveroutev1.CurrentPlan{
		PlanId: testPlanID, PlanRevision: 2,
		Origin: liveroutev1.PlanOrigin_PLAN_ORIGIN_USER_AUTHORED,
	}
	state := &liveroutev1.TripStateSnapshot{
		Trip: &liveroutev1.TripDefinition{
			TripId: testTripID, OwnerUserId: testOutboxID,
			DefaultTimeZoneName: "America/New_York", CurrentPlanId: testPlanID,
		},
		TripRevision: 2, AcceptedMutationSequence: 3,
		FinalizedMutationSequence: 3, CurrentPlan: plan, SnapshotSchemaVersion: 1,
	}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	canonical := &fakeBootstrapState{}
	planner := &fakeBootstrapPlanner{}
	bootstrapper, err := NewBootstrapper(
		testHolderID, time.Second, canonical,
		fakeRecoverySnapshots{snapshot: persistence.StoredSnapshot{
			SnapshotBlob: persistence.SnapshotBlob{
				ID: testProposalID, TripID: testTripID, SchemaVersion: 1,
				SourceRuntimeEpoch: 1, SourcePlannerStateVersion: 7,
				TripRevision: 2, CoveredFinalizedMutationSequence: 3,
				Payload: wire, Checksum: sha256.Sum256(wire),
			},
		}}, fakeLeases{}, planner,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bootstrapper.Bootstrap(context.Background(), testTripID)
	if err != nil {
		t.Fatal(err)
	}
	if planner.request.GetBootstrapTrip().GetSnapshot() == nil ||
		planner.request.GetBootstrapTrip().GetFullTrip() != nil ||
		result.CurrentPlanID != testPlanID || canonical.resolve != nil {
		t.Fatalf("snapshot recovery was not preferred: request=%+v result=%+v", planner.request, result)
	}
}

func TestBootstrapperDoesNotHideSnapshotStoreFailure(t *testing.T) {
	bootstrapper, err := NewBootstrapper(
		testHolderID, time.Second, &fakeBootstrapState{},
		fakeRecoverySnapshots{err: errors.New("snapshot database unavailable")},
		fakeLeases{}, &fakeBootstrapPlanner{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrapper.Bootstrap(context.Background(), testTripID); err == nil {
		t.Fatal("snapshot-store failure incorrectly fell back to canonical state")
	}
}

func pointer[T any](value T) *T { return &value }
