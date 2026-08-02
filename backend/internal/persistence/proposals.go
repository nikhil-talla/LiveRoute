package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrProposalStale            = errors.New("proposal source is stale")
	ErrProposalIdentityConflict = errors.New("proposal id conflicts with stored proposal")
	ErrProposalPayloadInvalid   = errors.New("proposal payload is invalid")
)

type ProposalSource struct {
	RuntimeEpoch             uint64
	PlannerStateVersion      uint64
	TripRevision             uint64
	AcceptedMutationSequence uint64
	BaseCurrentPlanID        string
}

type RuntimeFreshness struct {
	HolderID                 string
	RuntimeEpoch             uint64
	PlannerStateVersion      uint64
	AcceptedMutationSequence uint64
}

type PersistProposalRequest struct {
	ProposalID string
	TripID     string
	Source     ProposalSource
	Current    RuntimeFreshness
	Payload    []byte
	Checksum   [32]byte
	CreatedAt  time.Time
}

type PersistedProposal struct {
	ProposalID              string
	TripID                  string
	Source                  ProposalSource
	State                   string
	PayloadSizeBytes        uint64
	Checksum                [32]byte
	CreatedAt               time.Time
	Duplicate               bool
	Publishable             bool
	SupersededProposalCount uint64
	Payload                 []byte
}

type ProposalStore struct {
	pool *pgxpool.Pool
}

// LatestPendingPayload returns the exact deterministic StoredPlanProposal
// retained for a trip. Proposal contents are read separately from canonical
// current-plan state so an advisory result can never become plan authority.
func (store *ProposalStore) LatestPendingPayload(
	ctx context.Context,
	tripID string,
) ([]byte, error) {
	if !validCanonicalUUID(tripID) {
		return nil, errors.New("trip id is invalid")
	}
	var payload []byte
	err := store.pool.QueryRow(ctx, `
		SELECT payload
		FROM plan_proposals
		WHERE trip_id = $1 AND state = 'pending'
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, tripID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPendingProposalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load pending proposal: %w", err)
	}
	return append([]byte(nil), payload...), nil
}

func NewProposalStore(pool *pgxpool.Pool) (*ProposalStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &ProposalStore{pool: pool}, nil
}

func validatePersistProposal(request PersistProposalRequest) error {
	for _, value := range []string{
		request.ProposalID,
		request.TripID,
		request.Source.BaseCurrentPlanID,
		request.Current.HolderID,
	} {
		if !validCanonicalUUID(value) {
			return errors.New(
				"proposal identifiers must be canonical lowercase UUIDs",
			)
		}
	}
	for _, value := range []uint64{
		request.Source.RuntimeEpoch,
		request.Source.PlannerStateVersion,
		request.Source.TripRevision,
		request.Source.AcceptedMutationSequence,
		request.Current.RuntimeEpoch,
		request.Current.PlannerStateVersion,
		request.Current.AcceptedMutationSequence,
	} {
		if value > math.MaxInt64 {
			return errors.New("proposal source version is out of range")
		}
	}
	if request.Source.RuntimeEpoch == 0 ||
		request.Source.TripRevision == 0 ||
		request.Source.AcceptedMutationSequence == 0 ||
		request.Current.RuntimeEpoch == 0 ||
		request.Current.AcceptedMutationSequence == 0 {
		return errors.New("proposal source versions must be positive")
	}
	if len(request.Payload) == 0 ||
		len(request.Payload) > math.MaxInt32 {
		return ErrProposalPayloadInvalid
	}
	calculated := sha256.Sum256(request.Payload)
	if subtle.ConstantTimeCompare(calculated[:], request.Checksum[:]) != 1 {
		return ErrProposalPayloadInvalid
	}
	if request.CreatedAt.IsZero() ||
		request.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 {
		return errors.New(
			"proposal creation time must have exact millisecond precision",
		)
	}
	return nil
}

type lockedProposal struct {
	id                     string
	baseCurrentPlanID      string
	sourceRuntimeEpoch     int64
	sourcePlannerState     int64
	sourceTripRevision     int64
	sourceAcceptedMutation int64
	payload                []byte
	payloadSize            int32
	checksum               []byte
	state                  string
	createdAt              time.Time
}

func lockProposals(
	ctx context.Context,
	tx pgx.Tx,
	tripID string,
) ([]lockedProposal, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,
		       base_current_plan_id::text,
		       source_runtime_epoch,
		       source_planner_state_version,
		       source_trip_revision,
		       source_accepted_mutation_sequence,
		       payload,
		       payload_size_bytes,
		       checksum_sha256,
		       state,
		       created_at
		FROM plan_proposals
		WHERE trip_id = $1
		ORDER BY id
		FOR UPDATE
	`, tripID)
	if err != nil {
		return nil, fmt.Errorf("lock proposals: %w", err)
	}
	defer rows.Close()

	var result []lockedProposal
	for rows.Next() {
		var proposal lockedProposal
		if err := rows.Scan(
			&proposal.id,
			&proposal.baseCurrentPlanID,
			&proposal.sourceRuntimeEpoch,
			&proposal.sourcePlannerState,
			&proposal.sourceTripRevision,
			&proposal.sourceAcceptedMutation,
			&proposal.payload,
			&proposal.payloadSize,
			&proposal.checksum,
			&proposal.state,
			&proposal.createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan locked proposal: %w", err)
		}
		result = append(result, proposal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate locked proposals: %w", err)
	}
	return result, nil
}

func proposalMatches(
	stored lockedProposal,
	request PersistProposalRequest,
) bool {
	return stored.baseCurrentPlanID == request.Source.BaseCurrentPlanID &&
		stored.sourceRuntimeEpoch == int64(request.Source.RuntimeEpoch) &&
		stored.sourcePlannerState ==
			int64(request.Source.PlannerStateVersion) &&
		stored.sourceTripRevision == int64(request.Source.TripRevision) &&
		stored.sourceAcceptedMutation ==
			int64(request.Source.AcceptedMutationSequence) &&
		stored.payloadSize == int32(len(request.Payload)) &&
		len(stored.checksum) == sha256.Size &&
		subtle.ConstantTimeCompare(
			stored.checksum,
			request.Checksum[:],
		) == 1 &&
		bytes.Equal(stored.payload, request.Payload) &&
		stored.createdAt.Equal(request.CreatedAt)
}

func sourceIsCurrent(
	request PersistProposalRequest,
	tripRevision int64,
	finalizedMutationSequence int64,
	currentPlanID string,
	leaseHolderID string,
	leaseRuntimeEpoch int64,
	leaseExpiresAt time.Time,
	databaseTime time.Time,
) bool {
	return request.Source.RuntimeEpoch == request.Current.RuntimeEpoch &&
		request.Source.PlannerStateVersion ==
			request.Current.PlannerStateVersion &&
		request.Source.AcceptedMutationSequence ==
			request.Current.AcceptedMutationSequence &&
		request.Source.TripRevision == uint64(tripRevision) &&
		request.Source.AcceptedMutationSequence <=
			uint64(finalizedMutationSequence) &&
		request.Source.BaseCurrentPlanID == currentPlanID &&
		request.Current.HolderID == leaseHolderID &&
		request.Current.RuntimeEpoch == uint64(leaseRuntimeEpoch) &&
		leaseExpiresAt.After(databaseTime)
}

func persistedProposal(
	request PersistProposalRequest,
	state string,
	duplicate bool,
	publishable bool,
	superseded uint64,
) PersistedProposal {
	return PersistedProposal{
		ProposalID:              request.ProposalID,
		TripID:                  request.TripID,
		Source:                  request.Source,
		State:                   state,
		PayloadSizeBytes:        uint64(len(request.Payload)),
		Checksum:                request.Checksum,
		CreatedAt:               request.CreatedAt,
		Duplicate:               duplicate,
		Publishable:             publishable,
		SupersededProposalCount: superseded,
		Payload:                 append([]byte(nil), request.Payload...),
	}
}

func (store *ProposalStore) Persist(
	ctx context.Context,
	request PersistProposalRequest,
) (PersistedProposal, error) {
	if err := validatePersistProposal(request); err != nil {
		return PersistedProposal{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return PersistedProposal{}, fmt.Errorf("begin proposal persistence: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tripRevision int64
	var finalizedMutationSequence int64
	var currentPlanID string
	err = tx.QueryRow(ctx, `
		SELECT trip_revision,
		       finalized_mutation_sequence,
		       current_plan_id::text
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, request.TripID).Scan(
		&tripRevision,
		&finalizedMutationSequence,
		&currentPlanID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PersistedProposal{}, ErrTripNotFound
	}
	if err != nil {
		return PersistedProposal{}, fmt.Errorf("lock proposal trip: %w", err)
	}

	proposals, err := lockProposals(ctx, tx, request.TripID)
	if err != nil {
		return PersistedProposal{}, err
	}

	var leaseHolderID string
	var leaseRuntimeEpoch int64
	var leaseExpiresAt time.Time
	var databaseTime time.Time
	err = tx.QueryRow(ctx, `
		SELECT holder_id::text,
		       runtime_epoch,
		       lease_expires_at,
		       clock_timestamp()
		FROM trip_runtime_leases
		WHERE trip_id = $1
	`, request.TripID).Scan(
		&leaseHolderID,
		&leaseRuntimeEpoch,
		&leaseExpiresAt,
		&databaseTime,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PersistedProposal{}, ErrProposalStale
	}
	if err != nil {
		return PersistedProposal{}, fmt.Errorf("read proposal runtime lease: %w", err)
	}

	current := sourceIsCurrent(
		request,
		tripRevision,
		finalizedMutationSequence,
		currentPlanID,
		leaseHolderID,
		leaseRuntimeEpoch,
		leaseExpiresAt,
		databaseTime,
	)
	for _, proposal := range proposals {
		if proposal.id != request.ProposalID {
			continue
		}
		if !proposalMatches(proposal, request) {
			return PersistedProposal{}, ErrProposalIdentityConflict
		}
		result := persistedProposal(
			request,
			proposal.state,
			true,
			current && proposal.state == "pending",
			0,
		)
		if err := tx.Commit(ctx); err != nil {
			return PersistedProposal{},
				fmt.Errorf("commit duplicate proposal lookup: %w", err)
		}
		return result, nil
	}
	if !current {
		return PersistedProposal{}, ErrProposalStale
	}

	tag, err := tx.Exec(ctx, `
		UPDATE plan_proposals
		SET state = 'superseded',
		    decided_at = $2
		WHERE trip_id = $1
		  AND state = 'pending'
	`, request.TripID, databaseTime)
	if err != nil {
		return PersistedProposal{},
			fmt.Errorf("supersede pending proposal: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO plan_proposals (
			id, trip_id, base_current_plan_id, source_runtime_epoch,
			source_planner_state_version, source_trip_revision,
			source_accepted_mutation_sequence, schema_version,
			payload, payload_size_bytes, checksum_sha256, state,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, 1,
			$8, $9, $10, 'pending', $11
		)
	`, request.ProposalID, request.TripID,
		request.Source.BaseCurrentPlanID,
		int64(request.Source.RuntimeEpoch),
		int64(request.Source.PlannerStateVersion),
		int64(request.Source.TripRevision),
		int64(request.Source.AcceptedMutationSequence),
		request.Payload, len(request.Payload), request.Checksum[:],
		request.CreatedAt)
	if err != nil {
		return PersistedProposal{}, fmt.Errorf("insert proposal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PersistedProposal{}, fmt.Errorf("commit proposal persistence: %w", err)
	}
	return persistedProposal(
		request,
		"pending",
		false,
		true,
		uint64(tag.RowsAffected()),
	), nil
}
