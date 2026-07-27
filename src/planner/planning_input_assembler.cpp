#include "liveroute/planner/planning_input_assembler.hpp"

#include <cstddef>
#include <optional>
#include <utility>
#include <vector>

namespace liveroute::planner {

namespace {

struct ActivityLookup {
  const domain::Activity* activity;
  std::size_t original_trip_ordinal;
};

[[nodiscard]] std::optional<ActivityLookup> activity_for_id(
    const domain::TripState& state,
    const domain::ActivityId& activity_id) noexcept {
  for (std::size_t ordinal = 0; ordinal < state.activities.size(); ++ordinal) {
    if (state.activities[ordinal].activity_id == activity_id) {
      return ActivityLookup{.activity = &state.activities[ordinal],
                            .original_trip_ordinal = ordinal};
    }
  }
  return std::nullopt;
}

[[nodiscard]] bool is_terminal(domain::ActivityState state) noexcept {
  return state == domain::ActivityState::kCompleted ||
         state == domain::ActivityState::kSkipped;
}

}  // namespace

std::optional<BeamSearchInput> assemble_beam_search_input(
    const domain::TripState& state,
    domain::UnixTimeMilliseconds current_time,
    domain::UnixTimeMilliseconds planning_horizon_start,
    domain::UnixTimeMilliseconds planning_horizon_end,
    const domain::TravelTimeMatrix& travel_time_matrix) {
  if (!state.is_valid() ||
      state.completed_prefix_count > state.current_plan.segments.size()) {
    return std::nullopt;
  }

  std::size_t preserved_count = state.completed_prefix_count;
  for (std::size_t index = 0; index < state.current_plan.segments.size();
       ++index) {
    const auto activity =
        activity_for_id(state, state.current_plan.segments[index].activity_id);
    if (!activity) return std::nullopt;
    if (index < state.completed_prefix_count) {
      if (!is_terminal(activity->activity->activity_state)) {
        return std::nullopt;
      }
    } else if (is_terminal(activity->activity->activity_state)) {
      return std::nullopt;
    }
  }

  if (state.current_activity_id.has_value()) {
    if (preserved_count >= state.current_plan.segments.size() ||
        state.current_plan.segments[preserved_count].activity_id !=
            *state.current_activity_id) {
      return std::nullopt;
    }
    const auto current = activity_for_id(state, *state.current_activity_id);
    if (!current ||
        current->activity->activity_state != domain::ActivityState::kStarted) {
      return std::nullopt;
    }
    ++preserved_count;
  }

  BeamSearchInput input{
      .current_time = current_time,
      .planning_horizon_start = planning_horizon_start,
      .planning_horizon_end = planning_horizon_end,
      .preserved_prefix = {},
      .remaining_activities = {},
      .travel_time_matrix = &travel_time_matrix,
  };
  input.preserved_prefix.assign(
      state.current_plan.segments.begin(),
      state.current_plan.segments.begin() +
          static_cast<std::ptrdiff_t>(preserved_count));
  input.remaining_activities.reserve(state.current_plan.segments.size() -
                                     preserved_count);
  for (std::size_t index = preserved_count;
       index < state.current_plan.segments.size(); ++index) {
    const auto& segment = state.current_plan.segments[index];
    const auto activity = activity_for_id(state, segment.activity_id);
    if (!activity ||
        activity->activity->activity_state ==
            domain::ActivityState::kStarted) {
      return std::nullopt;
    }
    input.remaining_activities.push_back(
        {.activity = *activity->activity,
         .original_trip_ordinal = activity->original_trip_ordinal,
         .current_plan_segment = segment});
  }
  return input.is_valid()
             ? std::optional<BeamSearchInput>{std::move(input)}
             : std::nullopt;
}

}  // namespace liveroute::planner
