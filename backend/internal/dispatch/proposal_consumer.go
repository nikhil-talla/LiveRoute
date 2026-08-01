package dispatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"google.golang.org/protobuf/proto"
)

type proposalStore interface {
	Persist(context.Context, persistence.PersistProposalRequest) (persistence.PersistedProposal, error)
}

type ProposalConsumer struct {
	holderID string
	store    proposalStore
}

func NewProposalConsumer(holderID string, store proposalStore) (*ProposalConsumer, error) {
	if !canonicalUUID(holderID) || store == nil {
		return nil, errors.New("invalid proposal consumer configuration")
	}
	return &ProposalConsumer{holderID: holderID, store: store}, nil
}

// Run drains the planner client's bounded notification queue on a dedicated
// worker, keeping PostgreSQL work off the stream receive goroutine.
func (consumer *ProposalConsumer) Run(
	ctx context.Context,
	notifications <-chan *liveroutev1.PlannerStreamResponse,
) error {
	if notifications == nil {
		return errors.New("planner notification channel is required")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case response, open := <-notifications:
			if !open {
				return nil
			}
			if _, _, err := consumer.Persist(ctx, response); err != nil {
				return err
			}
		}
	}
}

// Persist validates an unsolicited planning result and stores the exact
// deterministic StoredPlanProposal bytes before any client publication.
// A result without a proposal is a valid no-op.
func (consumer *ProposalConsumer) Persist(
	ctx context.Context,
	response *liveroutev1.PlannerStreamResponse,
) (persistence.PersistedProposal, bool, error) {
	request, present, err := consumer.persistenceRequest(response)
	if err != nil || !present {
		return persistence.PersistedProposal{}, present, err
	}
	stored, err := consumer.store.Persist(ctx, request)
	if errors.Is(err, persistence.ErrProposalStale) {
		// Results can legitimately become stale between C++ publication and the
		// PostgreSQL transaction. They are discarded and never reach clients.
		return persistence.PersistedProposal{}, false, nil
	}
	if err != nil {
		return persistence.PersistedProposal{}, true, err
	}
	return stored, true, nil
}

func (consumer *ProposalConsumer) persistenceRequest(
	response *liveroutev1.PlannerStreamResponse,
) (persistence.PersistProposalRequest, bool, error) {
	if response == nil || !canonicalUUID(response.GetRequestId()) ||
		!canonicalUUID(response.GetTripId()) || response.GetRuntimeEpoch() == 0 ||
		response.GetTripRevision() == 0 || response.GetAcceptedMutationSequence() == 0 {
		return persistence.PersistProposalRequest{}, false,
			errors.New("invalid planner-result envelope")
	}
	result := response.GetReplanResult()
	if result == nil {
		return persistence.PersistProposalRequest{}, false,
			errors.New("notification is not a replan result")
	}
	proposal := result.GetProposal()
	if proposal == nil {
		if result.GetQuality().GetPlanQuality() !=
			liveroutev1.PlanQuality_PLAN_QUALITY_NO_NEW_PROPOSAL {
			return persistence.PersistProposalRequest{}, false,
				errors.New("proposal absence conflicts with result quality")
		}
		return persistence.PersistProposalRequest{}, false, nil
	}
	if err := validateProposalResult(response, result, proposal); err != nil {
		return persistence.PersistProposalRequest{}, true, err
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(
		&liveroutev1.StoredPlanProposal{
			Proposal: proposal, Notification: result.GetNotification(),
			Reasons: result.GetReasons(), Stats: result.GetStats(),
			Quality: result.GetQuality(),
		},
	)
	if err != nil {
		return persistence.PersistProposalRequest{}, true,
			fmt.Errorf("serialize stored proposal: %w", err)
	}
	return persistence.PersistProposalRequest{
		ProposalID: proposal.GetProposalId(), TripID: response.GetTripId(),
		Source: persistence.ProposalSource{
			RuntimeEpoch:             proposal.GetSourceRuntimeEpoch(),
			PlannerStateVersion:      proposal.GetSourcePlannerStateVersion(),
			TripRevision:             proposal.GetSourceTripRevision(),
			AcceptedMutationSequence: proposal.GetSourceAcceptedMutationSequence(),
			BaseCurrentPlanID:        proposal.GetBaseCurrentPlanId(),
		},
		Current: persistence.RuntimeFreshness{
			HolderID:                 consumer.holderID,
			RuntimeEpoch:             response.GetRuntimeEpoch(),
			PlannerStateVersion:      response.GetPlannerStateVersion(),
			AcceptedMutationSequence: response.GetAcceptedMutationSequence(),
		},
		Payload: payload, Checksum: sha256.Sum256(payload),
		CreatedAt: time.UnixMilli(proposal.GetCreatedAtUnixMs()).UTC(),
	}, true, nil
}

func validateProposalResult(
	response *liveroutev1.PlannerStreamResponse,
	result *liveroutev1.ReplanResult,
	proposal *liveroutev1.PlanProposal,
) error {
	quality := result.GetQuality()
	if result.GetStatus() != liveroutev1.StatusCode_STATUS_CODE_OK || result.GetRetryable() ||
		quality == nil ||
		(quality.GetPlanQuality() != liveroutev1.PlanQuality_PLAN_QUALITY_COMPLETE &&
			quality.GetPlanQuality() != liveroutev1.PlanQuality_PLAN_QUALITY_BEST_SO_FAR) ||
		quality.GetRoutingQuality() == liveroutev1.RoutingQuality_ROUTING_QUALITY_UNSPECIFIED ||
		quality.GetRecoveryState() == liveroutev1.RecoveryState_RECOVERY_STATE_UNSPECIFIED ||
		result.GetNotification() <= liveroutev1.NotificationType_NOTIFICATION_TYPE_UNSPECIFIED ||
		result.GetNotification() >= liveroutev1.NotificationType_NOTIFICATION_TYPE_INFEASIBLE_SCHEDULE ||
		!orderedReasons(result.GetReasons()) {
		return errors.New("proposal result metadata is invalid")
	}
	if !canonicalUUID(proposal.GetProposalId()) ||
		!canonicalUUID(proposal.GetBaseCurrentPlanId()) ||
		proposal.GetSourceRuntimeEpoch() != response.GetRuntimeEpoch() ||
		proposal.GetSourcePlannerStateVersion() != response.GetPlannerStateVersion() ||
		proposal.GetSourceTripRevision() != response.GetTripRevision() ||
		proposal.GetSourceAcceptedMutationSequence() != response.GetAcceptedMutationSequence() ||
		proposal.GetCreatedAtUnixMs() <= 0 {
		return errors.New("proposal source conflicts with response envelope")
	}
	segments := append(
		append([]*liveroutev1.ProposalSegment(nil), proposal.GetPreservedPrefix()...),
		proposal.GetRevisedSuffix()...,
	)
	if len(segments) == 0 || len(segments) > 64 {
		return errors.New("proposal segment count is invalid")
	}
	seen := make(map[string]struct{}, len(segments))
	var priorScheduledEnd *int64
	for index, segment := range segments {
		if err := validateProposalSegment(
			segment, index < len(proposal.GetPreservedPrefix()),
		); err != nil {
			return fmt.Errorf("proposal segment %d: %w", index, err)
		}
		if _, duplicate := seen[segment.GetActivityId()]; duplicate {
			return errors.New("proposal repeats an activity")
		}
		seen[segment.GetActivityId()] = struct{}{}
		if segment.GetDisposition() != liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_SKIPPED {
			if priorScheduledEnd != nil && segment.GetScheduledStartUnixMs() < *priorScheduledEnd {
				return errors.New("proposal segments overlap")
			}
			end := segment.GetScheduledEndUnixMs()
			priorScheduledEnd = &end
		}
	}
	return nil
}

func validateProposalSegment(segment *liveroutev1.ProposalSegment, prefix bool) error {
	if segment == nil || !canonicalUUID(segment.GetActivityId()) ||
		segment.GetLocation() == nil || segment.GetLocation().GetLatitude() < -90 ||
		segment.GetLocation().GetLatitude() > 90 || segment.GetLocation().GetLongitude() < -180 ||
		segment.GetLocation().GetLongitude() > 180 || segment.GetTimeZoneName() == "" ||
		!orderedReasons(segment.GetReasons()) {
		return errors.New("metadata is invalid")
	}
	disposition := segment.GetDisposition()
	if prefix && disposition != liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_PRESERVED {
		return errors.New("preserved prefix has a non-preserved disposition")
	}
	if disposition == liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_SKIPPED {
		if segment.ScheduledStartUnixMs != nil || segment.ScheduledEndUnixMs != nil ||
			segment.InboundRoute != nil {
			return errors.New("skipped segment has schedule or route data")
		}
		return nil
	}
	if disposition != liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_PRESERVED &&
		disposition != liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_MOVED &&
		disposition != liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_ADDED {
		return errors.New("disposition is invalid")
	}
	if segment.ScheduledStartUnixMs == nil || segment.ScheduledEndUnixMs == nil ||
		segment.GetScheduledStartUnixMs() >= segment.GetScheduledEndUnixMs() {
		return errors.New("schedule is invalid")
	}
	if (!prefix && segment.InboundRoute == nil) ||
		(segment.InboundRoute != nil && !segment.InboundRoute.GetReachable()) {
		return errors.New("route is invalid")
	}
	return nil
}

func orderedReasons(reasons []liveroutev1.PlanReasonCode) bool {
	previous := liveroutev1.PlanReasonCode_PLAN_REASON_CODE_UNSPECIFIED
	for _, reason := range reasons {
		if reason <= previous || reason > liveroutev1.PlanReasonCode_PLAN_REASON_CODE_NO_FEASIBLE_PLAN {
			return false
		}
		previous = reason
	}
	return true
}

func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if value[index] != '-' {
				return false
			}
			continue
		}
		if (value[index] < '0' || value[index] > '9') &&
			(value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}
