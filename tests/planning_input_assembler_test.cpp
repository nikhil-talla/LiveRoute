#include "liveroute/planner/planning_input_assembler.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <utility>
#include <vector>

namespace {

using namespace liveroute::domain;
using liveroute::planner::assemble_beam_search_input;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

Activity activity(std::uint8_t marker, ActivityState state) {
  return {
      .activity_id = id<ActivityId>(marker),
      .place_id = PlaceId{"place"},
      .display_name = "Place",
      .location = Location{40.0 + marker, -74.0},
      .time_zone_name = "America/New_York",
      .inbound_travel_mode = TravelMode::kWalking,
      .activity_class = ActivityClass::kFlexible,
      .activity_state = state,
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

CurrentPlanSegment scheduled(const Activity& activity, std::int64_t start,
                             std::int64_t end) {
  return {.activity_id = activity.activity_id,
          .state = PlanEntryState::kScheduled,
          .scheduled_start = UnixTimeMilliseconds{start},
          .scheduled_end = UnixTimeMilliseconds{end}};
}

CurrentPlanSegment omitted(const Activity& activity) {
  return {.activity_id = activity.activity_id,
          .state = PlanEntryState::kOmitted,
          .scheduled_start = std::nullopt,
          .scheduled_end = std::nullopt};
}

TravelTimeMatrix matrix(std::size_t locations) {
  return TravelTimeMatrix{
      locations,
      std::vector<RouteEstimate>(
          locations * locations,
          RouteEstimate{std::chrono::seconds{0}, 0, true})};
}

TripState state_fixture() {
  const auto completed = activity(1, ActivityState::kCompleted);
  const auto future_by_definition = activity(2, ActivityState::kPlanned);
  const auto started = activity(3, ActivityState::kStarted);
  const auto omitted_future = activity(4, ActivityState::kPlanned);
  std::vector<Activity> activities{completed, future_by_definition, started,
                                   omitted_future};
  return {
      .trip_id = id<TripId>(20),
      .default_time_zone_name = "America/New_York",
      .activities = activities,
      .completed_prefix_count = 1,
      .current_activity_id = started.activity_id,
      .current_plan =
          {.plan_id = id<PlanId>(21),
           .plan_revision = 1,
           .origin = PlanOrigin::kUserAuthored,
           .segments = {scheduled(completed, -2000, -1000),
                        scheduled(started, -500, 500),
                        omitted(omitted_future),
                        scheduled(future_by_definition, 1000, 2000)},
           .created_at = UnixTimeMilliseconds{-3000},
           .source_proposal_id = std::nullopt},
      .travel_delays = {},
      .current_observation = {},
      .active_proposal = std::nullopt,
  };
}

}  // namespace

int main() {
  const auto state = state_fixture();
  const auto travel = matrix(3);
  const auto input =
      assemble_beam_search_input(state, UnixTimeMilliseconds{0},
                                 UnixTimeMilliseconds{0},
                                 UnixTimeMilliseconds{10000}, travel);
  if (!input || !input->is_valid() || input->preserved_prefix.size() != 2 ||
      input->preserved_prefix[0].activity_id != id<ActivityId>(1) ||
      input->preserved_prefix[1].activity_id != id<ActivityId>(3) ||
      input->remaining_activities.size() != 2 ||
      input->remaining_activities[0].activity.activity_id != id<ActivityId>(4) ||
      input->remaining_activities[0].original_trip_ordinal != 3 ||
      input->remaining_activities[0].current_plan_segment.state !=
          PlanEntryState::kOmitted ||
      input->remaining_activities[1].activity.activity_id != id<ActivityId>(2) ||
      input->remaining_activities[1].original_trip_ordinal != 1 ||
      input->suffix_start_time() != UnixTimeMilliseconds{500}) {
    return 1;
  }

  auto started_out_of_order = state;
  std::swap(started_out_of_order.current_plan.segments[1],
            started_out_of_order.current_plan.segments[2]);
  auto nonterminal_prefix = state;
  nonterminal_prefix.activities[0].activity_state = ActivityState::kPlanned;
  auto terminal_suffix = state;
  terminal_suffix.activities[3].activity_state = ActivityState::kSkipped;
  auto started_without_current = state;
  started_without_current.current_activity_id = std::nullopt;
  const auto wrong_matrix = matrix(4);
  return assemble_beam_search_input(
             started_out_of_order, UnixTimeMilliseconds{0},
             UnixTimeMilliseconds{0}, UnixTimeMilliseconds{10000}, travel)
                 .has_value() ||
                 assemble_beam_search_input(
                     nonterminal_prefix, UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{10000}, travel)
                     .has_value() ||
                 assemble_beam_search_input(
                     terminal_suffix, UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{10000}, travel)
                     .has_value() ||
                 assemble_beam_search_input(
                     started_without_current, UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{10000}, travel)
                     .has_value() ||
                 assemble_beam_search_input(
                     state, UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{0},
                     UnixTimeMilliseconds{10000}, wrong_matrix)
                     .has_value()
             ? 1
             : 0;
}
