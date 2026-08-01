package dispatch

import (
	"context"
	"errors"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannertransport"
	"google.golang.org/protobuf/proto"
)

type canonicalBootstrapStore interface {
	Load(context.Context, string) (persistence.CanonicalTripState, error)
	ResolveCanonicalBootstrap(context.Context, persistence.ResolveCanonicalBootstrapRequest) (persistence.ResolvedCanonicalBootstrap, error)
}

type recoverySnapshotStore interface {
	LoadForRecovery(context.Context, string) (persistence.StoredSnapshot, error)
}

type Bootstrapper struct {
	holderID  string
	timeout   time.Duration
	state     canonicalBootstrapStore
	snapshots recoverySnapshotStore
	leases    leaseStore
	planner   plannerClient
	now       func() time.Time
}

func NewBootstrapper(
	holderID string,
	timeout time.Duration,
	state canonicalBootstrapStore,
	snapshots recoverySnapshotStore,
	leases leaseStore,
	planner plannerClient,
) (*Bootstrapper, error) {
	if holderID == "" || timeout <= 0 || state == nil || snapshots == nil ||
		leases == nil || planner == nil {
		return nil, errors.New("invalid bootstrapper configuration")
	}
	return &Bootstrapper{
		holderID: holderID, timeout: timeout, state: state, snapshots: snapshots,
		leases: leases, planner: planner, now: time.Now,
	}, nil
}

// Bootstrap loads only PostgreSQL-authoritative state. Non-durable telemetry
// is deliberately absent, and pending runtime-first work is replayed later by
// the ordered outbox dispatcher.
func (bootstrapper *Bootstrapper) Bootstrap(
	ctx context.Context,
	tripID string,
) (persistence.ResolvedCanonicalBootstrap, error) {
	lease, err := bootstrapper.leases.Current(ctx, tripID, bootstrapper.holderID)
	if err != nil {
		return persistence.ResolvedCanonicalBootstrap{}, err
	}
	payload, expected, canonical, err := bootstrapper.recoveryPayload(ctx, tripID)
	if err != nil {
		return persistence.ResolvedCanonicalBootstrap{}, err
	}
	requestID, err := plannertransport.NewRequestID()
	if err != nil {
		return persistence.ResolvedCanonicalBootstrap{}, err
	}
	request := &liveroutev1.PlannerStreamRequest{
		RequestId: requestID, TripId: tripID, RuntimeEpoch: lease.RuntimeEpoch,
		ExpiresAtUnixMs: bootstrapper.now().Add(bootstrapper.timeout).UnixMilli(),
		Payload:         &liveroutev1.PlannerStreamRequest_BootstrapTrip{BootstrapTrip: payload},
	}
	attemptContext, cancel := context.WithTimeout(ctx, bootstrapper.timeout)
	response, err := bootstrapper.planner.Exchange(attemptContext, request)
	cancel()
	if err != nil {
		return persistence.ResolvedCanonicalBootstrap{}, err
	}
	ack := response.GetTripBootstrapped()
	if response.GetRequestId() != requestID || response.GetTripId() != tripID ||
		response.GetRuntimeEpoch() != lease.RuntimeEpoch || ack == nil ||
		(ack.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_OK &&
			ack.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_DUPLICATE) ||
		ack.GetCurrentPlanId() != expected.CurrentPlanID ||
		ack.GetAcceptedMutationSequence() != expected.FinalizedMutationSequence ||
		ack.GetFinalizedMutationSequence() != expected.FinalizedMutationSequence ||
		response.GetTripRevision() != expected.TripRevision {
		return persistence.ResolvedCanonicalBootstrap{},
			errors.New("planner bootstrap acknowledgement conflicts with canonical state")
	}
	if !canonical {
		return expected, nil
	}
	return bootstrapper.state.ResolveCanonicalBootstrap(
		ctx,
		persistence.ResolveCanonicalBootstrapRequest{
			TripID: tripID, TripRevision: response.GetTripRevision(),
			AcceptedMutationSequence:  ack.GetAcceptedMutationSequence(),
			FinalizedMutationSequence: ack.GetFinalizedMutationSequence(),
			CurrentPlanID:             ack.GetCurrentPlanId(),
		},
	)
}

func (bootstrapper *Bootstrapper) recoveryPayload(
	ctx context.Context,
	tripID string,
) (*liveroutev1.BootstrapTrip, persistence.ResolvedCanonicalBootstrap, bool, error) {
	snapshot, err := bootstrapper.snapshots.LoadForRecovery(ctx, tripID)
	if err == nil {
		state := &liveroutev1.TripStateSnapshot{}
		if unmarshalErr := proto.Unmarshal(snapshot.Payload, state); unmarshalErr != nil ||
			state.GetCurrentPlan() == nil || state.GetCurrentPlan().GetPlanId() == "" {
			return nil, persistence.ResolvedCanonicalBootstrap{}, false,
				errors.New("compatible snapshot payload is invalid")
		}
		payload := &liveroutev1.BootstrapTrip{
			Base: &liveroutev1.BootstrapTrip_Snapshot{
				Snapshot: &liveroutev1.SnapshotBlob{
					SnapshotSchemaVersion:            snapshot.SchemaVersion,
					SourceRuntimeEpoch:               snapshot.SourceRuntimeEpoch,
					SourcePlannerStateVersion:        snapshot.SourcePlannerStateVersion,
					TripRevision:                     snapshot.TripRevision,
					CoveredFinalizedMutationSequence: snapshot.CoveredFinalizedMutationSequence,
					PayloadSizeBytes:                 uint32(len(snapshot.Payload)),
					ChecksumSha256:                   append([]byte(nil), snapshot.Checksum[:]...),
					Payload:                          append([]byte(nil), snapshot.Payload...),
				},
			},
			FinalizedMutationSequence: snapshot.CoveredFinalizedMutationSequence,
			TripRevision:              snapshot.TripRevision,
		}
		return payload, persistence.ResolvedCanonicalBootstrap{
			TripID: tripID, TripRevision: snapshot.TripRevision,
			AcceptedMutationSequence:  snapshot.CoveredFinalizedMutationSequence,
			FinalizedMutationSequence: snapshot.CoveredFinalizedMutationSequence,
			CurrentPlanID:             state.GetCurrentPlan().GetPlanId(),
		}, false, nil
	}
	if !errors.Is(err, persistence.ErrSnapshotNotFound) {
		return nil, persistence.ResolvedCanonicalBootstrap{}, false, err
	}
	state, err := bootstrapper.state.Load(ctx, tripID)
	if err != nil {
		return nil, persistence.ResolvedCanonicalBootstrap{}, false, err
	}
	payload, err := fullBootstrap(state)
	if err != nil {
		return nil, persistence.ResolvedCanonicalBootstrap{}, false, err
	}
	return payload, persistence.ResolvedCanonicalBootstrap{
		TripID: state.TripID, TripRevision: state.TripRevision,
		AcceptedMutationSequence:  state.FinalizedMutationSequence,
		FinalizedMutationSequence: state.FinalizedMutationSequence,
		CurrentPlanID:             state.CurrentPlanID,
	}, true, nil
}

func fullBootstrap(state persistence.CanonicalTripState) (*liveroutev1.BootstrapTrip, error) {
	plan := &liveroutev1.CurrentPlan{}
	if err := proto.Unmarshal(state.CurrentPlan.Payload, plan); err != nil ||
		plan.GetPlanId() != state.CurrentPlanID {
		return nil, errors.New("canonical current-plan payload is invalid")
	}
	trip := &liveroutev1.TripDefinition{
		TripId: state.TripID, OwnerUserId: state.OwnerUserID,
		DefaultTimeZoneName:  state.DefaultTimeZoneName,
		CompletedPrefixCount: state.CompletedPrefixCount,
		CurrentPlanId:        state.CurrentPlanID,
	}
	if state.CurrentActivityID != nil {
		trip.CurrentActivityId = *state.CurrentActivityID
	}
	for _, value := range state.Activities {
		activity := &liveroutev1.Activity{
			ActivityId: value.ID, PlaceId: value.PlaceID,
			DisplayName:       value.DisplayName,
			Location:          &liveroutev1.Location{Latitude: value.Latitude, Longitude: value.Longitude},
			TimeZoneName:      value.TimeZoneName,
			InboundTravelMode: travelMode(value.InboundTravelMode),
			ActivityClass:     activityClass(value.ActivityClass),
			ActivityState:     activityState(value.ActivityState),
			PriorityRank:      value.PriorityRank, UtilityScore: value.UtilityScore,
			ActivityDelaySeconds: value.ActivityDelaySeconds,
			Timing: &liveroutev1.ActivityTiming{
				ReservationGraceSeconds:  value.ReservationGraceSeconds,
				MinDurationSeconds:       value.MinDurationSeconds,
				PreferredDurationSeconds: value.PreferredDurationSeconds,
				MaxDurationSeconds:       value.MaxDurationSeconds,
				Mandatory:                value.Mandatory, CanShorten: value.CanShorten,
				CanMove: value.CanMove, CanSkip: value.CanSkip,
			},
		}
		if value.ReservationStart != nil {
			milliseconds := value.ReservationStart.UnixMilli()
			activity.Timing.ReservationStartUnixMs = &milliseconds
		}
		if value.MandatoryDeadline != nil {
			milliseconds := value.MandatoryDeadline.UnixMilli()
			activity.Timing.MandatoryDeadlineUnixMs = &milliseconds
		}
		if value.FoundClosedAt != nil {
			milliseconds := value.FoundClosedAt.UnixMilli()
			activity.FoundClosedAtUnixMs = &milliseconds
		}
		for _, window := range value.OpenWindows {
			activity.Timing.OpenWindows = append(activity.Timing.OpenWindows, &liveroutev1.TimeWindow{
				OpensAtUnixMs: window.OpensAt.UnixMilli(), ClosesAtUnixMs: window.ClosesAt.UnixMilli(),
			})
		}
		trip.Activities = append(trip.Activities, activity)
	}
	for _, value := range state.TravelDelays {
		trip.TravelDelays = append(trip.TravelDelays, &liveroutev1.TravelDelayState{
			FromActivityId: value.FromActivityID, ToActivityId: value.ToActivityID,
			AdditionalSeconds: value.AdditionalSeconds,
			ObservedAtUnixMs:  value.ObservedAt.UnixMilli(),
		})
	}
	return &liveroutev1.BootstrapTrip{
		Base:                      &liveroutev1.BootstrapTrip_FullTrip{FullTrip: trip},
		FinalizedMutationSequence: state.FinalizedMutationSequence,
		TripRevision:              state.TripRevision,
		CurrentPlan:               plan,
	}, nil
}

func travelMode(value string) liveroutev1.TravelMode {
	if value == "walking" {
		return liveroutev1.TravelMode_TRAVEL_MODE_WALKING
	}
	return liveroutev1.TravelMode_TRAVEL_MODE_DRIVING
}

func activityClass(value string) liveroutev1.ActivityClass {
	if value == "fixed" {
		return liveroutev1.ActivityClass_ACTIVITY_CLASS_FIXED
	}
	return liveroutev1.ActivityClass_ACTIVITY_CLASS_FLEXIBLE
}

func activityState(value persistence.ActivityState) liveroutev1.ActivityState {
	switch value {
	case persistence.ActivityStatePlanned:
		return liveroutev1.ActivityState_ACTIVITY_STATE_PLANNED
	case persistence.ActivityStateStarted:
		return liveroutev1.ActivityState_ACTIVITY_STATE_STARTED
	case persistence.ActivityStateCompleted:
		return liveroutev1.ActivityState_ACTIVITY_STATE_COMPLETED
	case persistence.ActivityStateSkipped:
		return liveroutev1.ActivityState_ACTIVITY_STATE_SKIPPED
	default:
		return liveroutev1.ActivityState_ACTIVITY_STATE_UNSPECIFIED
	}
}
