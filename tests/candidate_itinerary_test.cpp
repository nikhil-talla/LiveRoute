#include "liveroute/planner/candidate_itinerary.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <vector>

namespace {

using liveroute::domain::ActivityClass;
using liveroute::domain::ActivityId;
using liveroute::domain::ActivityState;
using liveroute::domain::ActivityTiming;
using liveroute::domain::CurrentPlanSegment;
using liveroute::domain::Location;
using liveroute::domain::PlaceId;
using liveroute::domain::PlanEntryState;
using liveroute::domain::RouteEstimate;
using liveroute::domain::TravelMode;
using liveroute::domain::TravelTimeMatrix;
using liveroute::domain::UnixTimeMilliseconds;
using liveroute::planner::BeamSearchInput;
using liveroute::planner::ExpansionDecision;
using liveroute::planner::PlanningActivity;
using liveroute::planner::reconstruct_candidate_itinerary;

ActivityId activity_id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return ActivityId{bytes};
}

PlanningActivity activity(std::uint8_t marker, std::size_t ordinal) {
  const auto id = activity_id(marker);
  return {
      .activity =
          {.activity_id = id,
           .place_id = PlaceId{"place"},
           .display_name = "Place",
           .location = Location{40, -74},
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
           .found_closed_at = std::nullopt},
      .original_trip_ordinal = ordinal,
      .current_plan_segment =
          {.activity_id = id,
           .state = PlanEntryState::kOmitted,
           .scheduled_start = std::nullopt,
           .scheduled_end = std::nullopt},
  };
}

TravelTimeMatrix zero_matrix(std::size_t locations) {
  return TravelTimeMatrix(
      locations,
      std::vector<RouteEstimate>(
          locations * locations,
          RouteEstimate{std::chrono::seconds{0}, 0, true}));
}

ExpansionDecision scheduled(std::size_t ordinal, std::int64_t start,
                            std::int64_t end) {
  return {.activity_ordinal = ordinal,
          .decision = 0,
          .start_unix_ms = start,
          .end_unix_ms = end};
}

ExpansionDecision skipped(std::size_t ordinal) {
  return {.activity_ordinal = ordinal,
          .decision = 1,
          .start_unix_ms = 0,
          .end_unix_ms = 0};
}

}  // namespace

int main() {
  const auto prefix_id = activity_id(9);
  const CurrentPlanSegment prefix{
      .activity_id = prefix_id,
      .state = PlanEntryState::kScheduled,
      .scheduled_start = UnixTimeMilliseconds{-2000},
      .scheduled_end = UnixTimeMilliseconds{-1000},
  };
  const auto first = activity(1, 7);
  const auto second = activity(2, 1);
  const auto third = activity(3, 3);
  const auto matrix = zero_matrix(4);
  const BeamSearchInput input{
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{10000},
      .preserved_prefix = {prefix},
      .remaining_activities = {first, second, third},
      .travel_time_matrix = &matrix,
  };
  const std::vector<ExpansionDecision> decisions{
      skipped(7), scheduled(3, 0, 1000), skipped(1)};
  const auto itinerary = reconstruct_candidate_itinerary(input, decisions);
  if (!itinerary || !itinerary->is_valid_for(input) ||
      itinerary->preserved_prefix.size() != 1 ||
      itinerary->revised_suffix.size() != 3 ||
      itinerary->revised_suffix[0].activity_id != activity_id(3) ||
      itinerary->revised_suffix[0].state != PlanEntryState::kScheduled ||
      itinerary->revised_suffix[1].activity_id != activity_id(2) ||
      itinerary->revised_suffix[1].state != PlanEntryState::kOmitted ||
      itinerary->revised_suffix[2].activity_id != activity_id(1) ||
      itinerary->revised_suffix[2].state != PlanEntryState::kOmitted) {
    return 1;
  }

  auto omitted_before_scheduled = *itinerary;
  std::swap(omitted_before_scheduled.revised_suffix[0],
            omitted_before_scheduled.revised_suffix[1]);
  auto changed_prefix = *itinerary;
  changed_prefix.preserved_prefix.front().scheduled_end =
      UnixTimeMilliseconds{-500};
  auto duplicate_suffix = *itinerary;
  duplicate_suffix.revised_suffix[2].activity_id =
      duplicate_suffix.revised_suffix[1].activity_id;
  if (omitted_before_scheduled.is_valid_for(input) ||
      changed_prefix.is_valid_for(input) ||
      duplicate_suffix.is_valid_for(input)) {
    return 1;
  }

  const std::vector<ExpansionDecision> incomplete{scheduled(3, 0, 1000)};
  const std::vector<ExpansionDecision> non_generated{
      skipped(7), scheduled(3, 500, 1500), skipped(1)};
  if (reconstruct_candidate_itinerary(input, incomplete).has_value() ||
      reconstruct_candidate_itinerary(input, non_generated).has_value()) {
    return 1;
  }

  auto duplicate_input = input;
  duplicate_input.preserved_prefix.front().activity_id =
      first.activity.activity_id;
  if (duplicate_input.is_valid()) {
    return 1;
  }

  auto in_progress_input = input;
  in_progress_input.preserved_prefix.front().scheduled_end =
      UnixTimeMilliseconds{2000};
  const std::vector<ExpansionDecision> overlaps_preserved{
      skipped(7), scheduled(3, 0, 1000), skipped(1)};
  const std::vector<ExpansionDecision> follows_preserved{
      skipped(7), scheduled(3, 2000, 3000), skipped(1)};
  if (in_progress_input.suffix_start_time() !=
          UnixTimeMilliseconds{2000} ||
      reconstruct_candidate_itinerary(in_progress_input,
                                      overlaps_preserved)
          .has_value() ||
      !reconstruct_candidate_itinerary(in_progress_input, follows_preserved)
           .has_value()) {
    return 1;
  }

  auto unreachable_estimates =
      std::vector<RouteEstimate>(
          16, RouteEstimate{std::chrono::seconds{0}, 0, true});
  unreachable_estimates[3] =
      RouteEstimate{std::chrono::seconds{0}, 0, false};
  const TravelTimeMatrix unreachable_matrix(4,
                                             std::move(unreachable_estimates));
  auto unreachable_input = input;
  unreachable_input.travel_time_matrix = &unreachable_matrix;
  return reconstruct_candidate_itinerary(unreachable_input, decisions)
                 .has_value()
             ? 1
             : 0;
}
