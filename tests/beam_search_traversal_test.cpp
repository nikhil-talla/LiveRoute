#include "liveroute/planner/beam_search.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <stop_token>
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
using liveroute::planner::BeamSearchOutcome;
using liveroute::planner::PlannerScratch;
using liveroute::planner::PlanningActivity;
using liveroute::planner::ReplanBudget;
using liveroute::planner::run_beam_search;

ActivityId activity_id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return ActivityId{bytes};
}

PlanningActivity activity(
    std::uint8_t marker, std::size_t ordinal, std::int32_t priority,
    ActivityClass activity_class, bool mandatory, bool can_skip,
    std::int64_t opens_at, std::int64_t closes_at,
    std::uint32_t duration_seconds, PlanEntryState baseline_state,
    std::optional<std::int64_t> baseline_start = std::nullopt,
    std::optional<std::int64_t> baseline_end = std::nullopt,
    std::optional<std::int64_t> reservation_start = std::nullopt) {
  const auto id = activity_id(marker);
  return {
      .activity =
          {.activity_id = id,
           .place_id = PlaceId{"place"},
           .display_name = "Place",
           .location = Location{40, -74},
           .time_zone_name = "America/New_York",
           .inbound_travel_mode = TravelMode::kWalking,
           .activity_class = activity_class,
           .activity_state = ActivityState::kPlanned,
           .priority_rank = priority,
           .utility_score = 1,
           .timing =
               ActivityTiming{
                   .open_windows = {{UnixTimeMilliseconds{opens_at},
                                     UnixTimeMilliseconds{closes_at}}},
                   .reservation_start =
                       reservation_start.has_value()
                           ? std::optional<UnixTimeMilliseconds>{
                                 UnixTimeMilliseconds{*reservation_start}}
                           : std::nullopt,
                   .reservation_grace_seconds = 0,
                   .min_duration_seconds = duration_seconds,
                   .preferred_duration_seconds = duration_seconds,
                   .max_duration_seconds = duration_seconds,
                   .mandatory = mandatory,
                   .can_shorten = false,
                   .can_move = true,
                   .can_skip = can_skip,
                   .mandatory_deadline = std::nullopt},
           .activity_delay_seconds = 0,
           .found_closed_at = std::nullopt},
      .original_trip_ordinal = ordinal,
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

TravelTimeMatrix zero_matrix(std::size_t locations) {
  return TravelTimeMatrix(
      locations,
      std::vector<RouteEstimate>(
          locations * locations,
          RouteEstimate{std::chrono::seconds{0}, 0, true}));
}

ReplanBudget budget(std::size_t beam_width = 32,
                    std::size_t max_expansions = 1000,
                    std::size_t max_candidates = 1000,
                    std::stop_token stop_token = {}) {
  return {
      .deadline = std::chrono::steady_clock::now() + std::chrono::minutes{1},
      .max_candidates = max_candidates,
      .beam_width = beam_width,
      .max_expansions = max_expansions,
      .stop_token = stop_token,
  };
}

bool contains_skip(
    const std::vector<liveroute::planner::ExpansionDecision>& decisions,
    std::size_t ordinal) {
  for (const auto& decision : decisions) {
    if (decision.activity_ordinal == ordinal && decision.decision == 1) {
      return true;
    }
  }
  return false;
}

bool contains_schedule(
    const std::vector<liveroute::planner::ExpansionDecision>& decisions,
    std::size_t ordinal, std::int64_t start, std::int64_t end) {
  for (const auto& decision : decisions) {
    if (decision.activity_ordinal == ordinal && decision.decision == 0 &&
        decision.start_unix_ms == start && decision.end_unix_ms == end) {
      return true;
    }
  }
  return false;
}

}  // namespace

int main() {
  {
    const auto first = activity(
        12, 1, 0, ActivityClass::kFlexible, false, true, 0, 10000, 2,
        PlanEntryState::kScheduled, 0, 2000);
    const auto second = activity(
        13, 2, 10, ActivityClass::kFlexible, false, true, 0, 10000, 2,
        PlanEntryState::kScheduled, 3000, 5000);
    auto estimates = std::vector<RouteEstimate>(
        9, RouteEstimate{std::chrono::seconds{0}, 0, true});
    estimates[0 * 3 + 1] =
        RouteEstimate{std::chrono::seconds{2}, 1, true};
    estimates[0 * 3 + 2] =
        RouteEstimate{std::chrono::seconds{2}, 1, true};
    estimates[1 * 3 + 2] =
        RouteEstimate{std::chrono::seconds{1}, 1, true};
    estimates[2 * 3 + 1] =
        RouteEstimate{std::chrono::seconds{1}, 1, true};
    const TravelTimeMatrix matrix(3, std::move(estimates));
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{1000},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{10000},
        .preserved_prefix = {},
        .remaining_activities = {first, second},
        .travel_time_matrix = &matrix,
    };
    const auto result = run_beam_search(input, budget());
    if (result.outcome != BeamSearchOutcome::kComplete ||
        !result.best_decisions || contains_skip(*result.best_decisions, 1) ||
        contains_skip(*result.best_decisions, 2) ||
        !contains_schedule(*result.best_decisions, 1, 3000, 5000) ||
        !contains_schedule(*result.best_decisions, 2, 6000, 8000)) {
      return 1;
    }
  }

  {
    const auto optional = activity(1, 1, 10, ActivityClass::kFlexible,
                                   false, true, 0, 7000, 4,
                                   PlanEntryState::kOmitted);
    const auto fixed = activity(2, 2, 0, ActivityClass::kFixed, true,
                                false, 0, 10000, 1,
                                PlanEntryState::kScheduled, 5000, 6000);
    const auto matrix = zero_matrix(3);
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{3000},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{10000},
        .preserved_prefix = {},
        .remaining_activities = {optional, fixed},
        .travel_time_matrix = &matrix,
    };
    const auto result = run_beam_search(input, budget(1));
    if (result.outcome != BeamSearchOutcome::kComplete ||
        !result.search_was_truncated || !result.best_decisions ||
        !contains_skip(*result.best_decisions, 1)) {
      return 1;
    }
  }

  {
    const auto important = activity(8, 1, 0, ActivityClass::kFlexible,
                                    false, true, 0, 5000, 4,
                                    PlanEntryState::kOmitted);
    const auto less_important = activity(
        9, 2, 10, ActivityClass::kFlexible, false, true, 0, 5000, 4,
        PlanEntryState::kOmitted);
    const auto fixed = activity(10, 3, 0, ActivityClass::kFixed, true,
                                false, 0, 10000, 1,
                                PlanEntryState::kScheduled, 5000, 6000);
    const auto matrix = zero_matrix(4);
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{0},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{10000},
        .preserved_prefix = {},
        .remaining_activities = {important, less_important, fixed},
        .travel_time_matrix = &matrix,
    };
    PlannerScratch scratch;
    const auto result = run_beam_search(input, budget(), scratch);
    const auto first_path_capacity = scratch.path_nodes.capacity();
    const auto repeated = run_beam_search(input, budget(), scratch);
    if (result.outcome != BeamSearchOutcome::kComplete ||
        !result.best_decisions || contains_skip(*result.best_decisions, 1) ||
        !contains_skip(*result.best_decisions, 2) ||
        repeated.outcome != result.outcome ||
        repeated.best_decisions != result.best_decisions ||
        repeated.expansion_count != result.expansion_count ||
        repeated.candidate_count != result.candidate_count ||
        repeated.search_was_truncated != result.search_was_truncated ||
        scratch.path_nodes.capacity() < first_path_capacity) {
      return 1;
    }
  }

  {
    const auto first = activity(3, 1, 0, ActivityClass::kFlexible,
                                false, true, 0, 5000, 1,
                                PlanEntryState::kScheduled, 0, 1000);
    const auto protected_anchor = activity(
        4, 2, 0, ActivityClass::kFixed, true, false, 0, 5000, 1,
        PlanEntryState::kOmitted, std::nullopt, std::nullopt, 2000);
    auto estimates = std::vector<RouteEstimate>(
        9, RouteEstimate{std::chrono::seconds{0}, 0, true});
    estimates[1 * 3 + 2] =
        RouteEstimate{std::chrono::seconds{2}, 1, true};
    const TravelTimeMatrix matrix(3, std::move(estimates));
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{0},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{5000},
        .preserved_prefix = {},
        .remaining_activities = {first, protected_anchor},
        .travel_time_matrix = &matrix,
    };

    const auto limited = run_beam_search(input, budget(1));
    if (limited.outcome != BeamSearchOutcome::kSearchLimited ||
        limited.has_complete_candidate() || !limited.search_was_truncated) {
      return 1;
    }
    const auto widened = run_beam_search(input, budget(2));
    if (widened.outcome != BeamSearchOutcome::kComplete ||
        !widened.has_complete_candidate()) {
      return 1;
    }
  }

  {
    const auto impossible = activity(
        5, 1, 0, ActivityClass::kFixed, true, false, 0, 10000, 1,
        PlanEntryState::kScheduled, 5000, 6000);
    const auto matrix = zero_matrix(2);
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{7000},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{10000},
        .preserved_prefix = {},
        .remaining_activities = {impossible},
        .travel_time_matrix = &matrix,
    };
    const auto result = run_beam_search(input, budget());
    if (result.outcome != BeamSearchOutcome::kExhaustiveInfeasible ||
        result.search_was_truncated) {
      return 1;
    }
  }

  {
    const auto first = activity(6, 1, 0, ActivityClass::kFlexible,
                                false, true, 0, 10000, 1,
                                PlanEntryState::kOmitted);
    const auto second = activity(7, 2, 0, ActivityClass::kFlexible,
                                 false, true, 0, 10000, 1,
                                 PlanEntryState::kOmitted);
    const auto matrix = zero_matrix(3);
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{0},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{10000},
        .preserved_prefix = {},
        .remaining_activities = {first, second},
        .travel_time_matrix = &matrix,
    };
    const auto result = run_beam_search(input, budget(32, 1));
    if (result.outcome != BeamSearchOutcome::kSearchLimited ||
        result.has_complete_candidate()) {
      return 1;
    }

    std::stop_source stop_source;
    stop_source.request_stop();
    const auto cancelled =
        run_beam_search(input, budget(32, 1000, 1000,
                                      stop_source.get_token()));
    if (cancelled.outcome != BeamSearchOutcome::kCancelled ||
        !cancelled.cancellation_requested || cancelled.deadline_hit) {
      return 1;
    }

    auto expired_budget = budget();
    expired_budget.deadline =
        std::chrono::steady_clock::now() - std::chrono::milliseconds{1};
    const auto expired = run_beam_search(input, expired_budget);
    if (expired.outcome != BeamSearchOutcome::kDeadlineExceeded ||
        !expired.deadline_hit || expired.cancellation_requested) {
      return 1;
    }
  }

  {
    const auto flexible = activity(
        11, 1, 0, ActivityClass::kFlexible, false, true, 0, 10000, 1,
        PlanEntryState::kScheduled, 2000, 3000);
    const auto matrix = zero_matrix(2);
    const BeamSearchInput input{
        .current_time = UnixTimeMilliseconds{0},
        .planning_horizon_start = UnixTimeMilliseconds{0},
        .planning_horizon_end = UnixTimeMilliseconds{10000},
        .preserved_prefix = {},
        .remaining_activities = {flexible},
        .travel_time_matrix = &matrix,
    };
    const auto result = run_beam_search(input, budget(32, 1000, 1));
    if (result.outcome != BeamSearchOutcome::kBestSoFar ||
        !result.has_complete_candidate() || result.expansion_count != 1 ||
        result.candidate_count != 1 || result.deadline_hit ||
        result.cancellation_requested) {
      return 1;
    }
  }

  return 0;
}
