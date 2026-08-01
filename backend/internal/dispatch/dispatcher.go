package dispatch

import (
	"context"
	"errors"
	"strings"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"github.com/liveroute/liveroute/backend/internal/plannertransport"
	"github.com/liveroute/liveroute/backend/internal/plannerwire"
)

type outboxStore interface {
	ClaimDue(context.Context, string, int, time.Duration) ([]persistence.ClaimedOutboxRow, error)
	ReleaseForRetry(context.Context, persistence.ClaimedOutboxRow, string, string, time.Duration) error
	PauseInternal(context.Context, persistence.ClaimedOutboxRow, string, string) error
	PendingFinalizationConfirmations(context.Context, int) ([]persistence.PendingFinalizationConfirmation, error)
}

type leaseStore interface {
	Current(context.Context, string, string) (persistence.RuntimeLease, error)
}

type plannerClient interface {
	Exchange(context.Context, *liveroutev1.PlannerStreamRequest) (*liveroutev1.PlannerStreamResponse, error)
}

type mirrorFinalizer interface {
	FinalizeCanonicalMirror(context.Context, persistence.FinalizeCanonicalMirrorRequest) (persistence.FinalizedCanonicalMirror, error)
}

type runtimeOutcomeFinalizer interface {
	Finalize(context.Context, persistence.ClaimedOutboxRow, *liveroutev1.ApplyTripEvent, *liveroutev1.PlannerStreamResponse, *liveroutev1.EventAcknowledged) error
}

type confirmationStore interface {
	ConfirmFinalizedMutations(context.Context, persistence.ConfirmFinalizedMutationsRequest) (persistence.ConfirmedFinalizedMutations, error)
}

type Config struct {
	ClaimOwner     string
	LeaseHolder    string
	BatchSize      int
	ClaimDuration  time.Duration
	AttemptTimeout time.Duration
}

type Dispatcher struct {
	config        Config
	outbox        outboxStore
	leases        leaseStore
	planner       plannerClient
	mirrors       mirrorFinalizer
	runtime       runtimeOutcomeFinalizer
	confirmations confirmationStore
	now           func() time.Time
	retryDelay    func(uint64) (time.Duration, error)
}

// Run continuously services durable outbox work at a bounded polling cadence.
// Delivery errors are reported and retried on the next pass; they do not stop
// the process or discard durable rows. The caller owns shutdown and logging.
func (dispatcher *Dispatcher) Run(
	ctx context.Context,
	interval time.Duration,
	reportError func(error),
) error {
	if ctx == nil || interval <= 0 {
		return errors.New("dispatcher context and positive interval are required")
	}
	if reportError == nil {
		reportError = func(error) {}
	}
	for {
		if _, err := dispatcher.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			reportError(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func New(
	config Config,
	outbox outboxStore,
	leases leaseStore,
	planner plannerClient,
	mirrors mirrorFinalizer,
	runtime runtimeOutcomeFinalizer,
	confirmations confirmationStore,
) (*Dispatcher, error) {
	if config.ClaimOwner == "" || config.LeaseHolder == "" ||
		config.BatchSize <= 0 || config.ClaimDuration <= 0 ||
		config.AttemptTimeout <= 0 || config.AttemptTimeout > config.ClaimDuration ||
		outbox == nil || leases == nil || planner == nil || mirrors == nil || runtime == nil ||
		confirmations == nil {
		return nil, errors.New("invalid dispatcher configuration")
	}
	return &Dispatcher{
		config:        config,
		outbox:        outbox,
		leases:        leases,
		planner:       planner,
		mirrors:       mirrors,
		runtime:       runtime,
		confirmations: confirmations,
		now:           time.Now,
		retryDelay: func(attempt uint64) (time.Duration, error) {
			return persistence.RetryDelay(attempt, nil)
		},
	}, nil
}

// RunOnce recovers missing cumulative confirmations before claiming a bounded
// batch. It returns the number of delivery rows resolved during this pass.
func (dispatcher *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	if err := dispatcher.recoverConfirmations(ctx); err != nil {
		return 0, err
	}
	rows, err := dispatcher.outbox.ClaimDue(
		ctx,
		dispatcher.config.ClaimOwner,
		dispatcher.config.BatchSize,
		dispatcher.config.ClaimDuration,
	)
	if err != nil {
		return 0, err
	}
	resolved := 0
	var failures []error
	for _, row := range rows {
		complete, err := dispatcher.dispatch(ctx, row)
		if complete {
			resolved++
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return resolved, errors.Join(failures...)
}

func (dispatcher *Dispatcher) dispatch(
	ctx context.Context,
	row persistence.ClaimedOutboxRow,
) (bool, error) {
	if row.EventSchemaVersion != 1 ||
		(row.ApplicationOrder != "canonical_first" && row.ApplicationOrder != "runtime_first") {
		return false, dispatcher.pause(ctx, row, "INVALID_STORED_EVENT")
	}
	event, err := plannerwire.DecodeStoredEvent(row.EventPayload)
	if err != nil || event.GetEventId() != row.EventID ||
		!eventMatchesCommand(row.CommandKind, event) {
		return false, dispatcher.pause(ctx, row, "INVALID_STORED_EVENT")
	}
	if row.ApplicationOrder == "canonical_first" &&
		(row.ResultingTripRevision == 0 || row.ResultingCurrentPlanID == "" ||
			(row.CommandKind != persistence.CommandTripEdited &&
				row.CommandKind != persistence.CommandReplaceCurrentPlan)) {
		return false, dispatcher.pause(ctx, row, "INVALID_STORED_EVENT")
	}
	lease, err := dispatcher.leases.Current(ctx, row.TripID, dispatcher.config.LeaseHolder)
	if err != nil {
		return false, dispatcher.retry(ctx, row, "UNAVAILABLE")
	}
	requestID, err := plannertransport.NewRequestID()
	if err != nil {
		return false, dispatcher.retry(ctx, row, "INTERNAL")
	}
	expectedRevision := row.ExpectedTripRevision
	var expectedPlannerStateVersion *uint64
	if decision := event.GetPlanDecision(); decision != nil {
		value := decision.GetSourcePlannerStateVersion()
		expectedPlannerStateVersion = &value
	}
	request := &liveroutev1.PlannerStreamRequest{
		RequestId:                   requestID,
		TripId:                      row.TripID,
		RuntimeEpoch:                lease.RuntimeEpoch,
		MutationSequence:            row.MutationSequence,
		ExpectedTripRevision:        &expectedRevision,
		ExpectedPlannerStateVersion: expectedPlannerStateVersion,
		ExpiresAtUnixMs:             dispatcher.now().Add(dispatcher.config.AttemptTimeout).UnixMilli(),
		Payload:                     &liveroutev1.PlannerStreamRequest_ApplyEvent{ApplyEvent: event},
	}
	attemptContext, cancel := context.WithTimeout(ctx, dispatcher.config.AttemptTimeout)
	response, err := dispatcher.planner.Exchange(attemptContext, request)
	cancel()
	if err != nil {
		return false, dispatcher.retry(ctx, row, "UNAVAILABLE")
	}
	ack := response.GetEventAcknowledged()
	if ack == nil {
		if plannerError := response.GetError(); plannerError != nil && plannerError.GetRetryable() {
			return false, dispatcher.retry(ctx, row, statusName(plannerError.GetStatus()))
		}
		return false, dispatcher.pause(ctx, row, "INTERNAL")
	}
	accepted := ack.GetDisposition() == liveroutev1.EventDisposition_EVENT_DISPOSITION_ACCEPTED ||
		ack.GetDisposition() == liveroutev1.EventDisposition_EVENT_DISPOSITION_DUPLICATE
	acceptedStatus := ack.GetStatus() == liveroutev1.StatusCode_STATUS_CODE_OK ||
		ack.GetStatus() == liveroutev1.StatusCode_STATUS_CODE_DUPLICATE
	if row.ApplicationOrder == "canonical_first" && (!accepted || !acceptedStatus) {
		if ack.GetRetryable() {
			return false, dispatcher.retry(ctx, row, statusName(ack.GetStatus()))
		}
		return false, dispatcher.pause(ctx, row, "INTERNAL")
	}
	if row.ApplicationOrder == "runtime_first" && ack.GetRetryable() {
		return false, dispatcher.retry(ctx, row, statusName(ack.GetStatus()))
	}
	if row.ApplicationOrder == "runtime_first" && !acceptedStatus {
		if _, terminal := terminalStatus(ack.GetStatus()); !terminal ||
			ack.GetDisposition() == liveroutev1.EventDisposition_EVENT_DISPOSITION_UNSPECIFIED {
			return false, dispatcher.pause(ctx, row, "INTERNAL")
		}
	}
	if response.GetRequestId() != requestID || response.GetTripId() != row.TripID ||
		response.GetRuntimeEpoch() != lease.RuntimeEpoch ||
		ack.GetEventId() != row.EventID ||
		ack.GetResolvedMutationSequence() < row.MutationSequence {
		return false, dispatcher.pause(ctx, row, "INTERNAL")
	}
	if row.ApplicationOrder == "runtime_first" {
		if err := dispatcher.runtime.Finalize(ctx, row, event, response, ack); err != nil {
			return false, err
		}
		if err := dispatcher.confirm(ctx, row.TripID, lease.RuntimeEpoch, row.MutationSequence); err != nil {
			return true, err
		}
		return true, nil
	}
	_, err = dispatcher.mirrors.FinalizeCanonicalMirror(ctx, persistence.FinalizeCanonicalMirrorRequest{
		TripID:                 row.TripID,
		IntentID:               row.CommandIntentID,
		OutboxID:               row.ID,
		EventID:                row.EventID,
		MutationSequence:       row.MutationSequence,
		ExpectedTripRevision:   row.ExpectedTripRevision,
		ResultingTripRevision:  response.GetTripRevision(),
		ResultingCurrentPlanID: ack.GetResultingCurrentPlanId(),
	})
	if err != nil {
		return false, err
	}
	if err := dispatcher.confirm(
		ctx, row.TripID, lease.RuntimeEpoch, row.MutationSequence,
	); err != nil {
		// The accepted outbox row is intentionally left unconfirmed. The next
		// RunOnce recovers this exact crash boundary before new claims.
		return true, err
	}
	return true, nil
}

func (dispatcher *Dispatcher) recoverConfirmations(ctx context.Context) error {
	values, err := dispatcher.outbox.PendingFinalizationConfirmations(
		ctx, dispatcher.config.BatchSize,
	)
	if err != nil {
		return err
	}
	for _, value := range values {
		lease, err := dispatcher.leases.Current(
			ctx, value.TripID, dispatcher.config.LeaseHolder,
		)
		if err != nil {
			continue
		}
		if err := dispatcher.confirm(
			ctx, value.TripID, lease.RuntimeEpoch,
			value.FinalizedMutationSequence,
		); err != nil {
			return err
		}
	}
	return nil
}

func (dispatcher *Dispatcher) confirm(
	ctx context.Context,
	tripID string,
	runtimeEpoch uint64,
	sequence uint64,
) error {
	requestID, err := plannertransport.NewRequestID()
	if err != nil {
		return err
	}
	request := &liveroutev1.PlannerStreamRequest{
		RequestId:       requestID,
		TripId:          tripID,
		RuntimeEpoch:    runtimeEpoch,
		ExpiresAtUnixMs: dispatcher.now().Add(dispatcher.config.AttemptTimeout).UnixMilli(),
		Payload: &liveroutev1.PlannerStreamRequest_ConfirmFinalizedMutations{
			ConfirmFinalizedMutations: &liveroutev1.ConfirmFinalizedMutations{
				FinalizedMutationSequence: sequence,
			},
		},
	}
	attemptContext, cancel := context.WithTimeout(ctx, dispatcher.config.AttemptTimeout)
	response, err := dispatcher.planner.Exchange(attemptContext, request)
	cancel()
	if err != nil {
		return err
	}
	ack := response.GetFinalizedMutationsAcknowledged()
	if response.GetRequestId() != requestID || response.GetTripId() != tripID ||
		response.GetRuntimeEpoch() != runtimeEpoch || ack == nil ||
		(ack.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_OK &&
			ack.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_DUPLICATE) ||
		ack.GetFinalizedMutationSequence() < sequence {
		return errors.New("planner finalization confirmation conflicts with request")
	}
	_, err = dispatcher.confirmations.ConfirmFinalizedMutations(
		ctx,
		persistence.ConfirmFinalizedMutationsRequest{
			TripID: tripID, FinalizedMutationSequence: sequence,
		},
	)
	return err
}

func (dispatcher *Dispatcher) retry(
	ctx context.Context,
	row persistence.ClaimedOutboxRow,
	status string,
) error {
	delay, err := dispatcher.retryDelay(row.AttemptCount)
	if err != nil {
		return err
	}
	return dispatcher.outbox.ReleaseForRetry(
		ctx, row, dispatcher.config.ClaimOwner, status, delay,
	)
}

func (dispatcher *Dispatcher) pause(
	ctx context.Context,
	row persistence.ClaimedOutboxRow,
	status string,
) error {
	return dispatcher.outbox.PauseInternal(
		ctx, row, dispatcher.config.ClaimOwner, status,
	)
}

func eventMatchesCommand(kind persistence.CommandKind, event *liveroutev1.ApplyTripEvent) bool {
	switch kind {
	case persistence.CommandActivityStatusChanged:
		return event.GetActivityStatusChanged() != nil
	case persistence.CommandActivityDelayed:
		return event.GetActivityDelayed() != nil
	case persistence.CommandReservationChanged:
		return event.GetReservationChanged() != nil
	case persistence.CommandMandatoryDeadlineChanged:
		return event.GetMandatoryDeadlineChanged() != nil
	case persistence.CommandOperatingHoursChanged:
		return event.GetOperatingHoursChanged() != nil
	case persistence.CommandPlaceFoundClosed:
		return event.GetPlaceFoundClosed() != nil
	case persistence.CommandTravelDelay:
		return event.GetTravelDelay() != nil
	case persistence.CommandAcceptProposal:
		return event.GetPlanDecision() != nil &&
			event.GetPlanDecision().GetDecision() == liveroutev1.PlanDecision_PLAN_DECISION_ACCEPT
	case persistence.CommandRejectProposal:
		return event.GetPlanDecision() != nil &&
			event.GetPlanDecision().GetDecision() == liveroutev1.PlanDecision_PLAN_DECISION_REJECT
	case persistence.CommandTripEdited:
		return event.GetTripEdited() != nil
	case persistence.CommandReplaceCurrentPlan:
		return event.GetCurrentPlanReplaced() != nil
	default:
		return false
	}
}

func statusName(status liveroutev1.StatusCode) string {
	name := strings.TrimPrefix(status.String(), "STATUS_CODE_")
	if name == "" || name == "UNSPECIFIED" {
		return "INTERNAL"
	}
	return name
}
