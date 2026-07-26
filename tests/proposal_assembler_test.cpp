#include "liveroute/planner/proposal_assembler.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <vector>

namespace {

using namespace liveroute::domain;
using namespace liveroute::planner;

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
              .open_windows = {{UnixTimeMilliseconds{-10000},
                                UnixTimeMilliseconds{20000}}},
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

CurrentPlanSegment scheduled(const Activity& activity, std::int64_t start,
                             std::int64_t end) {
  return {
      .activity_id = activity.activity_id,
      .state = PlanEntryState::kScheduled,
      .scheduled_start = UnixTimeMilliseconds{start},
      .scheduled_end = UnixTimeMilliseconds{end},
  };
}

CurrentPlanSegment omitted(const Activity& activity) {
  return {
      .activity_id = activity.activity_id,
      .state = PlanEntryState::kOmitted,
      .scheduled_start = std::nullopt,
      .scheduled_end = std::nullopt,
  };
}

PlanningActivity planning_activity(const Activity& activity,
                                   std::size_t ordinal,
                                   CurrentPlanSegment segment) {
  return {.activity = activity,
          .original_trip_ordinal = ordinal,
          .current_plan_segment = std::move(segment)};
}

TravelTimeMatrix matrix() {
  std::vector<RouteEstimate> estimates;
  estimates.reserve(16);
  for (std::uint32_t index = 0; index < 16; ++index) {
    estimates.push_back(
        RouteEstimate{std::chrono::seconds{0}, index, true});
  }
  return TravelTimeMatrix{4, std::move(estimates)};
}

ProposalSource source() {
  return {
      .proposal_id = id<ProposalId>(20),
      .runtime_epoch = 2,
      .planner_state_version = PlannerStateVersion{3},
      .base_current_plan_id = id<PlanId>(21),
      .trip_revision = TripRevision{4},
      .accepted_mutation_sequence = MutationSequence{5},
      .created_at = UnixTimeMilliseconds{6000},
  };
}

}  // namespace

int main() {
  const auto prefix = activity(9);
  const auto first = activity(1);
  const auto second = activity(2);
  const auto third = activity(3);
  const std::vector<Activity> activities{prefix, first, second, third};
  const auto travel = matrix();
  const BeamSearchInput input{
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{20000},
      .preserved_prefix = {scheduled(prefix, -2000, -1000)},
      .remaining_activities =
          {planning_activity(first, 0, scheduled(first, 1000, 2000)),
           planning_activity(second, 1, scheduled(second, 3000, 4000)),
           planning_activity(third, 2, omitted(third))},
      .travel_time_matrix = &travel,
  };
  const std::vector<ExpansionDecision> changed_decisions{
      {.activity_ordinal = 1,
       .decision = 0,
       .start_unix_ms = 0,
       .end_unix_ms = 1000},
      {.activity_ordinal = 0,
       .decision = 0,
       .start_unix_ms = 1000,
       .end_unix_ms = 2000},
      {.activity_ordinal = 2,
       .decision = 1,
       .start_unix_ms = 0,
       .end_unix_ms = 0},
  };
  const TripEventPayload trigger = ActivityDelayed{first.activity_id, 30};
  const auto assembled =
      assemble_plan_proposal(input, changed_decisions, activities, source(),
                             trigger,
                             ReplanFacts{.late_departure = true,
                                         .reservation_at_risk = false,
                                         .next_event_slack_ms = std::nullopt});
  if (!assembled || !assembled->changes_current_plan ||
      !assembled->proposal.is_valid_for(activities) ||
      assembled->proposal.preserved_prefix.size() != 1 ||
      assembled->proposal.preserved_prefix[0].inbound_route.has_value() ||
      assembled->proposal.revised_suffix.size() != 3 ||
      assembled->proposal.revised_suffix[0].activity_id != second.activity_id ||
      assembled->proposal.revised_suffix[0].disposition !=
          SegmentDisposition::kMoved ||
      assembled->proposal.revised_suffix[1].activity_id != first.activity_id ||
      assembled->proposal.revised_suffix[1].disposition !=
          SegmentDisposition::kMoved ||
      assembled->proposal.revised_suffix[2].activity_id != third.activity_id ||
      assembled->proposal.revised_suffix[2].disposition !=
          SegmentDisposition::kSkipped ||
      assembled->proposal.revised_suffix[0].inbound_route->distance_meters !=
          2 ||
      assembled->proposal.revised_suffix[1].inbound_route->distance_meters !=
          9 ||
      assembled->proposal.revised_suffix[0].reasons !=
          std::vector<PlanReasonCode>{PlanReasonCode::kLateDeparture,
                                      PlanReasonCode::kActivityDelay} ||
      !assembled->proposal.revised_suffix[2].reasons.empty()) {
    return 1;
  }

  const std::vector<ExpansionDecision> added_decisions{
      {.activity_ordinal = 0,
       .decision = 0,
       .start_unix_ms = 1000,
       .end_unix_ms = 2000},
      {.activity_ordinal = 1,
       .decision = 0,
       .start_unix_ms = 3000,
       .end_unix_ms = 4000},
      {.activity_ordinal = 2,
       .decision = 0,
       .start_unix_ms = 4000,
       .end_unix_ms = 5000},
  };
  const auto added =
      assemble_plan_proposal(input, added_decisions, activities, source(),
                             trigger, ReplanFacts{});
  if (!added || !added->changes_current_plan ||
      added->proposal.revised_suffix[2].disposition !=
          SegmentDisposition::kAdded) {
    return 1;
  }

  const std::vector<ExpansionDecision> unchanged_decisions{
      {.activity_ordinal = 0,
       .decision = 0,
       .start_unix_ms = 1000,
       .end_unix_ms = 2000},
      {.activity_ordinal = 1,
       .decision = 0,
       .start_unix_ms = 3000,
       .end_unix_ms = 4000},
      {.activity_ordinal = 2,
       .decision = 1,
       .start_unix_ms = 0,
       .end_unix_ms = 0},
  };
  const auto unchanged =
      assemble_plan_proposal(input, unchanged_decisions, activities, source(),
                             trigger, ReplanFacts{});
  if (!unchanged || unchanged->changes_current_plan ||
      unchanged->proposal.revised_suffix[0].disposition !=
          SegmentDisposition::kPreserved ||
      unchanged->proposal.revised_suffix[1].disposition !=
          SegmentDisposition::kPreserved ||
      !unchanged->proposal.revised_suffix[0].reasons.empty()) {
    return 1;
  }

  auto missing_activity = activities;
  missing_activity.pop_back();
  auto invalid_source = source();
  invalid_source.runtime_epoch = 0;
  return assemble_plan_proposal(input, unchanged_decisions, missing_activity,
                                source(), trigger, ReplanFacts{})
                     .has_value() ||
                 assemble_plan_proposal(input, unchanged_decisions, activities,
                                        invalid_source, trigger, ReplanFacts{})
                     .has_value()
             ? 1
             : 0;
}
