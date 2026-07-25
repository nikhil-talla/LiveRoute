#include "liveroute/planner/beam_search.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <vector>

namespace {

using liveroute::domain::Activity;
using liveroute::domain::ActivityClass;
using liveroute::domain::ActivityId;
using liveroute::domain::ActivityState;
using liveroute::domain::ActivityTiming;
using liveroute::domain::CurrentPlanSegment;
using liveroute::domain::Location;
using liveroute::domain::PlaceId;
using liveroute::domain::PlanEntryState;
using liveroute::domain::RouteEstimate;
using liveroute::domain::TimeWindow;
using liveroute::domain::TravelMode;
using liveroute::domain::TravelTimeMatrix;
using liveroute::domain::UnixTimeMilliseconds;
using liveroute::planner::BeamSearchInput;
using liveroute::planner::CandidateAlternative;
using liveroute::planner::CandidateAlternativeKind;
using liveroute::planner::PlanningActivity;
using liveroute::planner::generate_candidate_alternatives;

ActivityId activity_id() {
  std::array<std::byte, 16> bytes{};
  bytes.front() = std::byte{1};
  return ActivityId{bytes};
}

PlanningActivity activity_fixture(ActivityTiming timing,
                                  ActivityClass activity_class = ActivityClass::kFlexible,
                                  PlanEntryState state = PlanEntryState::kOmitted,
                                  std::optional<std::int64_t> start = std::nullopt,
                                  std::optional<std::int64_t> end = std::nullopt) {
  const auto id = activity_id();
  return {.activity = {.activity_id = id,
                       .place_id = PlaceId{"place"},
                       .display_name = "Place",
                       .location = Location{40, -74},
                       .time_zone_name = "America/New_York",
                       .inbound_travel_mode = TravelMode::kWalking,
                       .activity_class = activity_class,
                       .activity_state = ActivityState::kPlanned,
                       .priority_rank = 0,
                       .utility_score = 1,
                       .timing = std::move(timing),
                       .activity_delay_seconds = 0,
                       .found_closed_at = std::nullopt},
          .original_trip_ordinal = 4,
          .current_plan_segment = {
              .activity_id = id,
              .state = state,
              .scheduled_start = start.has_value()
                                     ? std::optional<UnixTimeMilliseconds>{
                                           UnixTimeMilliseconds{*start}}
                                     : std::nullopt,
              .scheduled_end = end.has_value()
                                   ? std::optional<UnixTimeMilliseconds>{
                                         UnixTimeMilliseconds{*end}}
                                   : std::nullopt,
          }};
}

BeamSearchInput input_for(const PlanningActivity& activity,
                          const TravelTimeMatrix* matrix) {
  return {.current_time = UnixTimeMilliseconds{0},
          .planning_horizon_start = UnixTimeMilliseconds{0},
          .planning_horizon_end = UnixTimeMilliseconds{16000},
          .preserved_prefix = {},
          .remaining_activities = {activity},
          .travel_time_matrix = matrix};
}

bool is_scheduled(const CandidateAlternative& alternative, std::int64_t start,
                  std::int64_t end, bool exact = false) {
  return alternative.kind == CandidateAlternativeKind::kScheduled &&
         alternative.start == UnixTimeMilliseconds{start} &&
         alternative.end == UnixTimeMilliseconds{end} &&
         alternative.is_exact_current_plan == exact;
}

}  // namespace

int main() {
  const TravelTimeMatrix matrix(2, {{}, {std::chrono::seconds{1}, 1, true},
                                    {std::chrono::seconds{1}, 1, true}, {}});
  const auto boundary_activity = activity_fixture(
      {.open_windows = {{UnixTimeMilliseconds{1000}, UnixTimeMilliseconds{9000}},
                        {UnixTimeMilliseconds{10000}, UnixTimeMilliseconds{16000}}},
       .reservation_start = UnixTimeMilliseconds{2200},
       .reservation_grace_seconds = 5,
       .min_duration_seconds = 2,
       .preferred_duration_seconds = 5,
       .max_duration_seconds = 7,
       .mandatory = false,
       .can_shorten = true,
       .can_move = true,
       .can_skip = true,
       .mandatory_deadline = std::nullopt},
      ActivityClass::kFlexible, PlanEntryState::kScheduled, 4000, 9000);
  const auto boundary_alternatives = generate_candidate_alternatives(
      input_for(boundary_activity, &matrix), boundary_activity,
      UnixTimeMilliseconds{2000});
  if (boundary_alternatives.size() != 5 ||
      !is_scheduled(boundary_alternatives[0], 2200, 7200) ||
      !is_scheduled(boundary_alternatives[1], 2200, 4200) ||
      !is_scheduled(boundary_alternatives[2], 4000, 9000, true) ||
      !is_scheduled(boundary_alternatives[3], 4000, 6000) ||
      boundary_alternatives[4].kind != CandidateAlternativeKind::kSkipped) {
    return 1;
  }

  const auto zero_duration_activity = activity_fixture(
      {.open_windows = {{UnixTimeMilliseconds{0}, UnixTimeMilliseconds{10000}}},
       .reservation_start = std::nullopt,
       .reservation_grace_seconds = 0,
       .min_duration_seconds = 0,
       .preferred_duration_seconds = 0,
       .max_duration_seconds = 5,
       .mandatory = false,
       .can_shorten = true,
       .can_move = true,
       .can_skip = true,
       .mandatory_deadline = UnixTimeMilliseconds{3500}});
  const auto zero_duration_alternatives = generate_candidate_alternatives(
      input_for(zero_duration_activity, &matrix), zero_duration_activity,
      UnixTimeMilliseconds{0});
  if (zero_duration_alternatives.size() != 2 ||
      !is_scheduled(zero_duration_alternatives[0], 0, 1000) ||
      zero_duration_alternatives[1].kind != CandidateAlternativeKind::kSkipped) {
    return 1;
  }

  const auto fixed_without_anchor = activity_fixture(
      {.open_windows = {{UnixTimeMilliseconds{0}, UnixTimeMilliseconds{10000}}},
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
      ActivityClass::kFixed);
  const auto fixed_alternatives = generate_candidate_alternatives(
      input_for(fixed_without_anchor, &matrix), fixed_without_anchor,
      UnixTimeMilliseconds{0});
  if (fixed_alternatives.size() != 1 ||
      fixed_alternatives.front().kind != CandidateAlternativeKind::kSkipped) {
    return 1;
  }

  auto immovable_omitted = fixed_without_anchor;
  immovable_omitted.activity.activity_class = ActivityClass::kFlexible;
  immovable_omitted.activity.timing.can_move = false;
  const auto immovable_alternatives = generate_candidate_alternatives(
      input_for(immovable_omitted, &matrix), immovable_omitted,
      UnixTimeMilliseconds{0});
  return immovable_alternatives.size() == 1 &&
                 immovable_alternatives.front().kind ==
                     CandidateAlternativeKind::kSkipped
             ? 0
             : 1;
}
