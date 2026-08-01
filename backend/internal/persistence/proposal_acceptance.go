package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrPendingProposalNotFound = errors.New("pending plan proposal was not found")

type ProposalAcceptancePreparation struct {
	ProposalID       string
	TripID           string
	Source           ProposalSource
	Payload          []byte
	CreatedAt        time.Time
	CurrentPlanID    string
	NextPlanRevision uint64
}

// PrepareProposalAcceptance reads the exact pending proposal and allocates
// the metadata needed for the planned accepted-engine CurrentPlan. It does
// not mutate proposal or trip state; RecordRuntimeFirst persists the prepared
// bytes before planner dispatch, and finalization verifies them again.
func (store *ProposalStore) PrepareProposalAcceptance(
	ctx context.Context,
	tripID string,
	proposalID string,
) (ProposalAcceptancePreparation, error) {
	if !validCanonicalUUID(tripID) || !validCanonicalUUID(proposalID) {
		return ProposalAcceptancePreparation{}, errors.New("proposal acceptance identifiers are invalid")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProposalAcceptancePreparation{}, fmt.Errorf("begin proposal acceptance preparation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentPlanID string
	if err := tx.QueryRow(ctx, `
		SELECT current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, tripID).Scan(&currentPlanID); errors.Is(err, pgx.ErrNoRows) {
		return ProposalAcceptancePreparation{}, ErrTripNotFound
	} else if err != nil {
		return ProposalAcceptancePreparation{}, fmt.Errorf("lock proposal trip: %w", err)
	}
	var result ProposalAcceptancePreparation
	var runtimeEpoch, plannerVersion, tripRevision, acceptedSequence int64
	var basePlanID string
	err = tx.QueryRow(ctx, `
		SELECT id::text, trip_id::text, base_current_plan_id::text,
		       source_runtime_epoch, source_planner_state_version,
		       source_trip_revision, source_accepted_mutation_sequence, payload
		FROM plan_proposals
		WHERE id = $1 AND trip_id = $2 AND state = 'pending'
		FOR UPDATE
	`, proposalID, tripID).Scan(
		&result.ProposalID, &result.TripID, &basePlanID,
		&runtimeEpoch, &plannerVersion, &tripRevision, &acceptedSequence, &result.Payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProposalAcceptancePreparation{}, ErrPendingProposalNotFound
	}
	if err != nil {
		return ProposalAcceptancePreparation{}, fmt.Errorf("lock pending proposal: %w", err)
	}
	var maximumRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(plan_revision), 0)
		FROM itinerary_plans
		WHERE trip_id = $1
	`, tripID).Scan(&maximumRevision); err != nil {
		return ProposalAcceptancePreparation{}, fmt.Errorf("read proposal plan revision: %w", err)
	}
	if runtimeEpoch <= 0 || plannerVersion < 0 || tripRevision <= 0 || acceptedSequence <= 0 ||
		maximumRevision < 0 || maximumRevision == math.MaxInt64 ||
		!validCanonicalUUID(basePlanID) || !validCanonicalUUID(currentPlanID) {
		return ProposalAcceptancePreparation{}, errors.New("pending proposal metadata is invalid")
	}
	var createdAt time.Time
	if err := tx.QueryRow(ctx, "SELECT date_trunc('milliseconds', clock_timestamp())").Scan(&createdAt); err != nil {
		return ProposalAcceptancePreparation{}, fmt.Errorf("allocate proposal plan timestamp: %w", err)
	}
	result.Source = ProposalSource{
		RuntimeEpoch: uint64(runtimeEpoch), PlannerStateVersion: uint64(plannerVersion),
		TripRevision: uint64(tripRevision), AcceptedMutationSequence: uint64(acceptedSequence),
		BaseCurrentPlanID: basePlanID,
	}
	result.CreatedAt = createdAt.UTC()
	result.CurrentPlanID = currentPlanID
	result.NextPlanRevision = uint64(maximumRevision + 1)
	if err := tx.Commit(ctx); err != nil {
		return ProposalAcceptancePreparation{}, fmt.Errorf("commit proposal acceptance preparation: %w", err)
	}
	return result, nil
}
