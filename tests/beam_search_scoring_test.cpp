#include "liveroute/planner/beam_search.hpp"

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
using liveroute::planner::PlannerScoreScratch;
using liveroute::planner::score_candidate;

ActivityId activity_id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return ActivityId{bytes};
}

PlanningActivity activity_fixture(
    std::uint8_t marker, std::size_t original_ordinal,
    std::int32_t priority_rank, std::int32_t utility_score,
    PlanEntryState baseline_state,
    std::optional<std::int64_t> baseline_start = std::nullopt,
    std::optional<std::int64_t> baseline_end = std::nullopt) {
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
           .priority_rank = priority_rank,
           .utility_score = utility_score,
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
                   .can_shorten = true,
                   .can_move = true,
                   .can_skip = true,
                   .mandatory_deadline = std::nullopt},
           .activity_delay_seconds = 0,
           .found_closed_at = std::nullopt},
      .original_trip_ordinal = original_ordinal,
      .current_plan_segment =
          {.activity_id = id,
           .state = baseline_state,
           .scheduled_start =
               baseline_start.has_value()
                   ? std::optional<UnixTimeMilliseconds>{
                         UnixTimeMilliseconds{*baseline_start}}
                   : std::nullopt,
           .scheduled_end =
               baseline_end.has_value()
                   ? std::optional<UnixTimeMilliseconds>{
                         UnixTimeMilliseconds{*baseline_end}}
                   : std::nullopt},
  };
}

TravelTimeMatrix reachable_zero_matrix(std::size_t location_count) {
  return TravelTimeMatrix(
      location_count,
      std::vector<RouteEstimate>(
          location_count * location_count,
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
  const auto first =
      activity_fixture(1, 7, 0, 10, PlanEntryState::kScheduled, 0, 1000);
  const auto second =
      activity_fixture(2, 1, 1, 20, PlanEntryState::kScheduled, 2000, 3000);
  const auto inserted =
      activity_fixture(3, 3, 1, 30, PlanEntryState::kOmitted);
  const auto matrix = reachable_zero_matrix(4);
  const auto input = BeamSearchInput{
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{10000},
      .preserved_prefix = {},
      // This is authoritative current-plan order, deliberately not ordinal order.
      .remaining_activities = {first, second, inserted},
      .travel_time_matrix = &matrix,
  };

  const std::vector<ExpansionDecision> inserted_between{
      scheduled(7, 0, 1000), scheduled(3, 1000, 2000),
      scheduled(1, 2000, 3000)};
  const auto insertion_score = score_candidate(input, inserted_between);
  if (!insertion_score || insertion_score->changed_activity_count != 1 ||
      insertion_score->total_start_shift_ms != 0 ||
      insertion_score->scheduled_utility != 60 ||
      insertion_score->canonical_plan_key.scheduled_ordinals_in_order !=
             std::vector<std::size_t>({7, 3, 1})) {
    return 1;
  }

  PlannerScoreScratch reusable_score_scratch;
  const auto insertion_score_reused =
      score_candidate(input, inserted_between, reusable_score_scratch);
  const auto omission_score_reused =
      score_candidate(input, std::vector<ExpansionDecision>{
                               scheduled(7, 0, 1000), skipped(1), skipped(3)},
                       reusable_score_scratch);
  if (!insertion_score_reused || !omission_score_reused ||
      insertion_score_reused->skips_by_priority !=
          insertion_score->skips_by_priority ||
      insertion_score_reused->scheduled_utility !=
          insertion_score->scheduled_utility ||
      insertion_score_reused->total_lateness_ms !=
          insertion_score->total_lateness_ms ||
      insertion_score_reused->total_preferred_shortfall_ms !=
          insertion_score->total_preferred_shortfall_ms ||
      insertion_score_reused->total_travel_ms != insertion_score->total_travel_ms ||
      insertion_score_reused->changed_activity_count !=
          insertion_score->changed_activity_count ||
      insertion_score_reused->total_start_shift_ms !=
          insertion_score->total_start_shift_ms ||
      insertion_score_reused->final_scheduled_end_unix_ms !=
          insertion_score->final_scheduled_end_unix_ms ||
      insertion_score_reused->canonical_plan_key !=
          insertion_score->canonical_plan_key ||
      omission_score_reused->scheduled_utility != 10 ||
      omission_score_reused->changed_activity_count != 1) {
    return 1;
  }

  const std::vector<ExpansionDecision> partial{scheduled(7, 0, 1000)};
  const auto partial_score = score_candidate(input, partial);
  if (!partial_score || partial_score->scheduled_utility != 60 ||
      partial_score->changed_activity_count != 0 ||
      partial_score->final_scheduled_end_unix_ms != 1000) {
    return 1;
  }

  const std::vector<ExpansionDecision> omissions{
      scheduled(7, 0, 1000), skipped(1), skipped(3)};
  const auto omission_score = score_candidate(input, omissions);
  if (!omission_score || omission_score->changed_activity_count != 1 ||
      omission_score->scheduled_utility != 10 ||
      omission_score->skips_by_priority !=
          std::vector<std::uint32_t>({0, 2}) ||
      omission_score->canonical_plan_key.skipped_ordinals !=
          std::vector<std::size_t>({1, 3})) {
    return 1;
  }

  const std::vector<ExpansionDecision> duplicate{
      scheduled(7, 0, 1000), scheduled(7, 0, 1000)};
  const std::vector<ExpansionDecision> non_generated{
      scheduled(7, 500, 1500)};
  if (score_candidate(input, duplicate).has_value() ||
      score_candidate(input, non_generated).has_value()) {
    return 1;
  }

  auto travel_estimates =
      std::vector<RouteEstimate>(
          9, RouteEstimate{std::chrono::seconds{0}, 0, true});
  travel_estimates[1] = RouteEstimate{std::chrono::seconds{2}, 1, true};
  travel_estimates[1 * 3 + 2] =
      RouteEstimate{std::chrono::seconds{3}, 1, true};
  const TravelTimeMatrix travel_matrix(3, std::move(travel_estimates));
  const auto travel_first =
      activity_fixture(4, 4, 0, 1, PlanEntryState::kOmitted);
  const auto travel_second =
      activity_fixture(5, 5, 0, 1, PlanEntryState::kOmitted);
  const auto travel_input = BeamSearchInput{
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{10000},
      .preserved_prefix = {},
      .remaining_activities = {travel_first, travel_second},
      .travel_time_matrix = &travel_matrix,
  };
  const std::vector<ExpansionDecision> travel_decisions{
      scheduled(4, 2000, 3000), scheduled(5, 6000, 7000)};
  const auto travel_score = score_candidate(travel_input, travel_decisions);
  return travel_score && travel_score->total_travel_ms == 5000 &&
                 travel_score->changed_activity_count == 2
             ? 0
             : 1;
}
