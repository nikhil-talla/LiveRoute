package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"google.golang.org/protobuf/proto"
)

const (
	testRequestID  = "66666666-6666-4666-8666-666666666666"
	testHolderID   = "77777777-7777-4777-8777-777777777777"
	testProposalID = "88888888-8888-4888-8888-888888888888"
)

type fakeProposalStore struct {
	request persistence.PersistProposalRequest
	result  persistence.PersistedProposal
	err     error
	errs    []error
	calls   int
}

func (store *fakeProposalStore) Persist(
	_ context.Context,
	request persistence.PersistProposalRequest,
) (persistence.PersistedProposal, error) {
	store.request = request
	store.calls++
	if len(store.errs) > 0 {
		err := store.errs[0]
		store.errs = store.errs[1:]
		return store.result, err
	}
	return store.result, store.err
}

func validProposalResponse() *liveroutev1.PlannerStreamResponse {
	start := int64(1_784_000_000_000)
	end := start + 3_600_000
	return &liveroutev1.PlannerStreamResponse{
		RequestId: testRequestID, TripId: testTripID, RuntimeEpoch: 3,
		AcceptedMutationSequence: 5, PlannerStateVersion: 8, TripRevision: 4,
		Payload: &liveroutev1.PlannerStreamResponse_ReplanResult{
			ReplanResult: &liveroutev1.ReplanResult{
				Status: liveroutev1.StatusCode_STATUS_CODE_OK,
				Proposal: &liveroutev1.PlanProposal{
					ProposalId: testProposalID, SourceRuntimeEpoch: 3,
					SourcePlannerStateVersion: 8, BaseCurrentPlanId: testPlanID,
					SourceTripRevision: 4, SourceAcceptedMutationSequence: 5,
					CreatedAtUnixMs: start,
					RevisedSuffix: []*liveroutev1.ProposalSegment{{
						ActivityId: testIntentID,
						Location: &liveroutev1.Location{
							Latitude: 40.7, Longitude: -74,
						},
						TimeZoneName:         "America/New_York",
						ScheduledStartUnixMs: &start, ScheduledEndUnixMs: &end,
						InboundRoute: &liveroutev1.RouteLeg{
							DurationSeconds: 60, DistanceMeters: 100, Reachable: true,
						},
						Disposition: liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_MOVED,
						Reasons: []liveroutev1.PlanReasonCode{
							liveroutev1.PlanReasonCode_PLAN_REASON_CODE_ACTIVITY_DELAY,
						},
					}},
				},
				Notification: liveroutev1.NotificationType_NOTIFICATION_TYPE_PLAN_CHANGE_SUGGESTED,
				Reasons: []liveroutev1.PlanReasonCode{
					liveroutev1.PlanReasonCode_PLAN_REASON_CODE_ACTIVITY_DELAY,
				},
				Stats: &liveroutev1.PlannerStats{CandidatesEvaluated: 2},
				Quality: &liveroutev1.ResultQuality{
					PlanQuality:    liveroutev1.PlanQuality_PLAN_QUALITY_COMPLETE,
					RoutingQuality: liveroutev1.RoutingQuality_ROUTING_QUALITY_FRESH,
					RecoveryState:  liveroutev1.RecoveryState_RECOVERY_STATE_CURRENT,
				},
			},
		},
	}
}

func TestProposalConsumerPersistsExactStoredArtifact(t *testing.T) {
	store := &fakeProposalStore{result: persistence.PersistedProposal{
		ProposalID: testProposalID, Publishable: true,
	}}
	consumer, err := NewProposalConsumer(testHolderID, store)
	if err != nil {
		t.Fatal(err)
	}
	stored, present, err := consumer.Persist(context.Background(), validProposalResponse())
	if err != nil {
		t.Fatal(err)
	}
	if !present || !stored.Publishable || store.request.ProposalID != testProposalID ||
		store.request.Source.PlannerStateVersion != 8 ||
		store.request.Current.HolderID != testHolderID {
		t.Fatalf("unexpected persisted proposal: present=%v stored=%+v request=%+v", present, stored, store.request)
	}
	artifact := &liveroutev1.StoredPlanProposal{}
	if err := proto.Unmarshal(store.request.Payload, artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.GetProposal().GetProposalId() != testProposalID ||
		artifact.GetNotification() != liveroutev1.NotificationType_NOTIFICATION_TYPE_PLAN_CHANGE_SUGGESTED ||
		len(store.request.Checksum) != 32 {
		t.Fatalf("unexpected stored artifact: %+v", artifact)
	}
}

func TestProposalConsumerRejectsEnvelopeMismatch(t *testing.T) {
	store := &fakeProposalStore{}
	consumer, _ := NewProposalConsumer(testHolderID, store)
	response := validProposalResponse()
	response.GetReplanResult().GetProposal().SourcePlannerStateVersion++
	if _, present, err := consumer.Persist(context.Background(), response); err == nil || !present {
		t.Fatalf("expected present invalid proposal, got present=%v err=%v", present, err)
	}
	if store.request.ProposalID != "" {
		t.Fatal("invalid proposal reached persistence")
	}
}

func TestProposalConsumerSilentlyDiscardsStaleResult(t *testing.T) {
	store := &fakeProposalStore{err: persistence.ErrProposalStale}
	consumer, _ := NewProposalConsumer(testHolderID, store)
	if _, present, err := consumer.Persist(context.Background(), validProposalResponse()); err != nil || present {
		t.Fatalf("stale proposal was not discarded: present=%v err=%v", present, err)
	}
}

func TestProposalConsumerRejectsIdentityConflict(t *testing.T) {
	store := &fakeProposalStore{err: persistence.ErrProposalIdentityConflict}
	consumer, _ := NewProposalConsumer(testHolderID, store)
	if _, present, err := consumer.Persist(context.Background(), validProposalResponse()); !present || !errors.Is(err, persistence.ErrProposalIdentityConflict) {
		t.Fatalf("identity conflict was hidden: present=%v err=%v", present, err)
	}
}

func TestProposalConsumerRetainsResultAcrossTransientPersistenceFailure(t *testing.T) {
	store := &fakeProposalStore{
		result: persistence.PersistedProposal{
			ProposalID: testProposalID, Publishable: true,
		},
		errs: []error{errors.New("database temporarily unavailable")},
	}
	consumer, err := NewProposalConsumer(testHolderID, store)
	if err != nil {
		t.Fatal(err)
	}
	consumer.proposalRetryDelay = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer.SetOnStored(func(context.Context, persistence.PersistedProposal, *liveroutev1.PlannerStreamResponse) error {
		cancel()
		return nil
	})
	notifications := make(chan *liveroutev1.PlannerStreamResponse, 1)
	notifications <- validProposalResponse()
	close(notifications)
	if err := consumer.Run(ctx, notifications); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected consumer result: %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("proposal was not retried exactly once: calls=%d", store.calls)
	}
}
