package gateway

import (
	"bytes"
	"errors"
	"fmt"

	liveroutev1 "github.com/liveroute/liveroute/backend/gen/liveroute/v1"
	"github.com/liveroute/liveroute/backend/internal/persistence"
	"google.golang.org/protobuf/proto"
)

var errSubscriptionStateInvalid = errors.New("subscription state is invalid")

// SubscriptionStatePayload converts one PostgreSQL-consistent canonical state
// into the JSON-compatible payload used by subscription_state and
// resynchronization_state. It intentionally accepts only the persistence
// model and deterministic stored proposal bytes; no transport or planner
// stream objects enter this conversion.
func SubscriptionStatePayload(
	state persistence.CanonicalTripState,
	pendingProposal []byte,
	subscribed bool,
	runtimeSyncState string,
) (map[string]any, error) {
	if runtimeSyncState != "not_required" && runtimeSyncState != "pending" &&
		runtimeSyncState != "synced" && runtimeSyncState != "paused_internal" {
		return nil, fmt.Errorf("%w: runtime sync state %q", errSubscriptionStateInvalid, runtimeSyncState)
	}
	trip, err := durableTripJSON(state)
	if err != nil {
		return nil, err
	}
	currentPlan, err := currentPlanJSON(state)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"subscribed":         subscribed,
		"trip":               trip,
		"current_plan":       currentPlan,
		"runtime_sync_state": runtimeSyncState,
	}
	if len(pendingProposal) != 0 {
		proposal, err := storedProposalJSON(pendingProposal)
		if err != nil {
			return nil, err
		}
		payload["latest_pending_proposal"] = proposal
	}
	return payload, nil
}

// StoredProposalPayload decodes the exact deterministic proposal artifact
// persisted by PostgreSQL for schema-shaped WebSocket publication.
func StoredProposalPayload(payload []byte) (map[string]any, error) {
	return storedProposalJSON(payload)
}

func durableTripJSON(state persistence.CanonicalTripState) (map[string]any, error) {
	if !canonicalUUID(state.TripID) || !canonicalUUID(state.OwnerUserID) ||
		state.DefaultTimeZoneName == "" || !canonicalUUID(state.CurrentPlanID) {
		return nil, fmt.Errorf("%w: trip identity", errSubscriptionStateInvalid)
	}
	activities := make([]any, len(state.Activities))
	for index, activity := range state.Activities {
		if activity.Ordinal != uint32(index) || !canonicalUUID(activity.ID) ||
			activity.PlaceID == "" || activity.DisplayName == "" ||
			activity.TimeZoneName == "" {
			return nil, fmt.Errorf("%w: activity %d", errSubscriptionStateInvalid, index)
		}
		value := map[string]any{
			"activity_id":            activity.ID,
			"place_id":               activity.PlaceID,
			"display_name":           activity.DisplayName,
			"location":               map[string]any{"latitude": activity.Latitude, "longitude": activity.Longitude},
			"time_zone_name":         activity.TimeZoneName,
			"inbound_travel_mode":    activity.InboundTravelMode,
			"activity_class":         activity.ActivityClass,
			"activity_state":         string(activity.ActivityState),
			"priority_rank":          activity.PriorityRank,
			"utility_score":          activity.UtilityScore,
			"activity_delay_seconds": activity.ActivityDelaySeconds,
			"timing":                 activityTimingJSON(activity),
		}
		if activity.FoundClosedAt != nil {
			value["found_closed_at_unix_ms"] = activity.FoundClosedAt.UnixMilli()
		}
		activities[index] = value
	}
	travelDelays := make([]any, len(state.TravelDelays))
	for index, delay := range state.TravelDelays {
		if !canonicalUUID(delay.FromActivityID) || !canonicalUUID(delay.ToActivityID) {
			return nil, fmt.Errorf("%w: travel delay %d", errSubscriptionStateInvalid, index)
		}
		travelDelays[index] = map[string]any{
			"from_activity_id":    delay.FromActivityID,
			"to_activity_id":      delay.ToActivityID,
			"additional_seconds":  delay.AdditionalSeconds,
			"observed_at_unix_ms": delay.ObservedAt.UnixMilli(),
		}
	}
	trip := map[string]any{
		"trip_id":                state.TripID,
		"owner_user_id":          state.OwnerUserID,
		"default_time_zone_name": state.DefaultTimeZoneName,
		"activities":             activities,
		"completed_prefix_count": state.CompletedPrefixCount,
		"current_plan_id":        state.CurrentPlanID,
		"travel_delays":          travelDelays,
	}
	if state.CurrentActivityID != nil {
		if !canonicalUUID(*state.CurrentActivityID) {
			return nil, fmt.Errorf("%w: current activity", errSubscriptionStateInvalid)
		}
		trip["current_activity_id"] = *state.CurrentActivityID
	}
	return trip, nil
}

func activityTimingJSON(activity persistence.CanonicalActivity) map[string]any {
	windows := make([]any, len(activity.OpenWindows))
	for index, window := range activity.OpenWindows {
		windows[index] = map[string]any{
			"opens_at_unix_ms":  window.OpensAt.UnixMilli(),
			"closes_at_unix_ms": window.ClosesAt.UnixMilli(),
		}
	}
	timing := map[string]any{
		"open_windows":               windows,
		"reservation_grace_seconds":  activity.ReservationGraceSeconds,
		"min_duration_seconds":       activity.MinDurationSeconds,
		"preferred_duration_seconds": activity.PreferredDurationSeconds,
		"max_duration_seconds":       activity.MaxDurationSeconds,
		"mandatory":                  activity.Mandatory,
		"can_shorten":                activity.CanShorten,
		"can_move":                   activity.CanMove,
		"can_skip":                   activity.CanSkip,
	}
	if activity.ReservationStart != nil {
		timing["reservation_start_unix_ms"] = activity.ReservationStart.UnixMilli()
	}
	if activity.MandatoryDeadline != nil {
		timing["mandatory_deadline_unix_ms"] = activity.MandatoryDeadline.UnixMilli()
	}
	return timing
}

func currentPlanJSON(state persistence.CanonicalTripState) (map[string]any, error) {
	plan := &liveroutev1.CurrentPlan{}
	if err := proto.Unmarshal(state.CurrentPlan.Payload, plan); err != nil {
		return nil, fmt.Errorf("%w: decode current plan: %v", errSubscriptionStateInvalid, err)
	}
	if !canonicalUUID(plan.GetPlanId()) || plan.GetPlanId() != state.CurrentPlan.ID ||
		plan.GetPlanRevision() != state.CurrentPlan.Revision ||
		plan.GetCreatedAtUnixMs() != state.CurrentPlan.CreatedAt.UnixMilli() {
		return nil, fmt.Errorf("%w: current plan metadata", errSubscriptionStateInvalid)
	}
	origin, err := planOriginJSON(plan.GetOrigin())
	if err != nil {
		return nil, err
	}
	segments := make([]any, len(plan.GetSegments()))
	for index, segment := range plan.GetSegments() {
		if !canonicalUUID(segment.GetActivityId()) {
			return nil, fmt.Errorf("%w: current plan segment %d", errSubscriptionStateInvalid, index)
		}
		value := map[string]any{"activity_id": segment.GetActivityId()}
		switch segment.GetState() {
		case liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_SCHEDULED:
			if segment.ScheduledStartUnixMs == nil || segment.ScheduledEndUnixMs == nil {
				return nil, fmt.Errorf("%w: scheduled current plan segment %d", errSubscriptionStateInvalid, index)
			}
			value["state"] = "scheduled"
			value["scheduled_start_unix_ms"] = segment.GetScheduledStartUnixMs()
			value["scheduled_end_unix_ms"] = segment.GetScheduledEndUnixMs()
		case liveroutev1.PlanEntryState_PLAN_ENTRY_STATE_OMITTED:
			value["state"] = "omitted"
		default:
			return nil, fmt.Errorf("%w: current plan segment state", errSubscriptionStateInvalid)
		}
		segments[index] = value
	}
	result := map[string]any{
		"plan_id":            plan.GetPlanId(),
		"plan_revision":      fmt.Sprintf("%d", plan.GetPlanRevision()),
		"origin":             origin,
		"segments":           segments,
		"created_at_unix_ms": plan.GetCreatedAtUnixMs(),
	}
	if plan.SourceProposalId != nil {
		if !canonicalUUID(plan.GetSourceProposalId()) {
			return nil, fmt.Errorf("%w: current plan source proposal", errSubscriptionStateInvalid)
		}
		result["source_proposal_id"] = plan.GetSourceProposalId()
	}
	return result, nil
}

func storedProposalJSON(payload []byte) (map[string]any, error) {
	proposal := &liveroutev1.StoredPlanProposal{}
	if err := proto.Unmarshal(payload, proposal); err != nil {
		return nil, fmt.Errorf("%w: decode stored proposal: %v", errSubscriptionStateInvalid, err)
	}
	deterministic, err := (proto.MarshalOptions{Deterministic: true}).Marshal(proposal)
	if err != nil || !bytes.Equal(deterministic, payload) {
		return nil, fmt.Errorf("%w: stored proposal is not deterministic", errSubscriptionStateInvalid)
	}
	plan := proposal.GetProposal()
	if plan == nil || !canonicalUUID(plan.GetProposalId()) || !canonicalUUID(plan.GetBaseCurrentPlanId()) {
		return nil, fmt.Errorf("%w: proposal identity", errSubscriptionStateInvalid)
	}
	preserved, err := proposalSegmentsJSON(plan.GetPreservedPrefix())
	if err != nil {
		return nil, err
	}
	revised, err := proposalSegmentsJSON(plan.GetRevisedSuffix())
	if err != nil {
		return nil, err
	}
	reasons, err := planReasonsJSON(proposal.GetReasons())
	if err != nil {
		return nil, err
	}
	stats := proposalStatsJSON(proposal.GetStats())
	quality, err := resultQualityJSON(proposal.GetQuality())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"proposal": map[string]any{
			"proposal_id":                       plan.GetProposalId(),
			"source_runtime_epoch":              fmt.Sprintf("%d", plan.GetSourceRuntimeEpoch()),
			"source_planner_state_version":      fmt.Sprintf("%d", plan.GetSourcePlannerStateVersion()),
			"base_current_plan_id":              plan.GetBaseCurrentPlanId(),
			"source_trip_revision":              fmt.Sprintf("%d", plan.GetSourceTripRevision()),
			"source_accepted_mutation_sequence": fmt.Sprintf("%d", plan.GetSourceAcceptedMutationSequence()),
			"preserved_prefix":                  preserved,
			"revised_suffix":                    revised,
			"created_at_unix_ms":                plan.GetCreatedAtUnixMs(),
		},
		"notification": notificationJSON(proposal.GetNotification()),
		"reasons":      reasons,
		"stats":        stats,
		"quality":      quality,
	}, nil
}

func proposalSegmentsJSON(segments []*liveroutev1.ProposalSegment) ([]any, error) {
	result := make([]any, len(segments))
	for index, segment := range segments {
		if !canonicalUUID(segment.GetActivityId()) || segment.GetLocation() == nil ||
			segment.GetTimeZoneName() == "" {
			return nil, fmt.Errorf("%w: proposal segment %d", errSubscriptionStateInvalid, index)
		}
		disposition, err := segmentDispositionJSON(segment.GetDisposition())
		if err != nil {
			return nil, err
		}
		reasons, err := planReasonsJSON(segment.GetReasons())
		if err != nil {
			return nil, err
		}
		value := map[string]any{
			"activity_id": segment.GetActivityId(),
			"location": map[string]any{
				"latitude":  segment.GetLocation().GetLatitude(),
				"longitude": segment.GetLocation().GetLongitude(),
			},
			"time_zone_name": segment.GetTimeZoneName(),
			"disposition":    disposition,
			"reasons":        reasons,
		}
		if segment.ScheduledStartUnixMs != nil {
			value["scheduled_start_unix_ms"] = segment.GetScheduledStartUnixMs()
		}
		if segment.ScheduledEndUnixMs != nil {
			value["scheduled_end_unix_ms"] = segment.GetScheduledEndUnixMs()
		}
		if route := segment.GetInboundRoute(); route != nil {
			value["inbound_route"] = map[string]any{
				"duration_seconds": route.GetDurationSeconds(),
				"distance_meters":  route.GetDistanceMeters(),
				"reachable":        route.GetReachable(),
			}
		}
		result[index] = value
	}
	return result, nil
}

func planReasonsJSON(reasons []liveroutev1.PlanReasonCode) ([]any, error) {
	result := make([]any, len(reasons))
	for index, reason := range reasons {
		value, err := planReasonJSON(reason)
		if err != nil {
			return nil, err
		}
		result[index] = value
	}
	return result, nil
}

func proposalStatsJSON(stats *liveroutev1.PlannerStats) map[string]any {
	if stats == nil {
		return map[string]any{
			"candidates_evaluated": "0", "candidates_pruned": "0", "search_depth": 0,
			"queue_wait_microseconds": 0, "provider_microseconds": 0,
			"planner_microseconds": 0, "serialization_microseconds": 0, "deadline_hit": false,
		}
	}
	return map[string]any{
		"candidates_evaluated":       fmt.Sprintf("%d", stats.GetCandidatesEvaluated()),
		"candidates_pruned":          fmt.Sprintf("%d", stats.GetCandidatesPruned()),
		"search_depth":               stats.GetSearchDepth(),
		"queue_wait_microseconds":    stats.GetQueueWaitMicroseconds(),
		"provider_microseconds":      stats.GetProviderMicroseconds(),
		"planner_microseconds":       stats.GetPlannerMicroseconds(),
		"serialization_microseconds": stats.GetSerializationMicroseconds(),
		"deadline_hit":               stats.GetDeadlineHit(),
	}
}

func resultQualityJSON(quality *liveroutev1.ResultQuality) (map[string]any, error) {
	if quality == nil {
		return nil, fmt.Errorf("%w: proposal quality missing", errSubscriptionStateInvalid)
	}
	planQuality, err := planQualityJSON(quality.GetPlanQuality())
	if err != nil {
		return nil, err
	}
	routingQuality, err := routingQualityJSON(quality.GetRoutingQuality())
	if err != nil {
		return nil, err
	}
	recoveryState, err := recoveryStateJSON(quality.GetRecoveryState())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"plan_quality":    planQuality,
		"routing_quality": routingQuality,
		"recovery_state":  recoveryState,
	}, nil
}

func planOriginJSON(value liveroutev1.PlanOrigin) (string, error) {
	switch value {
	case liveroutev1.PlanOrigin_PLAN_ORIGIN_USER_AUTHORED:
		return "user_authored", nil
	case liveroutev1.PlanOrigin_PLAN_ORIGIN_ACCEPTED_ENGINE_PROPOSAL:
		return "accepted_engine_proposal", nil
	default:
		return "", fmt.Errorf("%w: plan origin", errSubscriptionStateInvalid)
	}
}

func segmentDispositionJSON(value liveroutev1.SegmentDisposition) (string, error) {
	switch value {
	case liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_PRESERVED:
		return "preserved", nil
	case liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_MOVED:
		return "moved", nil
	case liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_SHORTENED:
		return "shortened", nil
	case liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_SKIPPED:
		return "skipped", nil
	case liveroutev1.SegmentDisposition_SEGMENT_DISPOSITION_ADDED:
		return "added", nil
	default:
		return "", fmt.Errorf("%w: proposal disposition", errSubscriptionStateInvalid)
	}
}

func planReasonJSON(value liveroutev1.PlanReasonCode) (string, error) {
	values := map[liveroutev1.PlanReasonCode]string{
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_LATE_DEPARTURE:      "late_departure",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_ACTIVITY_DELAY:      "activity_delay",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_ROUTE_DEVIATION:     "route_deviation",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_HOURS_CHANGED:       "hours_changed",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_PLACE_CLOSED:        "place_closed",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_RESERVATION_AT_RISK: "reservation_at_risk",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_TRAVEL_DELAY:        "travel_delay",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_USER_EDIT:           "user_edit",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_DEADLINE_BUDGET:     "deadline_budget",
		liveroutev1.PlanReasonCode_PLAN_REASON_CODE_NO_FEASIBLE_PLAN:    "no_feasible_plan",
	}
	if result, ok := values[value]; ok {
		return result, nil
	}
	return "", fmt.Errorf("%w: proposal reason", errSubscriptionStateInvalid)
}

func notificationJSON(value liveroutev1.NotificationType) string {
	values := map[liveroutev1.NotificationType]string{
		liveroutev1.NotificationType_NOTIFICATION_TYPE_NONE:                  "none",
		liveroutev1.NotificationType_NOTIFICATION_TYPE_LOW_SLACK_WARNING:     "low_slack_warning",
		liveroutev1.NotificationType_NOTIFICATION_TYPE_CRITICAL_LATENESS:     "critical_lateness",
		liveroutev1.NotificationType_NOTIFICATION_TYPE_PLAN_CHANGE_SUGGESTED: "plan_change_suggested",
		liveroutev1.NotificationType_NOTIFICATION_TYPE_INFEASIBLE_SCHEDULE:   "infeasible_schedule",
	}
	return values[value]
}

func planQualityJSON(value liveroutev1.PlanQuality) (string, error) {
	values := map[liveroutev1.PlanQuality]string{
		liveroutev1.PlanQuality_PLAN_QUALITY_COMPLETE:        "complete",
		liveroutev1.PlanQuality_PLAN_QUALITY_BEST_SO_FAR:     "best_so_far",
		liveroutev1.PlanQuality_PLAN_QUALITY_NO_NEW_PROPOSAL: "no_new_proposal",
	}
	if result, ok := values[value]; ok {
		return result, nil
	}
	return "", fmt.Errorf("%w: plan quality", errSubscriptionStateInvalid)
}

func routingQualityJSON(value liveroutev1.RoutingQuality) (string, error) {
	values := map[liveroutev1.RoutingQuality]string{
		liveroutev1.RoutingQuality_ROUTING_QUALITY_FRESH:       "fresh",
		liveroutev1.RoutingQuality_ROUTING_QUALITY_STALE_CACHE: "stale_cache",
		liveroutev1.RoutingQuality_ROUTING_QUALITY_UNAVAILABLE: "unavailable",
	}
	if result, ok := values[value]; ok {
		return result, nil
	}
	return "", fmt.Errorf("%w: routing quality", errSubscriptionStateInvalid)
}

func recoveryStateJSON(value liveroutev1.RecoveryState) (string, error) {
	values := map[liveroutev1.RecoveryState]string{
		liveroutev1.RecoveryState_RECOVERY_STATE_CURRENT:       "current",
		liveroutev1.RecoveryState_RECOVERY_STATE_NOT_ADVANCING: "not_advancing",
	}
	if result, ok := values[value]; ok {
		return result, nil
	}
	return "", fmt.Errorf("%w: recovery state", errSubscriptionStateInvalid)
}
