#include "liveroute/domain/plan_proposal.hpp"

#include <array>
#include <chrono>
#include <cstdint>
#include <optional>
#include <vector>

namespace {

using liveroute::domain::Activity;
using liveroute::domain::ActivityClass;
using liveroute::domain::ActivityId;
using liveroute::domain::ActivityState;
using liveroute::domain::ActivityTiming;
using liveroute::domain::Location;
using liveroute::domain::MutationSequence;
using liveroute::domain::NotificationType;
using liveroute::domain::PlaceId;
using liveroute::domain::PlanId;
using liveroute::domain::PlanProposal;
using liveroute::domain::PlanQuality;
using liveroute::domain::PlanReasonCode;
using liveroute::domain::PlannerStateVersion;
using liveroute::domain::PlannerStats;
using liveroute::domain::ProposalId;
using liveroute::domain::ProposalSegment;
using liveroute::domain::RecoveryState;
using liveroute::domain::ResultQuality;
using liveroute::domain::RouteEstimate;
using liveroute::domain::RoutingQuality;
using liveroute::domain::SegmentDisposition;
using liveroute::domain::StoredPlanProposal;
using liveroute::domain::TravelMode;
using liveroute::domain::TripRevision;
using liveroute::domain::UnixTimeMilliseconds;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

Activity activity(std::uint8_t marker) {
  return {
      .activity_id = id<ActivityId>(marker),
      .place_id = PlaceId{"place"},
      .display_name = "Place",
      .location = Location{40.0 + marker, -74.0},
      .time_zone_name = "America/New_York",
      .inbound_travel_mode = TravelMode::kWalking,
      .activity_class = ActivityClass::kFlexible,
      .activity_state = ActivityState::kPlanned,
      .priority_rank = 0,
      .utility_score = 1,
      .timing =
          ActivityTiming{
              .open_windows = {{UnixTimeMilliseconds{0},
                                UnixTimeMilliseconds{10000}}},
              .reservation_start = std::nullopt,
              .reservation_grace_seconds = 0,
              .min_duration_seconds = 1,
              .preferred_duration_seconds = 1,
              .max_duration_seconds = 1,
              .mandatory = false,
              .can_shorten = false,
              .can_move = true,
              .can_skip = true,
              .mandatory_deadline = std::nullopt},
      .activity_delay_seconds = 0,
      .found_closed_at = std::nullopt,
  };
}

ProposalSegment scheduled_segment(const Activity& activity,
                                  SegmentDisposition disposition,
                                  std::int64_t start, std::int64_t end) {
  return {
      .activity_id = activity.activity_id,
      .location = activity.location,
      .time_zone_name = activity.time_zone_name,
      .scheduled_start = UnixTimeMilliseconds{start},
      .scheduled_end = UnixTimeMilliseconds{end},
      .inbound_route =
          RouteEstimate{std::chrono::seconds{1}, 10, true},
      .disposition = disposition,
      .reasons = {PlanReasonCode::kLateDeparture},
  };
}

ProposalSegment skipped_segment(const Activity& activity) {
  return {
      .activity_id = activity.activity_id,
      .location = activity.location,
      .time_zone_name = activity.time_zone_name,
      .scheduled_start = std::nullopt,
      .scheduled_end = std::nullopt,
      .inbound_route = std::nullopt,
      .disposition = SegmentDisposition::kSkipped,
      .reasons = {},
  };
}

PlanProposal proposal(const Activity& first, const Activity& second) {
  return {
      .proposal_id = id<ProposalId>(8),
      .source_runtime_epoch = 2,
      .source_planner_state_version = PlannerStateVersion{3},
      .base_current_plan_id = id<PlanId>(7),
      .source_trip_revision = TripRevision{4},
      .source_accepted_mutation_sequence = MutationSequence{5},
      .preserved_prefix = {
          scheduled_segment(first, SegmentDisposition::kPreserved, 0, 1000)},
      .revised_suffix = {
          scheduled_segment(second, SegmentDisposition::kMoved, 1000, 2000)},
      .created_at = UnixTimeMilliseconds{3000},
  };
}

}  // namespace

int main() {
  const std::vector<Activity> activities{activity(1), activity(2)};
  const auto valid = proposal(activities[0], activities[1]);
  if (!valid.is_valid_for(activities)) return 1;

  auto skipped = valid;
  skipped.revised_suffix = {skipped_segment(activities[1])};
  if (!skipped.is_valid_for(activities)) return 1;

  auto no_epoch = valid;
  no_epoch.source_runtime_epoch = 0;
  auto no_revision = valid;
  no_revision.source_trip_revision = TripRevision{0};
  auto no_sequence = valid;
  no_sequence.source_accepted_mutation_sequence = MutationSequence{0};
  auto duplicate = valid;
  duplicate.revised_suffix.front().activity_id =
      duplicate.preserved_prefix.front().activity_id;
  auto moved_prefix = valid;
  moved_prefix.preserved_prefix.front().disposition =
      SegmentDisposition::kMoved;
  auto overlaps = valid;
  overlaps.revised_suffix.front().scheduled_start =
      UnixTimeMilliseconds{500};
  auto wrong_location = valid;
  wrong_location.revised_suffix.front().location.latitude = 0;
  auto missing_route = valid;
  missing_route.revised_suffix.front().inbound_route = std::nullopt;
  auto prefix_without_route = valid;
  prefix_without_route.preserved_prefix.front().inbound_route = std::nullopt;
  auto unreachable = valid;
  unreachable.revised_suffix.front().inbound_route->reachable = false;
  auto unspecified_disposition = valid;
  unspecified_disposition.revised_suffix.front().disposition =
      static_cast<SegmentDisposition>(0);
  auto shortened = valid;
  shortened.revised_suffix.front().disposition =
      SegmentDisposition::kShortened;
  auto unspecified_reason = valid;
  unspecified_reason.revised_suffix.front().reasons = {
      static_cast<PlanReasonCode>(0)};
  if (!prefix_without_route.is_valid_for(activities) ||
      no_epoch.is_valid_for(activities) ||
      no_revision.is_valid_for(activities) ||
      no_sequence.is_valid_for(activities) ||
      duplicate.is_valid_for(activities) ||
      moved_prefix.is_valid_for(activities) ||
      overlaps.is_valid_for(activities) ||
      wrong_location.is_valid_for(activities) ||
      missing_route.is_valid_for(activities) ||
      unreachable.is_valid_for(activities) ||
      unspecified_disposition.is_valid_for(activities) ||
      shortened.is_valid_for(activities) ||
      unspecified_reason.is_valid_for(activities)) {
    return 1;
  }

  const StoredPlanProposal stored{
      .proposal = valid,
      .notification = NotificationType::kPlanChangeSuggested,
      .reasons = {PlanReasonCode::kLateDeparture},
      .stats =
          PlannerStats{
              .candidates_evaluated = 10,
              .candidates_pruned = 2,
              .search_depth = 2,
              .queue_wait_microseconds = 1,
              .provider_microseconds = 2,
              .planner_microseconds = 3,
              .serialization_microseconds = 4,
              .deadline_hit = false},
      .quality =
          ResultQuality{.plan_quality = PlanQuality::kComplete,
                        .routing_quality = RoutingQuality::kFresh,
                        .recovery_state = RecoveryState::kCurrent},
  };
  if (!stored.is_valid_for(activities)) return 1;

  auto no_proposal_quality = stored;
  no_proposal_quality.quality.plan_quality = PlanQuality::kNoNewProposal;
  auto excessive_depth = stored;
  excessive_depth.stats.search_depth = 65;
  auto invalid_notification = stored;
  invalid_notification.notification = static_cast<NotificationType>(0);
  return no_proposal_quality.is_valid_for(activities) ||
                 excessive_depth.is_valid_for(activities) ||
                 invalid_notification.is_valid_for(activities)
             ? 1
             : 0;
}
