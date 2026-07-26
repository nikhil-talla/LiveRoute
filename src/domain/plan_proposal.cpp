#include "liveroute/domain/plan_proposal.hpp"

#include <algorithm>
#include <cstddef>
#include <optional>
#include <vector>

namespace liveroute::domain {

namespace {

[[nodiscard]] bool is_valid_disposition(
    SegmentDisposition disposition) noexcept {
  switch (disposition) {
    case SegmentDisposition::kPreserved:
    case SegmentDisposition::kMoved:
    case SegmentDisposition::kSkipped:
    case SegmentDisposition::kAdded:
      return true;
    case SegmentDisposition::kShortened:
      return false;
  }
  return false;
}

[[nodiscard]] bool is_valid_reason(PlanReasonCode reason) noexcept {
  switch (reason) {
    case PlanReasonCode::kLateDeparture:
    case PlanReasonCode::kActivityDelay:
    case PlanReasonCode::kRouteDeviation:
    case PlanReasonCode::kHoursChanged:
    case PlanReasonCode::kPlaceClosed:
    case PlanReasonCode::kReservationAtRisk:
    case PlanReasonCode::kTravelDelay:
    case PlanReasonCode::kUserEdit:
    case PlanReasonCode::kDeadlineBudget:
    case PlanReasonCode::kNoFeasiblePlan:
      return true;
  }
  return false;
}

[[nodiscard]] bool is_valid_notification(
    NotificationType notification) noexcept {
  switch (notification) {
    case NotificationType::kNone:
    case NotificationType::kLowSlackWarning:
    case NotificationType::kCriticalLateness:
    case NotificationType::kPlanChangeSuggested:
    case NotificationType::kInfeasibleSchedule:
      return true;
  }
  return false;
}

[[nodiscard]] bool same_location(const Location& left,
                                 const Location& right) noexcept {
  return left.latitude == right.latitude &&
         left.longitude == right.longitude;
}

[[nodiscard]] const Activity* activity_for_id(
    std::span<const Activity> activities,
    const ActivityId& activity_id) noexcept {
  for (const auto& activity : activities) {
    if (activity.activity_id == activity_id) return &activity;
  }
  return nullptr;
}

}  // namespace

bool ProposalSegment::is_valid_for(const Activity& activity,
                                   bool inbound_route_required) const noexcept {
  if (activity_id != activity.activity_id || !activity.is_valid() ||
      !location.is_valid() || !same_location(location, activity.location) ||
      time_zone_name.empty() ||
      time_zone_name != activity.time_zone_name ||
      !is_valid_disposition(disposition) ||
      std::any_of(reasons.begin(), reasons.end(),
                  [](PlanReasonCode reason) {
                    return !is_valid_reason(reason);
                  })) {
    return false;
  }

  if (disposition == SegmentDisposition::kSkipped) {
    return !scheduled_start.has_value() && !scheduled_end.has_value() &&
           !inbound_route.has_value();
  }
  return scheduled_start.has_value() && scheduled_end.has_value() &&
         *scheduled_start < *scheduled_end &&
         (!inbound_route_required || inbound_route.has_value()) &&
         (!inbound_route.has_value() ||
          (inbound_route->is_valid() && inbound_route->reachable));
}

bool PlanProposal::is_valid_for(std::span<const Activity> activities) const {
  if (source_runtime_epoch == 0 || source_trip_revision.value() == 0 ||
      source_accepted_mutation_sequence.value() == 0 ||
      activities.size() > 64 || preserved_prefix.size() > 64 ||
      revised_suffix.size() > 64 - preserved_prefix.size() ||
      preserved_prefix.size() + revised_suffix.size() != activities.size()) {
    return false;
  }

  std::vector<ActivityId> expected;
  expected.reserve(activities.size());
  for (const auto& activity : activities) {
    if (!activity.is_valid()) return false;
    expected.push_back(activity.activity_id);
  }
  std::sort(expected.begin(), expected.end());
  if (std::adjacent_find(expected.begin(), expected.end()) != expected.end()) {
    return false;
  }

  std::vector<ActivityId> actual;
  actual.reserve(activities.size());
  std::optional<UnixTimeMilliseconds> prior_scheduled_end;
  const auto validate_segment =
      [&](const ProposalSegment& segment, bool preserved) {
        const auto* activity = activity_for_id(activities, segment.activity_id);
        if (activity == nullptr ||
            !segment.is_valid_for(*activity, !preserved) ||
            (preserved &&
             segment.disposition != SegmentDisposition::kPreserved &&
             segment.disposition != SegmentDisposition::kSkipped)) {
          return false;
        }
        actual.push_back(segment.activity_id);
        if (segment.disposition != SegmentDisposition::kSkipped) {
          if (prior_scheduled_end.has_value() &&
              *segment.scheduled_start < *prior_scheduled_end) {
            return false;
          }
          prior_scheduled_end = segment.scheduled_end;
        }
        return true;
      };

  for (const auto& segment : preserved_prefix) {
    if (!validate_segment(segment, true)) return false;
  }
  for (const auto& segment : revised_suffix) {
    if (!validate_segment(segment, false)) return false;
  }

  std::sort(actual.begin(), actual.end());
  if (std::adjacent_find(actual.begin(), actual.end()) != actual.end()) {
    return false;
  }
  return actual == expected;
}

bool ResultQuality::is_valid() const noexcept {
  switch (plan_quality) {
    case PlanQuality::kComplete:
    case PlanQuality::kBestSoFar:
    case PlanQuality::kNoNewProposal:
      break;
    default:
      return false;
  }
  switch (routing_quality) {
    case RoutingQuality::kFresh:
    case RoutingQuality::kStaleCache:
    case RoutingQuality::kUnavailable:
      break;
    default:
      return false;
  }
  switch (recovery_state) {
    case RecoveryState::kCurrent:
    case RecoveryState::kNotAdvancing:
      return true;
  }
  return false;
}

bool StoredPlanProposal::is_valid_for(
    std::span<const Activity> activities) const {
  return proposal.is_valid_for(activities) &&
         is_valid_notification(notification) &&
         std::all_of(reasons.begin(), reasons.end(), is_valid_reason) &&
         stats.is_valid() && quality.is_valid() &&
         (quality.plan_quality == PlanQuality::kComplete ||
          quality.plan_quality == PlanQuality::kBestSoFar);
}

}  // namespace liveroute::domain
