package gateway

import (
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"google.golang.org/protobuf/proto"
)

func TestPlannedAcceptedCurrentPlanUsesStoredProposalAndAssignedMetadata(t *testing.T) {
	proposalID := "11111111-1111-4111-8111-111111111111"
	stored := &liveroutev1.StoredPlanProposal{Proposal: &liveroutev1.PlanProposal{
		ProposalId: proposalID,
		PreservedPrefix: []*liveroutev1.ProposalSegment{{
			ActivityId:           "22222222-2222-4222-8222-222222222222",
			Disposition:          liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_PRESERVED,
			ScheduledStartUnixMs: proposalInt64Ptr(100), ScheduledEndUnixMs: proposalInt64Ptr(200),
		}},
		RevisedSuffix: []*liveroutev1.ProposalSegment{{
			ActivityId:  "33333333-3333-4333-8333-333333333333",
			Disposition: liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_SKIPPED,
		}},
	}}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := plannedAcceptedCurrentPlan(persistence.ProposalAcceptancePreparation{
		ProposalID: proposalID, Payload: payload, NextPlanRevision: 9,
		CreatedAt: time.UnixMilli(1_784_000_123_456).UTC(),
	}, proposalID)
	if err != nil {
		t.Fatal(err)
	}
	plan := &liveroutev1.CurrentPlan{}
	if err := proto.Unmarshal(prepared.Payload, plan); err != nil {
		t.Fatal(err)
	}
	if plan.GetPlanId() != prepared.ID || plan.GetPlanRevision() != 9 ||
		plan.GetOrigin() != liveroutev1.PlanOrigin_PLAN_ORIGIN_ACCEPTED_ENGINE_PROPOSAL ||
		plan.GetSourceProposalId() != proposalID || len(plan.GetSegments()) != 2 ||
		plan.GetSegments()[1].GetState() != liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_OMITTED {
		t.Fatalf("unexpected accepted plan: %v", plan)
	}
}

func proposalInt64Ptr(value int64) *int64 { return &value }
