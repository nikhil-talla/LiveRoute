package gateway

import (
	"testing"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"google.golang.org/protobuf/proto"
)

func TestSubscriptionStatePayloadOmitsCurrentActivityWhenNoneStarted(t *testing.T) {
	createdAt := time.UnixMilli(1_784_000_000_123).UTC()
	state := subscriptionJSONState(createdAt)
	payload, err := SubscriptionStatePayload(state, nil, true, "not_required")
	if err != nil {
		t.Fatal(err)
	}
	trip := payload["trip"].(map[string]any)
	if _, present := trip["current_activity_id"]; present {
		t.Fatal("current_activity_id should be omitted when no activity is started")
	}
	plan := payload["current_plan"].(map[string]any)
	if plan["plan_revision"] != "1" || plan["origin"] != "user_authored" {
		t.Fatalf("unexpected current plan=%v", plan)
	}
}

func TestSubscriptionStatePayloadConvertsDeterministicStoredProposal(t *testing.T) {
	createdAt := time.UnixMilli(1_784_000_000_123).UTC()
	state := subscriptionJSONState(createdAt)
	proposal := &liveroutev1.StoredPlanProposal{
		Proposal: &liveroutev1.PlanProposal{
			ProposalId:         "550e8400-e29b-41d4-a716-446655440014",
			SourceRuntimeEpoch: 2, SourcePlannerStateVersion: 3,
			BaseCurrentPlanId: state.CurrentPlanID, SourceTripRevision: 1,
			SourceAcceptedMutationSequence: 1,
			CreatedAtUnixMs:                createdAt.UnixMilli(),
		},
		Notification: liveroutev1.NotificationType_NOTIFICATION_TYPE_NONE,
		Stats:        &liveroutev1.PlannerStats{},
		Quality: &liveroutev1.ResultQuality{
			PlanQuality:    liveroutev1.PlanQuality_PLAN_QUALITY_COMPLETE,
			RoutingQuality: liveroutev1.RoutingQuality_ROUTING_QUALITY_FRESH,
			RecoveryState:  liveroutev1.RecoveryState_RECOVERY_STATE_CURRENT,
		},
	}
	payloadBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := SubscriptionStatePayload(state, payloadBytes, true, "synced")
	if err != nil {
		t.Fatal(err)
	}
	stored := payload["latest_pending_proposal"].(map[string]any)
	proposalJSON := stored["proposal"].(map[string]any)
	if proposalJSON["source_runtime_epoch"] != "2" || stored["notification"] != "none" {
		t.Fatalf("unexpected proposal=%v", stored)
	}
	quality := stored["quality"].(map[string]any)
	if quality["plan_quality"] != "complete" || quality["recovery_state"] != "current" {
		t.Fatalf("unexpected quality=%v", quality)
	}
}

func subscriptionJSONState(createdAt time.Time) persistence.CanonicalTripState {
	planID := "550e8400-e29b-41d4-a716-446655440013"
	plan, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(&liveroutev1.CurrentPlan{
		PlanId: planID, PlanRevision: 1,
		Origin:          liveroutev1.PlanOrigin_PLAN_ORIGIN_USER_AUTHORED,
		CreatedAtUnixMs: createdAt.UnixMilli(),
	})
	return persistence.CanonicalTripState{
		TripID:              "550e8400-e29b-41d4-a716-446655440002",
		OwnerUserID:         "550e8400-e29b-41d4-a716-446655440005",
		DefaultTimeZoneName: "America/New_York",
		TripRevision:        1, FinalizedMutationSequence: 1,
		CurrentPlanID: planID,
		CurrentPlan: persistence.CanonicalCurrentPlan{
			ID: planID, Revision: 1, Origin: "user_authored", Payload: plan, CreatedAt: createdAt,
		},
	}
}
