package dispatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
)

type fakeSnapshotPersistence struct {
	blob persistence.SnapshotBlob
}

func (store *fakeSnapshotPersistence) Persist(
	_ context.Context,
	blob persistence.SnapshotBlob,
) (persistence.StoredSnapshot, error) {
	store.blob = blob
	return persistence.StoredSnapshot{SnapshotBlob: blob}, nil
}

type fakeSnapshotPlanner struct {
	request  *liveroutev1.PlannerStreamRequest
	notReady bool
}

func (planner *fakeSnapshotPlanner) Exchange(
	_ context.Context,
	request *liveroutev1.PlannerStreamRequest,
) (*liveroutev1.PlannerStreamResponse, error) {
	planner.request = request
	if planner.notReady {
		return &liveroutev1.PlannerStreamResponse{
			RequestId: request.GetRequestId(), TripId: request.GetTripId(),
			RuntimeEpoch: request.GetRuntimeEpoch(),
			Payload: &liveroutev1.PlannerStreamResponse_TripSnapshot{
				TripSnapshot: &liveroutev1.TripSnapshot{
					Status: liveroutev1.StatusCode_STATUS_CODE_SNAPSHOT_NOT_READY,
				},
			},
		}, nil
	}
	payload := []byte("snapshot-wire")
	checksum := sha256.Sum256(payload)
	return &liveroutev1.PlannerStreamResponse{
		RequestId: request.GetRequestId(), TripId: request.GetTripId(),
		RuntimeEpoch: request.GetRuntimeEpoch(), PlannerStateVersion: 9, TripRevision: 4,
		Payload: &liveroutev1.PlannerStreamResponse_TripSnapshot{
			TripSnapshot: &liveroutev1.TripSnapshot{
				Status: liveroutev1.StatusCode_STATUS_CODE_OK,
				Snapshot: &liveroutev1.SnapshotBlob{
					SnapshotSchemaVersion: 1, SourceRuntimeEpoch: request.GetRuntimeEpoch(),
					SourcePlannerStateVersion: 9, TripRevision: 4,
					CoveredFinalizedMutationSequence: 6,
					PayloadSizeBytes:                 uint32(len(payload)),
					ChecksumSha256:                   checksum[:], Payload: payload,
				},
			},
		},
	}, nil
}

func TestSnapshotCoordinatorPersistsCorrelatedSnapshot(t *testing.T) {
	planner := &fakeSnapshotPlanner{}
	store := &fakeSnapshotPersistence{}
	coordinator, err := NewSnapshotCoordinator(
		testHolderID, time.Second,
		fakeLeases{}, planner, store,
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := coordinator.Capture(
		context.Background(), testTripID,
		liveroutev1.SnapshotReason_SNAPSHOT_REASON_DURABLE_BOUNDARY, 6, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if planner.request.GetRequestSnapshot() == nil || stored.ID == "" ||
		store.blob.SourcePlannerStateVersion != 9 ||
		store.blob.CoveredFinalizedMutationSequence != 6 {
		t.Fatalf("snapshot was not persisted exactly: request=%+v blob=%+v", planner.request, store.blob)
	}
}

func TestSnapshotCoordinatorPreservesNotReady(t *testing.T) {
	planner := &fakeSnapshotPlanner{notReady: true}
	coordinator, _ := NewSnapshotCoordinator(
		testHolderID, time.Second,
		fakeLeases{}, planner, &fakeSnapshotPersistence{},
	)
	if _, err := coordinator.Capture(
		context.Background(), testTripID,
		liveroutev1.SnapshotReason_SNAPSHOT_REASON_PERIODIC, 6, 8,
	); !errors.Is(err, persistence.ErrSnapshotNotReady) {
		t.Fatalf("snapshot-not-ready status was lost: %v", err)
	}
}
