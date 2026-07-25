#include "liveroute/planner/beam_search.hpp"

#include <array>
#include <optional>

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
using liveroute::planner::PlanningActivity;

PlanningActivity fixture_activity() {
  std::array<std::byte, 16> bytes{};
  bytes.front() = std::byte{1};
  const auto id = ActivityId{bytes};
  return {.activity = {.activity_id = id, .place_id = PlaceId{"p"}, .display_name = "p",
                       .location = Location{40, -74}, .time_zone_name = "America/New_York",
                       .inbound_travel_mode = TravelMode::kWalking,
                       .activity_class = ActivityClass::kFlexible,
                       .activity_state = ActivityState::kPlanned,
                       .priority_rank = 0, .utility_score = 1,
                       .timing = ActivityTiming{.open_windows = {{UnixTimeMilliseconds{0},
                                                                  UnixTimeMilliseconds{10000}}},
                                                .reservation_start = std::nullopt,
                                                .reservation_grace_seconds = 0,
                                                .min_duration_seconds = 1,
                                                .preferred_duration_seconds = 1,
                                                .max_duration_seconds = 1,
                                                .can_shorten = true, .can_move = true,
                                                .can_skip = true,
                                                .mandatory_deadline = std::nullopt},
                       .activity_delay_seconds = 0,
                       .found_closed_at = std::nullopt},
          .original_trip_ordinal = 0,
          .current_plan_segment = {.activity_id = id, .state = PlanEntryState::kOmitted,
                                   .scheduled_start = std::nullopt,
                                   .scheduled_end = std::nullopt}};
}

}  // namespace

int main() {
  const TravelTimeMatrix matrix(2, {{}, {std::chrono::seconds{1}, 1, true},
                                    {std::chrono::seconds{1}, 1, true}, {}});
  auto input = BeamSearchInput{.current_time = UnixTimeMilliseconds{0},
                               .planning_horizon_start = UnixTimeMilliseconds{0},
                               .planning_horizon_end = UnixTimeMilliseconds{10000},
                               .preserved_prefix = {},
                               .remaining_activities = {fixture_activity()},
                               .travel_time_matrix = &matrix};
  auto invalid = input;
  invalid.travel_time_matrix = nullptr;
  return input.is_valid() && !invalid.is_valid() ? 0 : 1;
}
