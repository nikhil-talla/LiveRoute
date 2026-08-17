package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
)

type runtimeCommandStore interface {
	FinalizeAcceptedMutation(context.Context, persistence.FinalizeAcceptedMutationRequest) (persistence.FinalizedCommand, error)
	FinalizeProposalAcceptance(context.Context, persistence.FinalizeProposalDecisionRequest) (persistence.FinalizedCommand, error)
	FinalizeProposalRejection(context.Context, persistence.FinalizeProposalDecisionRequest) (persistence.FinalizedCommand, error)
	FinalizeStaleProposalDecision(context.Context, persistence.FinalizeProposalDecisionRequest) (persistence.FinalizedCommand, error)
	FinalizeTerminal(context.Context, persistence.FinalizeTerminalCommandRequest) (persistence.FinalizedCommand, error)
}

type RuntimeFinalizer struct {
	store runtimeCommandStore
}

func NewRuntimeFinalizer(store runtimeCommandStore) (*RuntimeFinalizer, error) {
	if store == nil {
		return nil, errors.New("runtime command store is required")
	}
	return &RuntimeFinalizer{store: store}, nil
}

func (finalizer *RuntimeFinalizer) Finalize(
	ctx context.Context,
	row persistence.ClaimedOutboxRow,
	event *liveroutev1.ApplyTripEvent,
	response *liveroutev1.PlannerStreamResponse,
	ack *liveroutev1.EventAcknowledged,
) (persistence.FinalizedCommand, error) {
	if ack.GetDisposition() == liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED ||
		ack.GetDisposition() == liveroutev1.EventDisposition_EVENT_DISPOSITION_DUPLICATE {
		return finalizer.finalizeAccepted(ctx, row, event, response)
	}
	status, ok := terminalStatus(ack.GetStatus())
	if !ok {
		return persistence.FinalizedCommand{}, errors.New("runtime acknowledgement is not terminal")
	}
	outcome, err := stableOutcome(row.EventID, statusName(ack.GetStatus()), response.GetPlannerStateVersion())
	if err != nil {
		return persistence.FinalizedCommand{}, err
	}
	if decision := event.GetPlanDecision(); decision != nil && ack.GetStatus() == liveroutev1.StatusCode_STATUS_CODE_STALE &&
		ack.GetStaleReason() == liveroutev1.StaleReason_STALE_REASON_PLAN_PROPOSAL {
		result, finalizeErr := finalizer.store.FinalizeStaleProposalDecision(
			ctx, proposalRequest(row, decision, response.GetPlannerStateVersion(), outcome),
		)
		return result, finalizeErr
	}
	return finalizer.store.FinalizeTerminal(ctx, persistence.FinalizeTerminalCommandRequest{
		TripID:                       row.TripID,
		IntentID:                     row.CommandIntentID,
		OutboxID:                     row.ID,
		EventID:                      row.EventID,
		MutationSequence:             row.MutationSequence,
		ExpectedTripRevision:         row.ExpectedTripRevision,
		ResultingPlannerStateVersion: response.GetPlannerStateVersion(),
		Status:                       status,
		OutcomePayload:               outcome,
	})
}

func (finalizer *RuntimeFinalizer) finalizeAccepted(
	ctx context.Context,
	row persistence.ClaimedOutboxRow,
	event *liveroutev1.ApplyTripEvent,
	response *liveroutev1.PlannerStreamResponse,
) (persistence.FinalizedCommand, error) {
	outcome, err := stableOutcome(row.EventID, "OK", response.GetPlannerStateVersion())
	if err != nil {
		return persistence.FinalizedCommand{}, err
	}
	if decision := event.GetPlanDecision(); decision != nil {
		request := proposalRequest(
			row, decision, response.GetPlannerStateVersion(), outcome,
		)
		switch decision.GetDecision() {
		case liveroutev1.PlanDecision_PLAN_DECISION_ACCEPT:
			return finalizer.store.FinalizeProposalAcceptance(ctx, request)
		case liveroutev1.PlanDecision_PLAN_DECISION_REJECT:
			return finalizer.store.FinalizeProposalRejection(ctx, request)
		default:
			return persistence.FinalizedCommand{}, errors.New("accepted proposal decision is invalid")
		}
	}
	mutation, err := acceptedMutation(event)
	if err != nil {
		return persistence.FinalizedCommand{}, err
	}
	return finalizer.store.FinalizeAcceptedMutation(ctx, persistence.FinalizeAcceptedMutationRequest{
		TripID:                       row.TripID,
		IntentID:                     row.CommandIntentID,
		OutboxID:                     row.ID,
		EventID:                      row.EventID,
		MutationSequence:             row.MutationSequence,
		ExpectedTripRevision:         row.ExpectedTripRevision,
		ResultingPlannerStateVersion: response.GetPlannerStateVersion(),
		Mutation:                     mutation,
		OutcomePayload:               outcome,
	})
}

func proposalRequest(
	row persistence.ClaimedOutboxRow,
	decision *liveroutev1.PlanDecisionEvent,
	plannerStateVersion uint64,
	outcome json.RawMessage,
) persistence.FinalizeProposalDecisionRequest {
	return persistence.FinalizeProposalDecisionRequest{
		TripID:                       row.TripID,
		IntentID:                     row.CommandIntentID,
		OutboxID:                     row.ID,
		EventID:                      row.EventID,
		MutationSequence:             row.MutationSequence,
		ExpectedTripRevision:         row.ExpectedTripRevision,
		ResultingPlannerStateVersion: plannerStateVersion,
		Identity: persistence.ProposalDecisionIdentity{
			ProposalID:                decision.GetProposalId(),
			SourceRuntimeEpoch:        decision.GetSourceRuntimeEpoch(),
			SourcePlannerStateVersion: decision.GetSourcePlannerStateVersion(),
			BaseCurrentPlanID:         decision.GetBaseCurrentPlanId(),
		},
		OutcomePayload: outcome,
	}
}

func acceptedMutation(event *liveroutev1.ApplyTripEvent) (persistence.AcceptedMutation, error) {
	switch value := event.GetEvent().(type) {
	case *liveroutev1.ApplyTripEvent_ActivityStatusChanged:
		state := persistence.ActivityState("")
		switch value.ActivityStatusChanged.GetState() {
		case liveroutev1.ActivityState_ACTIVITY_STATE_PLANNED:
			state = persistence.ActivityStatePlanned
		case liveroutev1.ActivityState_ACTIVITY_STATE_STARTED:
			state = persistence.ActivityStateStarted
		case liveroutev1.ActivityState_ACTIVITY_STATE_COMPLETED:
			state = persistence.ActivityStateCompleted
		case liveroutev1.ActivityState_ACTIVITY_STATE_SKIPPED:
			state = persistence.ActivityStateSkipped
		}
		return persistence.AcceptedMutation{ActivityStatus: &persistence.ActivityStatusMutation{
			ActivityID: value.ActivityStatusChanged.GetActivityId(), State: state,
		}}, nil
	case *liveroutev1.ApplyTripEvent_ActivityDelayed:
		return persistence.AcceptedMutation{ActivityDelay: &persistence.ActivityDelayMutation{
			ActivityID:   value.ActivityDelayed.GetActivityId(),
			DelaySeconds: value.ActivityDelayed.GetDelaySeconds(),
		}}, nil
	case *liveroutev1.ApplyTripEvent_ReservationChanged:
		var start *int64
		if value.ReservationChanged.ReservationStartUnixMs != nil {
			copy := value.ReservationChanged.GetReservationStartUnixMs()
			start = &copy
		}
		return persistence.AcceptedMutation{Reservation: &persistence.ReservationMutation{
			ActivityID:                       value.ReservationChanged.GetActivityId(),
			ReservationStartUnixMilliseconds: start,
			ReservationGraceSeconds:          value.ReservationChanged.GetReservationGraceSeconds(),
		}}, nil
	case *liveroutev1.ApplyTripEvent_MandatoryDeadlineChanged:
		return persistence.AcceptedMutation{MandatoryDeadline: &persistence.MandatoryDeadlineMutation{
			ActivityID:                   value.MandatoryDeadlineChanged.GetActivityId(),
			LatestFinishUnixMilliseconds: value.MandatoryDeadlineChanged.GetLatestFinishUnixMs(),
		}}, nil
	case *liveroutev1.ApplyTripEvent_OperatingHoursChanged:
		windows := make([]persistence.OpenWindow, 0, len(value.OperatingHoursChanged.GetOpenWindows()))
		for _, window := range value.OperatingHoursChanged.GetOpenWindows() {
			windows = append(windows, persistence.OpenWindow{
				OpensAtUnixMilliseconds:  window.GetOpensAtUnixMs(),
				ClosesAtUnixMilliseconds: window.GetClosesAtUnixMs(),
			})
		}
		return persistence.AcceptedMutation{OperatingHours: &persistence.OperatingHoursMutation{
			ActivityID: value.OperatingHoursChanged.GetActivityId(), OpenWindows: windows,
		}}, nil
	case *liveroutev1.ApplyTripEvent_PlaceFoundClosed:
		return persistence.AcceptedMutation{PlaceFoundClosed: &persistence.PlaceFoundClosedMutation{
			ActivityID:                 value.PlaceFoundClosed.GetActivityId(),
			ObservedAtUnixMilliseconds: value.PlaceFoundClosed.GetObservedAtUnixMs(),
		}}, nil
	case *liveroutev1.ApplyTripEvent_TravelDelay:
		return persistence.AcceptedMutation{TravelDelay: &persistence.TravelDelayMutation{
			FromActivityID:             value.TravelDelay.GetFromActivityId(),
			ToActivityID:               value.TravelDelay.GetToActivityId(),
			AdditionalSeconds:          value.TravelDelay.GetAdditionalSeconds(),
			ObservedAtUnixMilliseconds: event.GetOccurredAtUnixMs(),
		}}, nil
	default:
		return persistence.AcceptedMutation{}, errors.New("unsupported accepted runtime event")
	}
}

func terminalStatus(status liveroutev1.StatusCode) (persistence.TerminalStatus, bool) {
	switch status {
	case liveroutev1.StatusCode_STATUS_CODE_STALE:
		return persistence.TerminalStatusStale, true
	case liveroutev1.StatusCode_STATUS_CODE_INVALID_ARGUMENT:
		return persistence.TerminalStatusInvalidArgument, true
	case liveroutev1.StatusCode_STATUS_CODE_NOT_FOUND:
		return persistence.TerminalStatusNotFound, true
	case liveroutev1.StatusCode_STATUS_CODE_COMMAND_EXPIRED:
		return persistence.TerminalStatusCommandExpired, true
	case liveroutev1.StatusCode_STATUS_CODE_INFEASIBLE:
		return persistence.TerminalStatusInfeasible, true
	default:
		return "", false
	}
}

func stableOutcome(eventID, status string, plannerStateVersion uint64) (json.RawMessage, error) {
	return json.Marshal(struct {
		EventID             string `json:"event_id"`
		Status              string `json:"status"`
		PlannerStateVersion uint64 `json:"planner_state_version"`
	}{eventID, status, plannerStateVersion})
}
