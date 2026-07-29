package persistence

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrClaimLost = errors.New("outbox claim was lost")

type ClaimedOutboxRow struct {
	ID                 string
	CommandIntentID    string
	TripID             string
	MutationSequence   uint64
	EventSchemaVersion uint32
	EventPayload       json.RawMessage
	AttemptCount       uint64
	ClaimExpiresAt     time.Time
}

type OutboxStore struct {
	pool *pgxpool.Pool
}

func NewOutboxStore(pool *pgxpool.Pool) (*OutboxStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &OutboxStore{pool: pool}, nil
}

func (store *OutboxStore) ClaimDue(
	ctx context.Context,
	claimOwner string,
	batchSize int,
	claimDuration time.Duration,
) ([]ClaimedOutboxRow, error) {
	if claimOwner == "" {
		return nil, errors.New("claim owner is required")
	}
	if batchSize <= 0 {
		return nil, errors.New("claim batch size must be positive")
	}
	if claimDuration.Milliseconds() <= 0 {
		return nil, errors.New("claim duration must be at least one millisecond")
	}
	rows, err := store.pool.Query(ctx, `
		WITH database_time AS (
			SELECT clock_timestamp() AS now
		),
		candidates AS (
			SELECT candidate.id
			FROM planner_outbox AS candidate, database_time
			WHERE candidate.delivery_state = 'pending'
			  AND candidate.next_attempt_at <= database_time.now
			  AND (
			    candidate.claim_owner IS NULL OR
			    candidate.claim_expires_at <= database_time.now
			  )
			  AND NOT EXISTS (
			    SELECT 1
			    FROM planner_outbox AS predecessor
			    WHERE predecessor.trip_id = candidate.trip_id
			      AND predecessor.mutation_sequence <
			          candidate.mutation_sequence
			      AND predecessor.delivery_state IN (
			        'pending', 'paused_internal'
			      )
			  )
			ORDER BY candidate.next_attempt_at,
			         candidate.trip_id,
			         candidate.mutation_sequence
			FOR UPDATE OF candidate SKIP LOCKED
			LIMIT $1
		)
		UPDATE planner_outbox AS claimed
		SET claim_owner = $2,
		    claim_expires_at =
		        (SELECT now FROM database_time) +
		        ($3 * interval '1 millisecond'),
		    attempt_count = attempt_count + 1,
		    last_attempt_at = (SELECT now FROM database_time),
		    updated_at = (SELECT now FROM database_time)
		FROM candidates
		WHERE claimed.id = candidates.id
		RETURNING claimed.id::text,
		          claimed.command_intent_id::text,
		          claimed.trip_id::text,
		          claimed.mutation_sequence,
		          claimed.event_schema_version,
		          claimed.event_payload,
		          claimed.attempt_count,
		          claimed.claim_expires_at
	`, batchSize, claimOwner, claimDuration.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim due outbox rows: %w", err)
	}
	defer rows.Close()

	result := make([]ClaimedOutboxRow, 0, batchSize)
	for rows.Next() {
		var row ClaimedOutboxRow
		var mutationSequence int64
		var eventSchemaVersion int32
		var attemptCount int64
		if err := rows.Scan(
			&row.ID,
			&row.CommandIntentID,
			&row.TripID,
			&mutationSequence,
			&eventSchemaVersion,
			&row.EventPayload,
			&attemptCount,
			&row.ClaimExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan claimed outbox row: %w", err)
		}
		if mutationSequence <= 0 || eventSchemaVersion != 1 ||
			attemptCount <= 0 || !json.Valid(row.EventPayload) {
			return nil, errors.New("claimed outbox row violates storage contract")
		}
		row.MutationSequence = uint64(mutationSequence)
		row.EventSchemaVersion = uint32(eventSchemaVersion)
		row.AttemptCount = uint64(attemptCount)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox rows: %w", err)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].TripID != result[right].TripID {
			return result[left].TripID < result[right].TripID
		}
		return result[left].MutationSequence <
			result[right].MutationSequence
	})
	return result, nil
}

func RetryDelay(
	attemptCount uint64,
	random io.Reader,
) (time.Duration, error) {
	if attemptCount == 0 {
		return 0, errors.New("attempt count must be positive")
	}
	if random == nil {
		random = rand.Reader
	}
	exponent := attemptCount
	if exponent > 7 {
		exponent = 7
	}
	capDuration := 250 * time.Millisecond * time.Duration(1<<exponent)
	if capDuration > 30*time.Second {
		capDuration = 30 * time.Second
	}
	upperExclusive := new(big.Int).SetInt64(int64(capDuration) + 1)
	value, err := rand.Int(random, upperExclusive)
	if err != nil {
		return 0, fmt.Errorf("read retry jitter: %w", err)
	}
	return time.Duration(value.Int64()), nil
}

func (store *OutboxStore) ReleaseForRetry(
	ctx context.Context,
	row ClaimedOutboxRow,
	claimOwner string,
	lastStatus string,
	delay time.Duration,
) error {
	if row.ID == "" || claimOwner == "" || row.AttemptCount == 0 {
		return errors.New("claimed row identity is required")
	}
	if lastStatus == "" || delay < 0 || delay > 30*time.Second {
		return errors.New("retry status or delay is invalid")
	}
	tag, err := store.pool.Exec(ctx, `
		WITH database_time AS (SELECT clock_timestamp() AS now)
		UPDATE planner_outbox
		SET next_attempt_at =
		        (SELECT now FROM database_time) +
		        ($4 * interval '1 microsecond'),
		    last_status = $5,
		    claim_owner = NULL,
		    claim_expires_at = NULL,
		    updated_at = (SELECT now FROM database_time)
		WHERE id = $1
		  AND delivery_state = 'pending'
		  AND claim_owner = $2
		  AND attempt_count = $3
		  AND claim_expires_at > (SELECT now FROM database_time)
	`, row.ID, claimOwner, int64(row.AttemptCount),
		delay.Microseconds(), lastStatus)
	if err != nil {
		return fmt.Errorf("release outbox retry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}
