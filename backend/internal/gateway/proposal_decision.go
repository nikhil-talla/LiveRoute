package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
	"google.golang.org/protobuf/proto"
)

type proposalDecisionPreparer interface {
	PrepareProposalAcceptance(context.Context, string, string) (persistence.ProposalAcceptancePreparation, error)
}

type ProposalDecisionAdapter struct {
	preparer proposalDecisionPreparer
	recorder runtimeMutationRecorder
}

func NewProposalDecisionAdapter(
	preparer proposalDecisionPreparer,
	recorder runtimeMutationRecorder,
) (*ProposalDecisionAdapter, error) {
	if preparer == nil || recorder == nil {
		return nil, errors.New("proposal decision stores are required")
	}
	return &ProposalDecisionAdapter{preparer: preparer, recorder: recorder}, nil
}

type proposalDecisionCommand struct {
	ProposalID                string `json:"proposal_id"`
	SourceRuntimeEpoch        string `json:"source_runtime_epoch"`
	SourcePlannerStateVersion string `json:"source_planner_state_version"`
	BaseCurrentPlanID         string `json:"base_current_plan_id"`
}

func (adapter *ProposalDecisionAdapter) Handle(
	ctx context.Context,
	message AuthenticatedMessage,
) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("proposal decision context is required")
	}
	canonicalPayload, err := canonicalizeClientMessage(message.Raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize proposal decision: %w", err)
	}
	var envelope runtimeMutationEnvelope
	if err := json.Unmarshal(message.Raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode proposal decision: %w", err)
	}
	if envelope.ProtocolVersion != protocolVersion || envelope.Kind != "trip_command" ||
		envelope.MessageID == "" || envelope.TripID == "" || message.UserID == "" {
		return nil, errors.New("proposal decision envelope identity is invalid")
	}
	var command proposalDecisionCommand
	if err := json.Unmarshal(envelope.Payload.Command, &command); err != nil {
		return nil, fmt.Errorf("decode proposal decision identity: %w", err)
	}
	runtimeEpoch, err := strconv.ParseUint(command.SourceRuntimeEpoch, 10, 64)
	if err != nil {
		return nil, errors.New("proposal runtime epoch is invalid")
	}
	plannerVersion, err := strconv.ParseUint(command.SourcePlannerStateVersion, 10, 64)
	if err != nil {
		return nil, errors.New("proposal planner state version is invalid")
	}
	if !canonicalUUID(command.ProposalID) || !canonicalUUID(command.BaseCurrentPlanID) || runtimeEpoch == 0 {
		return nil, errors.New("proposal decision identity is invalid")
	}
	kind := persistence.CommandKind(envelope.Payload.CommandKind)
	if kind != persistence.CommandAcceptProposal && kind != persistence.CommandRejectProposal {
		return nil, errors.New("command is not a proposal decision")
	}
	preparation, err := adapter.preparer.PrepareProposalAcceptance(ctx, envelope.TripID, command.ProposalID)
	if err != nil {
		return nil, err
	}
	if preparation.Source.RuntimeEpoch != runtimeEpoch ||
		preparation.Source.PlannerStateVersion != plannerVersion ||
		preparation.Source.BaseCurrentPlanID != command.BaseCurrentPlanID {
		return nil, persistence.ErrProposalStale
	}
	var decision liveroutev1.PlanDecision
	var planned *persistence.PlannedCurrentPlan
	if kind == persistence.CommandAcceptProposal {
		decision = liveroutev1.PlanDecision_PLAN_DECISION_ACCEPT
		planned, err = plannedAcceptedCurrentPlan(preparation, command.ProposalID)
	} else if kind == persistence.CommandRejectProposal {
		decision = liveroutev1.PlanDecision_PLAN_DECISION_REJECT
	}
	if err != nil {
		return nil, err
	}
	expiresAt, err := runtimeCommandExpiry(envelope.Payload.CommandExpiresAtUnixMS)
	if err != nil {
		return nil, err
	}
	eventPayload, err := plannerwire.EncodeStoredEvent(&liveroutev1.ApplyTripEvent{
		EventId: envelope.MessageID, OccurredAtUnixMs: time.Now().UTC().UnixMilli(),
		CommandExpiresAtUnixMs: envelope.Payload.CommandExpiresAtUnixMS,
		Event: &liveroutev1.ApplyTripEvent_PlanDecision{PlanDecision: &liveroutev1.PlanDecisionEvent{
			Decision: decision, ProposalId: command.ProposalID,
			SourceRuntimeEpoch: runtimeEpoch, SourcePlannerStateVersion: plannerVersion,
			BaseCurrentPlanId: command.BaseCurrentPlanID,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("encode proposal decision event: %w", err)
	}
	digest := sha256.Sum256(canonicalPayload)
	result, err := adapter.record(ctx, envelope, message.UserID, preparation.Source.TripRevision, kind, expiresAt, digest, canonicalPayload, eventPayload, planned)
	if err != nil {
		return nil, err
	}
	return runtimeDurableAcknowledgement(envelope, result)
}

func (adapter *ProposalDecisionAdapter) Publish(
	ctx context.Context,
	message AuthenticatedMessage,
) error {
	if message.Sink == nil {
		return errors.New("authenticated message sink is required")
	}
	acknowledgement, err := adapter.Handle(ctx, message)
	if err != nil {
		return err
	}
	return message.Sink.PublishServerEnvelope(acknowledgement)
}

func (adapter *ProposalDecisionAdapter) record(
	ctx context.Context,
	envelope runtimeMutationEnvelope,
	ownerUserID string,
	expectedRevision uint64,
	kind persistence.CommandKind,
	expiresAt *time.Time,
	digest [32]byte,
	commandPayload json.RawMessage,
	eventPayload json.RawMessage,
	planned *persistence.PlannedCurrentPlan,
) (persistence.RecordedCommand, error) {
	return adapter.recorder.RecordRuntimeFirst(ctx, persistence.RecordRuntimeCommandRequest{
		IntentID: newUUID(), OutboxID: newUUID(), TripID: envelope.TripID,
		OwnerUserID: ownerUserID, MessageID: envelope.MessageID, EventID: envelope.MessageID,
		ExpectedTripRevision: expectedRevision, Kind: kind, CommandExpiresAt: expiresAt,
		PayloadDigest: digest, CommandPayload: commandPayload, EventPayload: eventPayload,
		PlannedCurrentPlan: planned,
	})
}

func plannedAcceptedCurrentPlan(
	preparation persistence.ProposalAcceptancePreparation,
	proposalID string,
) (*persistence.PlannedCurrentPlan, error) {
	stored := &liveroutev1.StoredPlanProposal{}
	if err := proto.Unmarshal(preparation.Payload, stored); err != nil {
		return nil, fmt.Errorf("decode stored proposal: %w", err)
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(stored)
	if err != nil || !bytes.Equal(canonical, preparation.Payload) {
		return nil, errors.New("stored proposal bytes are not deterministic")
	}
	proposal := stored.GetProposal()
	if proposal == nil || proposal.GetProposalId() != proposalID {
		return nil, errors.New("stored proposal identity is invalid")
	}
	segments := make([]*liveroutev1.CurrentPlanSegment, 0,
		len(proposal.GetPreservedPrefix())+len(proposal.GetRevisedSuffix()))
	for _, segment := range append(proposal.GetPreservedPrefix(), proposal.GetRevisedSuffix()...) {
		converted := &liveroutev1.CurrentPlanSegment{ActivityId: segment.GetActivityId()}
		if segment.GetDisposition() == liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_SKIPPED {
			converted.State = liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_OMITTED
		} else {
			if segment.ScheduledStartUnixMs == nil || segment.ScheduledEndUnixMs == nil {
				return nil, errors.New("stored proposal scheduled segment is incomplete")
			}
			converted.State = liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_SCHEDULED
			converted.ScheduledStartUnixMs = segment.ScheduledStartUnixMs
			converted.ScheduledEndUnixMs = segment.ScheduledEndUnixMs
		}
		segments = append(segments, converted)
	}
	plan := &liveroutev1.CurrentPlan{
		PlanId: newUUID(), PlanRevision: preparation.NextPlanRevision,
		Origin:   liveroutev1.PlanOrigin_PLAN_ORIGIN_ACCEPTED_ENGINE_PROPOSAL,
		Segments: segments, CreatedAtUnixMs: preparation.CreatedAt.UnixMilli(),
		SourceProposalId: &proposalID,
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("encode accepted current plan: %w", err)
	}
	checksum := sha256.Sum256(payload)
	return &persistence.PlannedCurrentPlan{ID: plan.GetPlanId(), Payload: payload, Checksum: checksum}, nil
}
