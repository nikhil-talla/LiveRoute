package dispatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
	"google.golang.org/protobuf/proto"
)

type crashPlanner struct{}

func (crashPlanner) Exchange(_ context.Context, request *liveroutev1.PlannerStreamRequest) (*liveroutev1.PlannerStreamResponse, error) {
	if bootstrap := request.GetBootstrapTrip(); bootstrap != nil {
		return &liveroutev1.PlannerStreamResponse{
			RequestId: request.GetRequestId(), TripId: request.GetTripId(),
			RuntimeEpoch: request.GetRuntimeEpoch(), TripRevision: bootstrap.GetTripRevision(),
			Payload: &liveroutev1.PlannerStreamResponse_TripBootstrapped{
				TripBootstrapped: &liveroutev1.TripBootstrapped{
					Status:                    liveroutev1.StatusCode_STATUS_CODE_OK,
					CurrentPlanId:             bootstrap.GetCurrentPlan().GetPlanId(),
					AcceptedMutationSequence:  bootstrap.GetFinalizedMutationSequence(),
					FinalizedMutationSequence: bootstrap.GetFinalizedMutationSequence(),
				},
			},
		}, nil
	}
	if request.GetApplyEvent() != nil {
		return &liveroutev1.PlannerStreamResponse{
			RequestId: request.GetRequestId(), TripId: request.GetTripId(),
			RuntimeEpoch: request.GetRuntimeEpoch(), TripRevision: 2,
			AcceptedMutationSequence: request.GetMutationSequence(),
			PlannerStateVersion:      1,
			Payload: &liveroutev1.PlannerStreamResponse_EventAcknowledged{
				EventAcknowledged: &liveroutev1.EventAcknowledged{
					Disposition:              liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED,
					Status:                   liveroutev1.StatusCode_STATUS_CODE_OK,
					EventId:                  request.GetApplyEvent().GetEventId(),
					ResolvedMutationSequence: request.GetMutationSequence(),
				},
			},
		}, nil
	}
	sequence := request.GetConfirmFinalizedMutations().GetFinalizedMutationSequence()
	return &liveroutev1.PlannerStreamResponse{
		RequestId: request.GetRequestId(), TripId: request.GetTripId(),
		RuntimeEpoch: request.GetRuntimeEpoch(),
		Payload: &liveroutev1.PlannerStreamResponse_FinalizedMutationsAcknowledged{
			FinalizedMutationsAcknowledged: &liveroutev1.FinalizedMutationsAcknowledged{
				Status:                    liveroutev1.StatusCode_STATUS_CODE_OK,
				FinalizedMutationSequence: sequence,
			},
		},
	}, nil
}

type crashBeforeFinalization struct{}

func (crashBeforeFinalization) Finalize(context.Context, persistence.ClaimedOutboxRow, *liveroutev1.ApplyTripEvent, *liveroutev1.PlannerStreamResponse, *liveroutev1.EventAcknowledged) error {
	return errors.New("injected backend crash before finalization")
}

func TestRuntimeCommandConvergesAfterAcceptanceCrashAndHigherEpochBootstrap(t *testing.T) {
	databaseURL := os.Getenv("LIVEROUTE_TEST_CRASH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LIVEROUTE_TEST_CRASH_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		userID     = "e1111111-1111-4111-8111-111111111111"
		tripID     = "e2222222-2222-4222-8222-222222222222"
		planID     = "e3333333-3333-4333-8333-333333333333"
		activityID = "e4444444-4444-4444-8444-444444444444"
		intentID   = "e5555555-5555-4555-8555-555555555555"
		outboxID   = "e6666666-6666-4666-8666-666666666666"
		eventID    = "e7777777-7777-4777-8777-777777777777"
		holderA    = "e8888888-8888-4888-8888-888888888888"
		holderB    = "e9999999-9999-4999-8999-999999999999"
		claimA     = "eaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		claimB     = "ebbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	createdAt := time.UnixMilli(1_800_000_000_000).UTC()
	start := createdAt.Add(time.Hour).UnixMilli()
	end := start + 60_000
	plan := &liveroutev1.CurrentPlan{
		PlanId: planID, PlanRevision: 1,
		Origin:          liveroutev1.PlanOrigin_PLAN_ORIGIN_USER_AUTHORED,
		CreatedAtUnixMs: createdAt.UnixMilli(),
		Segments: []*liveroutev1.CurrentPlanSegment{{
			ActivityId:           activityID,
			State:                liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_SCHEDULED,
			ScheduledStartUnixMs: &start, ScheduledEndUnixMs: &end,
		}},
	}
	planWire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planChecksum := sha256.Sum256(planWire)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, 'Crash test', 'America/New_York')
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trips (
			id, owner_user_id, default_time_zone_name, trip_revision,
			next_mutation_sequence, finalized_mutation_sequence, current_plan_id
		) VALUES ($1, $2, 'America/New_York', 1, 2, 1, $3)
	`, tripID, userID, planID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO itinerary_plans (
			id, trip_id, plan_revision, origin, authored_by_user_id,
			schema_version, payload, payload_size_bytes, checksum_sha256, created_at
		) VALUES ($1, $2, 1, 'user_authored', $3, 1, $4, $5, $6, $7)
	`, planID, tripID, userID, planWire, len(planWire), planChecksum[:], createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trip_activities (
			id, trip_id, ordinal, place_id, display_name, latitude, longitude,
			time_zone_name, inbound_travel_mode, activity_class, activity_state,
			priority_rank, utility_score, reservation_grace_seconds,
			min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
			mandatory, can_shorten, can_move, can_skip
		) VALUES ($1, $2, 0, 'place', 'Activity', 40, -74,
			'America/New_York', 'walking', 'flexible', 'planned', 1, 1,
			0, 60, 60, 60, false, false, true, true)
	`, activityID, tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_open_windows (
			trip_id, activity_id, window_index, opens_at, closes_at
		) VALUES ($1, $2, 0, $3, $4)
	`, tripID, activityID, createdAt, createdAt.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM trips WHERE id = $1", tripID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	eventPayload, err := plannerwire.EncodeStoredEvent(&liveroutev1.ApplyTripEvent{
		EventId: eventID, OccurredAtUnixMs: createdAt.UnixMilli(),
		Event: &liveroutev1.ApplyTripEvent_ActivityDelayed{
			ActivityDelayed: &liveroutev1.ActivityDelayed{ActivityId: activityID, DelaySeconds: 30},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commandPayload := []byte(`{"delay_seconds":30}`)
	commandStore, _ := persistence.NewCommandStore(pool)
	if _, err := commandStore.RecordRuntimeFirst(ctx, persistence.RecordRuntimeCommandRequest{
		IntentID: intentID, OutboxID: outboxID, TripID: tripID, OwnerUserID: userID,
		MessageID: eventID, EventID: eventID, ExpectedTripRevision: 1,
		Kind:          persistence.CommandActivityDelayed,
		PayloadDigest: sha256.Sum256(commandPayload), CommandPayload: commandPayload,
		EventPayload: eventPayload,
	}); err != nil {
		t.Fatal(err)
	}
	outboxStore, _ := persistence.NewOutboxStore(pool)
	leaseStore, _ := persistence.NewLeaseStore(pool)
	canonicalStore, _ := persistence.NewCanonicalStateStore(pool)
	snapshotStore, _ := persistence.NewSnapshotStore(pool)
	if _, err := leaseStore.Acquire(ctx, tripID, holderA, time.Minute); err != nil {
		t.Fatal(err)
	}

	first, err := New(Config{
		ClaimOwner: claimA, LeaseHolder: holderA, BatchSize: 1,
		ClaimDuration: time.Second, AttemptTimeout: 500 * time.Millisecond,
	}, outboxStore, leaseStore, crashPlanner{}, canonicalStore,
		crashBeforeFinalization{}, commandStore)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := first.RunOnce(ctx); err == nil || resolved != 0 {
		t.Fatalf("injected crash did not leave work replayable: resolved=%d err=%v", resolved, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE trip_runtime_leases SET lease_expires_at = clock_timestamp() - interval '1 millisecond'
		WHERE trip_id = $1
	`, tripID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE planner_outbox SET claim_expires_at = clock_timestamp() - interval '1 millisecond'
		WHERE id = $1
	`, outboxID); err != nil {
		t.Fatal(err)
	}
	lease, err := leaseStore.Acquire(ctx, tripID, holderB, time.Minute)
	if err != nil || lease.RuntimeEpoch != 2 {
		t.Fatalf("restart did not acquire epoch 2: %+v %v", lease, err)
	}
	bootstrapper, _ := NewBootstrapper(
		holderB, time.Second, canonicalStore, snapshotStore, leaseStore, crashPlanner{},
	)
	if _, err := bootstrapper.Bootstrap(ctx, tripID); err != nil {
		t.Fatal(err)
	}
	runtimeFinalizer, _ := NewRuntimeFinalizer(commandStore)
	second, err := New(Config{
		ClaimOwner: claimB, LeaseHolder: holderB, BatchSize: 1,
		ClaimDuration: time.Second, AttemptTimeout: 500 * time.Millisecond,
	}, outboxStore, leaseStore, crashPlanner{}, canonicalStore,
		runtimeFinalizer, commandStore)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := second.RunOnce(ctx); err != nil || resolved != 1 {
		t.Fatalf("restart replay did not converge: resolved=%d err=%v", resolved, err)
	}
	var revision, finalized, delay, attempts int64
	var intentState, deliveryState string
	var confirmed bool
	if err := pool.QueryRow(ctx, `
		SELECT trip.trip_revision, trip.finalized_mutation_sequence,
		       activity.activity_delay_seconds, intent.state,
		       outbox.delivery_state, outbox.attempt_count,
		       outbox.finalization_confirmed_at IS NOT NULL
		FROM trips AS trip
		JOIN trip_activities AS activity ON activity.trip_id = trip.id
		JOIN command_intents AS intent ON intent.trip_id = trip.id AND intent.id = $2
		JOIN planner_outbox AS outbox ON outbox.command_intent_id = intent.id
		WHERE trip.id = $1 AND activity.id = $3
	`, tripID, intentID, activityID).Scan(
		&revision, &finalized, &delay, &intentState, &deliveryState, &attempts, &confirmed,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || finalized != 2 || delay != 30 || intentState != "applied" ||
		deliveryState != "accepted" || attempts != 2 || !confirmed {
		t.Fatalf("unexpected converged state: revision=%d finalized=%d delay=%d intent=%s outbox=%s attempts=%d confirmed=%v",
			revision, finalized, delay, intentState, deliveryState, attempts, confirmed)
	}
}
