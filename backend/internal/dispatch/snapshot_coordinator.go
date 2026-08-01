package dispatch

import (
	"context"
	"errors"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannertransport"
)

type snapshotPersistence interface {
	Persist(context.Context, persistence.SnapshotBlob) (persistence.StoredSnapshot, error)
}

type SnapshotCoordinator struct {
	holderID string
	timeout  time.Duration
	leases   leaseStore
	planner  plannerClient
	store    snapshotPersistence
	now      func() time.Time
}

func NewSnapshotCoordinator(
	holderID string,
	timeout time.Duration,
	leases leaseStore,
	planner plannerClient,
	store snapshotPersistence,
) (*SnapshotCoordinator, error) {
	if !canonicalUUID(holderID) || timeout <= 0 || leases == nil || planner == nil || store == nil {
		return nil, errors.New("invalid snapshot coordinator configuration")
	}
	return &SnapshotCoordinator{
		holderID: holderID, timeout: timeout, leases: leases,
		planner: planner, store: store, now: time.Now,
	}, nil
}

func (coordinator *SnapshotCoordinator) Capture(
	ctx context.Context,
	tripID string,
	reason liveroutev1.SnapshotReason,
	minimumFinalizedMutationSequence uint64,
	minimumPlannerStateVersion uint64,
) (persistence.StoredSnapshot, error) {
	if !canonicalUUID(tripID) ||
		reason < liveroutev1.SnapshotReason_SNAPSHOT_REASON_PERIODIC ||
		reason > liveroutev1.SnapshotReason_SNAPSHOT_REASON_SHUTDOWN {
		return persistence.StoredSnapshot{}, errors.New("invalid snapshot request")
	}
	lease, err := coordinator.leases.Current(ctx, tripID, coordinator.holderID)
	if err != nil {
		return persistence.StoredSnapshot{}, err
	}
	requestID, err := plannertransport.NewRequestID()
	if err != nil {
		return persistence.StoredSnapshot{}, err
	}
	request := &liveroutev1.PlannerStreamRequest{
		RequestId: requestID, TripId: tripID, RuntimeEpoch: lease.RuntimeEpoch,
		ExpiresAtUnixMs: coordinator.now().Add(coordinator.timeout).UnixMilli(),
		Payload: &liveroutev1.PlannerStreamRequest_RequestSnapshot{
			RequestSnapshot: &liveroutev1.RequestSnapshot{
				Reason:                           reason,
				MinimumFinalizedMutationSequence: minimumFinalizedMutationSequence,
				MinimumPlannerStateVersion:       minimumPlannerStateVersion,
			},
		},
	}
	attemptContext, cancel := context.WithTimeout(ctx, coordinator.timeout)
	response, err := coordinator.planner.Exchange(attemptContext, request)
	cancel()
	if err != nil {
		return persistence.StoredSnapshot{}, err
	}
	result := response.GetTripSnapshot()
	if response.GetRequestId() != requestID || response.GetTripId() != tripID ||
		response.GetRuntimeEpoch() != lease.RuntimeEpoch || result == nil {
		return persistence.StoredSnapshot{}, errors.New("snapshot response is not correlated")
	}
	if result.GetStatus() == liveroutev1.StatusCode_STATUS_CODE_SNAPSHOT_NOT_READY {
		return persistence.StoredSnapshot{}, persistence.ErrSnapshotNotReady
	}
	if result.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_OK || result.GetRetryable() {
		return persistence.StoredSnapshot{}, errors.New("planner rejected snapshot request")
	}
	blob := result.GetSnapshot()
	if blob == nil || len(blob.GetChecksumSha256()) != 32 ||
		blob.GetPayloadSizeBytes() != uint32(len(blob.GetPayload())) ||
		blob.GetSourceRuntimeEpoch() != response.GetRuntimeEpoch() ||
		blob.GetSourcePlannerStateVersion() != response.GetPlannerStateVersion() ||
		blob.GetTripRevision() != response.GetTripRevision() ||
		blob.GetCoveredFinalizedMutationSequence() < minimumFinalizedMutationSequence ||
		blob.GetSourcePlannerStateVersion() < minimumPlannerStateVersion {
		return persistence.StoredSnapshot{}, errors.New("snapshot metadata conflicts with response")
	}
	var checksum [32]byte
	copy(checksum[:], blob.GetChecksumSha256())
	return coordinator.store.Persist(ctx, persistence.SnapshotBlob{
		ID: requestID, TripID: tripID, SchemaVersion: blob.GetSnapshotSchemaVersion(),
		SourceRuntimeEpoch:               blob.GetSourceRuntimeEpoch(),
		SourcePlannerStateVersion:        blob.GetSourcePlannerStateVersion(),
		TripRevision:                     blob.GetTripRevision(),
		CoveredFinalizedMutationSequence: blob.GetCoveredFinalizedMutationSequence(),
		Payload:                          append([]byte(nil), blob.GetPayload()...), Checksum: checksum,
	})
}
