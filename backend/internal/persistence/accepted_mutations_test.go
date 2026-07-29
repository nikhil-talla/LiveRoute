package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type acceptedMutationFixture struct {
	trip       commandTripFixture
	activityID string
	otherID    string
	recorded   RecordedCommand
}

func createAcceptedMutationFixture(
	t *testing.T,
	prefix string,
	kind CommandKind,
) (*CommandStore, acceptedMutationFixture) {
	t.Helper()
	pool, ctx := openPersistenceTestPool(t)
	fixture := acceptedMutationFixture{
		trip: commandTripFixture{
			userID: prefix + "111111-1111-1111-1111-111111111111",
			tripID: prefix + "222222-2222-2222-2222-222222222222",
			planID: prefix + "333333-3333-3333-3333-333333333333",
		},
		activityID: prefix + "444444-4444-4444-4444-444444444444",
		otherID:    prefix + "555555-5555-5555-5555-555555555555",
	}
	createCommandTrip(t, ctx, pool, fixture.trip)
	for ordinal, activityID := range []string{
		fixture.activityID,
		fixture.otherID,
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO trip_activities (
				id, trip_id, ordinal, place_id, display_name,
				latitude, longitude, time_zone_name, inbound_travel_mode,
				activity_class, activity_state, priority_rank, utility_score,
				reservation_grace_seconds, min_duration_seconds,
				preferred_duration_seconds, max_duration_seconds,
				mandatory, can_shorten, can_move, can_skip
			) VALUES (
				$1, $2, $3, $4, $4,
				40.0, -74.0, 'America/New_York', 'walking',
				'flexible', 'planned', $3, 1,
				0, 60, 60, 60, false, false, true, true
			)
		`, activityID, fixture.trip.tripID, ordinal,
			fmt.Sprintf("place-%d", ordinal)); err != nil {
			t.Fatal(err)
		}
	}
	command := commandRequest(
		fixture.trip,
		prefix+"666666-6666-6666-6666-666666666666",
		prefix+"777777-7777-7777-7777-777777777777",
		prefix+"888888-8888-8888-8888-888888888888",
	)
	command.Kind = kind
	store, err := NewCommandStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture.recorded, err = store.RecordRuntimeFirst(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func acceptedMutationRequest(
	fixture acceptedMutationFixture,
	mutation AcceptedMutation,
) FinalizeAcceptedMutationRequest {
	return FinalizeAcceptedMutationRequest{
		TripID:                       fixture.recorded.TripID,
		IntentID:                     fixture.recorded.IntentID,
		OutboxID:                     fixture.recorded.OutboxID,
		EventID:                      fixture.recorded.EventID,
		MutationSequence:             fixture.recorded.MutationSequence,
		ExpectedTripRevision:         fixture.recorded.ExpectedTripRevision,
		ResultingPlannerStateVersion: 19,
		Mutation:                     mutation,
		OutcomePayload:               []byte(`{"safe_message":"applied"}`),
	}
}

func TestFinalizeAcceptedMutationAppliesEveryCanonicalMapping(t *testing.T) {
	const start = int64(1_784_000_000_123)
	tests := []struct {
		name     string
		prefix   string
		kind     CommandKind
		mutation func(acceptedMutationFixture) AcceptedMutation
		verify   func(*testing.T, *CommandStore, acceptedMutationFixture)
	}{
		{
			name:   "activity status",
			prefix: "10",
			kind:   CommandActivityStatusChanged,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				return AcceptedMutation{ActivityStatus: &ActivityStatusMutation{
					ActivityID: f.activityID,
					State:      ActivityStateStarted,
				}}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var value string
				if err := store.pool.QueryRow(context.Background(), `
					SELECT activity_state
					FROM trip_activities
					WHERE trip_id = $1 AND id = $2
				`, f.trip.tripID, f.activityID).Scan(&value); err != nil {
					t.Fatal(err)
				}
				if value != "started" {
					t.Fatalf("unexpected activity state %q", value)
				}
			},
		},
		{
			name:   "activity delay",
			prefix: "20",
			kind:   CommandActivityDelayed,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				return AcceptedMutation{ActivityDelay: &ActivityDelayMutation{
					ActivityID: f.activityID, DelaySeconds: 41,
				}}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var value int
				if err := store.pool.QueryRow(context.Background(), `
					SELECT activity_delay_seconds
					FROM trip_activities
					WHERE trip_id = $1 AND id = $2
				`, f.trip.tripID, f.activityID).Scan(&value); err != nil {
					t.Fatal(err)
				}
				if value != 41 {
					t.Fatalf("unexpected activity delay %d", value)
				}
			},
		},
		{
			name:   "reservation",
			prefix: "30",
			kind:   CommandReservationChanged,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				value := start
				return AcceptedMutation{Reservation: &ReservationMutation{
					ActivityID:                       f.activityID,
					ReservationStartUnixMilliseconds: &value,
					ReservationGraceSeconds:          42,
				}}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var milliseconds int64
				var grace int
				if err := store.pool.QueryRow(context.Background(), `
					SELECT floor(extract(epoch FROM reservation_start) * 1000)::bigint,
					       reservation_grace_seconds
					FROM trip_activities
					WHERE trip_id = $1 AND id = $2
				`, f.trip.tripID, f.activityID).Scan(
					&milliseconds,
					&grace,
				); err != nil {
					t.Fatal(err)
				}
				if milliseconds != start || grace != 42 {
					t.Fatalf("unexpected reservation %d/%d", milliseconds, grace)
				}
			},
		},
		{
			name:   "mandatory deadline",
			prefix: "40",
			kind:   CommandMandatoryDeadlineChanged,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				return AcceptedMutation{
					MandatoryDeadline: &MandatoryDeadlineMutation{
						ActivityID:                   f.activityID,
						LatestFinishUnixMilliseconds: start + 1_000,
					},
				}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var milliseconds int64
				if err := store.pool.QueryRow(context.Background(), `
					SELECT floor(extract(epoch FROM mandatory_deadline) * 1000)::bigint
					FROM trip_activities
					WHERE trip_id = $1 AND id = $2
				`, f.trip.tripID, f.activityID).Scan(&milliseconds); err != nil {
					t.Fatal(err)
				}
				if milliseconds != start+1_000 {
					t.Fatalf("unexpected deadline %d", milliseconds)
				}
			},
		},
		{
			name:   "operating hours",
			prefix: "50",
			kind:   CommandOperatingHoursChanged,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				return AcceptedMutation{
					OperatingHours: &OperatingHoursMutation{
						ActivityID: f.activityID,
						OpenWindows: []OpenWindow{
							{
								OpensAtUnixMilliseconds:  start,
								ClosesAtUnixMilliseconds: start + 1_000,
							},
							{
								OpensAtUnixMilliseconds:  start + 1_000,
								ClosesAtUnixMilliseconds: start + 2_000,
							},
						},
					},
				}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var count int
				var firstOpen int64
				if err := store.pool.QueryRow(context.Background(), `
					SELECT count(*),
					       min(floor(extract(epoch FROM opens_at) * 1000))::bigint
					FROM activity_open_windows
					WHERE trip_id = $1 AND activity_id = $2
				`, f.trip.tripID, f.activityID).Scan(&count, &firstOpen); err != nil {
					t.Fatal(err)
				}
				if count != 2 || firstOpen != start {
					t.Fatalf("unexpected windows %d/%d", count, firstOpen)
				}
			},
		},
		{
			name:   "place found closed",
			prefix: "60",
			kind:   CommandPlaceFoundClosed,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				return AcceptedMutation{
					PlaceFoundClosed: &PlaceFoundClosedMutation{
						ActivityID:                 f.activityID,
						ObservedAtUnixMilliseconds: start + 2_000,
					},
				}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var milliseconds int64
				if err := store.pool.QueryRow(context.Background(), `
					SELECT floor(extract(epoch FROM found_closed_at) * 1000)::bigint
					FROM trip_activities
					WHERE trip_id = $1 AND id = $2
				`, f.trip.tripID, f.activityID).Scan(&milliseconds); err != nil {
					t.Fatal(err)
				}
				if milliseconds != start+2_000 {
					t.Fatalf("unexpected closure observation %d", milliseconds)
				}
			},
		},
		{
			name:   "travel delay",
			prefix: "70",
			kind:   CommandTravelDelay,
			mutation: func(f acceptedMutationFixture) AcceptedMutation {
				return AcceptedMutation{TravelDelay: &TravelDelayMutation{
					FromActivityID:             f.activityID,
					ToActivityID:               f.otherID,
					AdditionalSeconds:          43,
					ObservedAtUnixMilliseconds: start + 3_000,
				}}
			},
			verify: func(t *testing.T, store *CommandStore, f acceptedMutationFixture) {
				var seconds int
				var milliseconds int64
				if err := store.pool.QueryRow(context.Background(), `
					SELECT additional_seconds,
					       floor(extract(epoch FROM observed_at) * 1000)::bigint
					FROM trip_travel_delays
					WHERE trip_id = $1
					  AND from_activity_id = $2
					  AND to_activity_id = $3
				`, f.trip.tripID, f.activityID, f.otherID).Scan(
					&seconds,
					&milliseconds,
				); err != nil {
					t.Fatal(err)
				}
				if seconds != 43 || milliseconds != start+3_000 {
					t.Fatalf("unexpected travel delay %d/%d", seconds, milliseconds)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, fixture := createAcceptedMutationFixture(
				t,
				test.prefix,
				test.kind,
			)
			request := acceptedMutationRequest(
				fixture,
				test.mutation(fixture),
			)
			finalized, err := store.FinalizeAcceptedMutation(
				context.Background(),
				request,
			)
			if err != nil {
				t.Fatal(err)
			}
			if finalized.Duplicate ||
				finalized.State != "applied" ||
				finalized.Status != "OK" ||
				finalized.ResultingTripRevision != 2 ||
				finalized.ResultingPlannerStateVersion != 19 {
				t.Fatalf("unexpected finalization: %+v", finalized)
			}
			test.verify(t, store, fixture)

			var revision int64
			var finalizedSequence int64
			var intentState string
			var outboxState string
			if err := store.pool.QueryRow(context.Background(), `
				SELECT trip.trip_revision,
				       trip.finalized_mutation_sequence,
				       intent.state,
				       outbox.delivery_state
				FROM trips AS trip
				JOIN command_intents AS intent ON intent.trip_id = trip.id
				JOIN planner_outbox AS outbox
				  ON outbox.command_intent_id = intent.id
				WHERE trip.id = $1 AND intent.id = $2
			`, fixture.trip.tripID, fixture.recorded.IntentID).Scan(
				&revision,
				&finalizedSequence,
				&intentState,
				&outboxState,
			); err != nil {
				t.Fatal(err)
			}
			if revision != 2 ||
				finalizedSequence != 2 ||
				intentState != "applied" ||
				outboxState != "accepted" {
				t.Fatalf(
					"unexpected durable state %d/%d/%s/%s",
					revision,
					finalizedSequence,
					intentState,
					outboxState,
				)
			}
		})
	}
}

func TestFinalizeAcceptedMutationInvalidatesPendingProposal(t *testing.T) {
	store, fixture := createAcceptedMutationFixture(
		t,
		"80",
		CommandActivityDelayed,
	)
	payload := []byte{1}
	checksum := sha256.Sum256(payload)
	proposalID := "80999999-9999-9999-9999-999999999999"
	if _, err := store.pool.Exec(context.Background(), `
		INSERT INTO plan_proposals (
			id, trip_id, base_current_plan_id, source_runtime_epoch,
			source_planner_state_version, source_trip_revision,
			source_accepted_mutation_sequence, schema_version,
			payload, payload_size_bytes, checksum_sha256, state, created_at
		) VALUES ($1, $2, $3, 1, 1, 1, 1, 1, $4, 1, $5, 'pending',
		          clock_timestamp())
	`, proposalID, fixture.trip.tripID, fixture.trip.planID,
		payload, checksum[:]); err != nil {
		t.Fatal(err)
	}
	request := acceptedMutationRequest(fixture, AcceptedMutation{
		ActivityDelay: &ActivityDelayMutation{
			ActivityID: fixture.activityID, DelaySeconds: 1,
		},
	})
	if _, err := store.FinalizeAcceptedMutation(
		context.Background(),
		request,
	); err != nil {
		t.Fatal(err)
	}
	var state string
	var decided bool
	if err := store.pool.QueryRow(context.Background(), `
		SELECT state, decided_at IS NOT NULL
		FROM plan_proposals
		WHERE id = $1
	`, proposalID).Scan(&state, &decided); err != nil {
		t.Fatal(err)
	}
	if state != "stale" || !decided {
		t.Fatalf("pending proposal was not invalidated: %s/%t", state, decided)
	}
}

func TestFinalizeAcceptedMutationRollsBackMissingTarget(t *testing.T) {
	store, fixture := createAcceptedMutationFixture(
		t,
		"90",
		CommandActivityDelayed,
	)
	request := acceptedMutationRequest(fixture, AcceptedMutation{
		ActivityDelay: &ActivityDelayMutation{
			ActivityID:   "90ffffff-ffff-ffff-ffff-ffffffffffff",
			DelaySeconds: 1,
		},
	})
	_, err := store.FinalizeAcceptedMutation(context.Background(), request)
	if !errors.Is(err, ErrMutationTargetNotFound) {
		t.Fatalf("expected missing target, got %v", err)
	}
	var revision int64
	var finalized int64
	var intentState string
	if err := store.pool.QueryRow(context.Background(), `
		SELECT trip.trip_revision,
		       trip.finalized_mutation_sequence,
		       intent.state
		FROM trips AS trip
		JOIN command_intents AS intent ON intent.trip_id = trip.id
		WHERE trip.id = $1 AND intent.id = $2
	`, fixture.trip.tripID, fixture.recorded.IntentID).Scan(
		&revision,
		&finalized,
		&intentState,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || finalized != 1 || intentState != "pending" {
		t.Fatalf("failed mutation was partially committed: %d/%d/%s",
			revision, finalized, intentState)
	}
}

func TestFinalizeAcceptedMutationExactReplayAndConcurrency(t *testing.T) {
	store, fixture := createAcceptedMutationFixture(
		t,
		"a0",
		CommandActivityDelayed,
	)
	request := acceptedMutationRequest(fixture, AcceptedMutation{
		ActivityDelay: &ActivityDelayMutation{
			ActivityID: fixture.activityID, DelaySeconds: 5,
		},
	})
	const callers = 2
	results := make(chan FinalizedCommand, callers)
	failures := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.FinalizeAcceptedMutation(
				context.Background(),
				request,
			)
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	duplicateCount := 0
	var finalizedAt int64
	for result := range results {
		if result.Duplicate {
			duplicateCount++
		}
		current := result.FinalizedAt.UnixNano()
		if finalizedAt != 0 && current != finalizedAt {
			t.Fatalf("replay returned different finalization times")
		}
		finalizedAt = current
	}
	if duplicateCount != 1 {
		t.Fatalf("expected one duplicate, got %d", duplicateCount)
	}

	conflict := request
	conflict.OutcomePayload = []byte(`{"safe_message":"different"}`)
	if _, err := store.FinalizeAcceptedMutation(
		context.Background(),
		conflict,
	); !errors.Is(err, ErrCommandFinalizationConflict) {
		t.Fatalf("expected replay conflict, got %v", err)
	}
}
