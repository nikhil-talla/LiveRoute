package persistence

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxSnapshotPayloadBytes = 2 * 1024 * 1024

var (
	ErrSnapshotInvalid  = errors.New("snapshot is invalid")
	ErrSnapshotStale    = errors.New("snapshot is older than stored state")
	ErrSnapshotConflict = errors.New("snapshot identity conflicts with storage")
	ErrSnapshotNotFound = errors.New("no compatible snapshot is available")
	ErrSnapshotNotReady = errors.New("snapshot is not ready for finalized persistence")
)

type SnapshotBlob struct {
	ID                               string
	TripID                           string
	SchemaVersion                    uint32
	SourceRuntimeEpoch               uint64
	SourcePlannerStateVersion        uint64
	TripRevision                     uint64
	CoveredFinalizedMutationSequence uint64
	Payload                          []byte
	Checksum                         [32]byte
}

type StoredSnapshot struct {
	SnapshotBlob
	CreatedAt           time.Time
	Duplicate           bool
	declaredPayloadSize int32
}

type SnapshotStore struct {
	pool *pgxpool.Pool
}

func NewSnapshotStore(pool *pgxpool.Pool) (*SnapshotStore, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &SnapshotStore{pool: pool}, nil
}

func validateSnapshotBlob(blob SnapshotBlob) error {
	if !validCanonicalUUID(blob.ID) ||
		!validCanonicalUUID(blob.TripID) {
		return fmt.Errorf("%w: identifiers are invalid", ErrSnapshotInvalid)
	}
	if blob.SchemaVersion != 1 ||
		blob.SourceRuntimeEpoch == 0 ||
		blob.SourceRuntimeEpoch > math.MaxInt64 ||
		blob.SourcePlannerStateVersion > math.MaxInt64 ||
		blob.TripRevision == 0 ||
		blob.TripRevision > math.MaxInt64 ||
		blob.CoveredFinalizedMutationSequence > math.MaxInt64 ||
		len(blob.Payload) == 0 ||
		len(blob.Payload) > MaxSnapshotPayloadBytes {
		return fmt.Errorf("%w: metadata is out of range", ErrSnapshotInvalid)
	}
	checksum := sha256.Sum256(blob.Payload)
	if subtle.ConstantTimeCompare(checksum[:], blob.Checksum[:]) != 1 {
		return fmt.Errorf("%w: checksum differs", ErrSnapshotInvalid)
	}
	payload, err := parseSnapshotPayload(blob.Payload)
	if err != nil ||
		payload.tripID != blob.TripID ||
		payload.tripRevision != blob.TripRevision ||
		payload.finalizedMutationSequence !=
			blob.CoveredFinalizedMutationSequence {
		return fmt.Errorf("%w: payload metadata differs", ErrSnapshotInvalid)
	}
	return nil
}

func snapshotIsOlder(candidate, current SnapshotBlob) bool {
	if candidate.SourceRuntimeEpoch != current.SourceRuntimeEpoch {
		return candidate.SourceRuntimeEpoch < current.SourceRuntimeEpoch
	}
	if candidate.SourcePlannerStateVersion !=
		current.SourcePlannerStateVersion {
		return candidate.SourcePlannerStateVersion <
			current.SourcePlannerStateVersion
	}
	if candidate.CoveredFinalizedMutationSequence !=
		current.CoveredFinalizedMutationSequence {
		return candidate.CoveredFinalizedMutationSequence <
			current.CoveredFinalizedMutationSequence
	}
	return candidate.TripRevision < current.TripRevision
}

func snapshotIsNewer(left, right StoredSnapshot) bool {
	if left.SourceRuntimeEpoch != right.SourceRuntimeEpoch {
		return left.SourceRuntimeEpoch > right.SourceRuntimeEpoch
	}
	if left.SourcePlannerStateVersion != right.SourcePlannerStateVersion {
		return left.SourcePlannerStateVersion >
			right.SourcePlannerStateVersion
	}
	if left.CoveredFinalizedMutationSequence !=
		right.CoveredFinalizedMutationSequence {
		return left.CoveredFinalizedMutationSequence >
			right.CoveredFinalizedMutationSequence
	}
	if left.TripRevision != right.TripRevision {
		return left.TripRevision > right.TripRevision
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ID > right.ID
}

func scanStoredSnapshot(
	row pgx.Row,
	duplicate bool,
) (StoredSnapshot, error) {
	var result StoredSnapshot
	var schemaVersion int32
	var runtimeEpoch int64
	var plannerVersion int64
	var tripRevision int64
	var coveredSequence int64
	var payloadSize int32
	var checksum []byte
	err := row.Scan(
		&result.ID,
		&result.TripID,
		&schemaVersion,
		&runtimeEpoch,
		&plannerVersion,
		&tripRevision,
		&coveredSequence,
		&payloadSize,
		&checksum,
		&result.Payload,
		&result.CreatedAt,
	)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if schemaVersion != 1 ||
		runtimeEpoch <= 0 ||
		plannerVersion < 0 ||
		tripRevision <= 0 ||
		coveredSequence < 0 ||
		payloadSize < 0 ||
		len(checksum) != sha256.Size {
		return StoredSnapshot{}, ErrSnapshotInvalid
	}
	result.SchemaVersion = uint32(schemaVersion)
	result.SourceRuntimeEpoch = uint64(runtimeEpoch)
	result.SourcePlannerStateVersion = uint64(plannerVersion)
	result.TripRevision = uint64(tripRevision)
	result.CoveredFinalizedMutationSequence = uint64(coveredSequence)
	copy(result.Checksum[:], checksum)
	result.Duplicate = duplicate
	result.declaredPayloadSize = payloadSize
	return result, nil
}

const snapshotColumns = `
	id::text,
	trip_id::text,
	snapshot_schema_version,
	source_runtime_epoch,
	source_planner_state_version,
	trip_revision,
	covered_finalized_mutation_sequence,
	payload_size_bytes,
	checksum_sha256,
	payload,
	created_at
`

func (store *SnapshotStore) Persist(
	ctx context.Context,
	blob SnapshotBlob,
) (StoredSnapshot, error) {
	if err := validateSnapshotBlob(blob); err != nil {
		return StoredSnapshot{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("begin snapshot persistence: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tripRevision int64
	var finalizedSequence int64
	err = tx.QueryRow(ctx, `
		SELECT trip_revision, finalized_mutation_sequence
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, blob.TripID).Scan(&tripRevision, &finalizedSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredSnapshot{}, ErrTripNotFound
	}
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("lock snapshot trip: %w", err)
	}

	existing, err := scanStoredSnapshot(tx.QueryRow(ctx, `
		SELECT `+snapshotColumns+`
		FROM planner_snapshots
		WHERE trip_id = $1
		  AND source_runtime_epoch = $2
		  AND source_planner_state_version = $3
		  AND covered_finalized_mutation_sequence = $4
		FOR UPDATE
	`, blob.TripID, int64(blob.SourceRuntimeEpoch),
		int64(blob.SourcePlannerStateVersion),
		int64(blob.CoveredFinalizedMutationSequence)), true)
	if err == nil {
		if existing.ID != blob.ID ||
			existing.TripRevision != blob.TripRevision ||
			existing.SchemaVersion != blob.SchemaVersion ||
			subtle.ConstantTimeCompare(
				existing.Checksum[:],
				blob.Checksum[:],
			) != 1 ||
			string(existing.Payload) != string(blob.Payload) {
			return StoredSnapshot{}, ErrSnapshotConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return StoredSnapshot{},
				fmt.Errorf("commit duplicate snapshot: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return StoredSnapshot{}, fmt.Errorf("find duplicate snapshot: %w", err)
	}

	if blob.TripRevision > uint64(tripRevision) ||
		blob.CoveredFinalizedMutationSequence > uint64(finalizedSequence) {
		return StoredSnapshot{}, fmt.Errorf(
			"%w: snapshot is ahead of PostgreSQL",
			ErrSnapshotInvalid,
		)
	}
	var unresolvedOutbox int64
	err = tx.QueryRow(ctx, `
		SELECT count(*)
		FROM planner_outbox
		WHERE trip_id = $1
		  AND mutation_sequence <= $2
		  AND (
		    delivery_state NOT IN ('accepted', 'terminal_rejected') OR
		    finalization_confirmed_at IS NULL
		  )
	`, blob.TripID, int64(blob.CoveredFinalizedMutationSequence)).Scan(
		&unresolvedOutbox)
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("check finalized outbox coverage: %w", err)
	}
	if unresolvedOutbox != 0 {
		return StoredSnapshot{}, ErrSnapshotNotReady
	}

	rows, err := tx.Query(ctx, `
		SELECT `+snapshotColumns+`
		FROM planner_snapshots
		WHERE trip_id = $1
		  AND invalidated_at IS NULL
		  AND snapshot_schema_version = 1
		ORDER BY id
		FOR UPDATE
	`, blob.TripID)
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("lock stored snapshots: %w", err)
	}
	var validSnapshots []StoredSnapshot
	type invalidSnapshot struct {
		id     string
		reason string
	}
	var invalidSnapshots []invalidSnapshot
	for rows.Next() {
		candidate, err := scanStoredSnapshot(rows, false)
		if err != nil {
			rows.Close()
			return StoredSnapshot{}, fmt.Errorf("scan stored snapshot: %w", err)
		}
		if reason := storedSnapshotValidationReason(candidate); reason != "" {
			invalidSnapshots = append(invalidSnapshots, invalidSnapshot{
				id:     candidate.ID,
				reason: reason,
			})
		} else {
			validSnapshots = append(validSnapshots, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return StoredSnapshot{}, fmt.Errorf("iterate stored snapshots: %w", err)
	}
	rows.Close()
	sort.Slice(validSnapshots, func(left, right int) bool {
		return snapshotIsNewer(
			validSnapshots[left],
			validSnapshots[right],
		)
	})

	var databaseTime time.Time
	err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseTime)
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("read snapshot time: %w", err)
	}
	for _, invalid := range invalidSnapshots {
		_, err = tx.Exec(ctx, `
			UPDATE planner_snapshots
			SET invalidated_at = $3,
			    invalidation_reason = $4
			WHERE id = $1
			  AND trip_id = $2
			  AND invalidated_at IS NULL
		`, invalid.id, blob.TripID, databaseTime, invalid.reason)
		if err != nil {
			return StoredSnapshot{},
				fmt.Errorf("invalidate stored snapshot: %w", err)
		}
	}
	if len(validSnapshots) > 0 &&
		(snapshotIsOlder(blob, validSnapshots[0].SnapshotBlob) ||
			blob.TripRevision < validSnapshots[0].TripRevision ||
			blob.CoveredFinalizedMutationSequence <
				validSnapshots[0].CoveredFinalizedMutationSequence) {
		return StoredSnapshot{}, ErrSnapshotStale
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO planner_snapshots (
			id, trip_id, snapshot_schema_version, source_runtime_epoch,
			source_planner_state_version, trip_revision,
			covered_finalized_mutation_sequence, payload_size_bytes,
			checksum_sha256, payload, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, blob.ID, blob.TripID, int32(blob.SchemaVersion),
		int64(blob.SourceRuntimeEpoch),
		int64(blob.SourcePlannerStateVersion),
		int64(blob.TripRevision),
		int64(blob.CoveredFinalizedMutationSequence),
		len(blob.Payload), blob.Checksum[:], blob.Payload, databaseTime)
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("insert snapshot: %w", err)
	}

	_, err = tx.Exec(ctx, `
		DELETE FROM planner_snapshots
		WHERE id IN (
			SELECT id
			FROM planner_snapshots
			WHERE trip_id = $1
			  AND invalidated_at IS NULL
			  AND snapshot_schema_version = 1
			ORDER BY source_runtime_epoch DESC,
			         source_planner_state_version DESC,
			         covered_finalized_mutation_sequence DESC,
			         trip_revision DESC,
			         created_at DESC,
			         id DESC
			OFFSET 2
		)
	`, blob.TripID)
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("retain newest snapshots: %w", err)
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM planner_outbox
		WHERE trip_id = $1
		  AND mutation_sequence <= $2
		  AND delivery_state IN ('accepted', 'terminal_rejected')
	`, blob.TripID, int64(blob.CoveredFinalizedMutationSequence))
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("prune covered outbox: %w", err)
	}

	result := StoredSnapshot{
		SnapshotBlob:        blob,
		CreatedAt:           databaseTime,
		declaredPayloadSize: int32(len(blob.Payload)),
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredSnapshot{}, fmt.Errorf("commit snapshot: %w", err)
	}
	return result, nil
}

func storedSnapshotValidationReason(snapshot StoredSnapshot) string {
	if len(snapshot.Payload) == 0 ||
		len(snapshot.Payload) > MaxSnapshotPayloadBytes ||
		snapshot.declaredPayloadSize != int32(len(snapshot.Payload)) {
		return "payload_size_invalid"
	}
	checksum := sha256.Sum256(snapshot.Payload)
	if subtle.ConstantTimeCompare(
		checksum[:],
		snapshot.Checksum[:],
	) != 1 {
		return "checksum_mismatch"
	}
	payload, err := parseSnapshotPayload(snapshot.Payload)
	if err != nil {
		return "payload_invalid"
	}
	if payload.tripID != snapshot.TripID ||
		payload.tripRevision != snapshot.TripRevision ||
		payload.finalizedMutationSequence !=
			snapshot.CoveredFinalizedMutationSequence {
		return "metadata_mismatch"
	}
	return ""
}

func (store *SnapshotStore) invalidate(
	ctx context.Context,
	snapshot StoredSnapshot,
	reason string,
) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("begin snapshot invalidation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	err = tx.QueryRow(ctx, `
		SELECT true
		FROM trips
		WHERE id = $1
		FOR UPDATE
	`, snapshot.TripID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFound
	}
	if err != nil {
		return fmt.Errorf("lock invalid snapshot trip: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE planner_snapshots
		SET invalidated_at = clock_timestamp(),
		    invalidation_reason = $3
		WHERE id = $1
		  AND trip_id = $2
		  AND invalidated_at IS NULL
	`, snapshot.ID, snapshot.TripID, reason)
	if err != nil {
		return fmt.Errorf("invalidate snapshot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit snapshot invalidation: %w", err)
	}
	return nil
}

func (store *SnapshotStore) LoadForRecovery(
	ctx context.Context,
	tripID string,
) (StoredSnapshot, error) {
	if !validCanonicalUUID(tripID) {
		return StoredSnapshot{}, errors.New("trip id is invalid")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT `+snapshotColumns+`
		FROM planner_snapshots
		WHERE trip_id = $1
		  AND invalidated_at IS NULL
		  AND snapshot_schema_version = 1
		ORDER BY source_runtime_epoch DESC,
		         source_planner_state_version DESC,
		         covered_finalized_mutation_sequence DESC,
		         trip_revision DESC,
		         created_at DESC,
		         id DESC
	`, tripID)
	if err != nil {
		return StoredSnapshot{}, fmt.Errorf("load recovery snapshots: %w", err)
	}
	var candidates []StoredSnapshot
	for rows.Next() {
		snapshot, err := scanStoredSnapshot(rows, false)
		if err != nil {
			rows.Close()
			return StoredSnapshot{}, fmt.Errorf("scan recovery snapshot: %w", err)
		}
		candidates = append(candidates, snapshot)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return StoredSnapshot{}, fmt.Errorf("iterate recovery snapshots: %w", err)
	}
	rows.Close()

	for _, candidate := range candidates {
		reason := storedSnapshotValidationReason(candidate)
		if reason == "" {
			return candidate, nil
		}
		if err := store.invalidate(ctx, candidate, reason); err != nil {
			return StoredSnapshot{}, err
		}
	}
	return StoredSnapshot{}, ErrSnapshotNotFound
}
