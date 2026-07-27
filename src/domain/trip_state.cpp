#include "liveroute/domain/trip_state.hpp"

#include <algorithm>
#include <cmath>
#include <utility>

namespace liveroute::domain {
namespace {

template <class... Visitors>
struct Overloaded : Visitors... {
  using Visitors::operator()...;
};

template <class... Visitors>
Overloaded(Visitors...) -> Overloaded<Visitors...>;

[[nodiscard]] Activity* activity_for(
    std::vector<Activity>& activities, const ActivityId& activity_id) noexcept {
  for (auto& activity : activities) {
    if (activity.activity_id == activity_id) return &activity;
  }
  return nullptr;
}

[[nodiscard]] const Activity* activity_for(
    std::span<const Activity> activities,
    const ActivityId& activity_id) noexcept {
  for (const auto& activity : activities) {
    if (activity.activity_id == activity_id) return &activity;
  }
  return nullptr;
}

[[nodiscard]] bool contains_activity(
    std::span<const Activity> activities, const ActivityId& activity_id) noexcept {
  return activity_for(activities, activity_id) != nullptr;
}

[[nodiscard]] std::optional<std::size_t> current_plan_index_for(
    const CurrentPlan& plan, const ActivityId& activity_id) noexcept {
  for (std::size_t index = 0; index < plan.segments.size(); ++index) {
    if (plan.segments[index].activity_id == activity_id) return index;
  }
  return std::nullopt;
}

[[nodiscard]] bool is_terminal(ActivityState state) noexcept {
  return state == ActivityState::kCompleted ||
         state == ActivityState::kSkipped;
}

[[nodiscard]] bool apply_activity_status_change(
    TripState& state, const ActivityStatusChanged& update) noexcept {
  auto* activity = activity_for(state.activities, update.activity_id);
  const auto plan_index =
      current_plan_index_for(state.current_plan, update.activity_id);
  if (activity == nullptr || !plan_index) return false;

  switch (update.state) {
    case ActivityState::kPlanned:
      if (*plan_index < state.completed_prefix_count) return false;
      activity->activity_state = update.state;
      if (state.current_activity_id ==
          std::optional<ActivityId>{update.activity_id}) {
        state.current_activity_id.reset();
      }
      return true;
    case ActivityState::kStarted:
      if (*plan_index != state.completed_prefix_count ||
          (state.current_activity_id.has_value() &&
           state.current_activity_id !=
               std::optional<ActivityId>{update.activity_id})) {
        return false;
      }
      activity->activity_state = update.state;
      state.current_activity_id = update.activity_id;
      return true;
    case ActivityState::kCompleted:
    case ActivityState::kSkipped:
      if (*plan_index != state.completed_prefix_count ||
          (state.current_activity_id.has_value() &&
           state.current_activity_id !=
               std::optional<ActivityId>{update.activity_id})) {
        return false;
      }
      activity->activity_state = update.state;
      ++state.completed_prefix_count;
      state.current_activity_id.reset();
      return true;
  }
  return false;
}

[[nodiscard]] bool same_delay_key(const TravelDelayState& delay,
                                  const TravelDelay& event) noexcept {
  return delay.from_activity_id == event.from_activity_id &&
         delay.to_activity_id == event.to_activity_id;
}

[[nodiscard]] bool matches_active_proposal(
    const StoredPlanProposal& active, const PlanDecisionEvent& event) noexcept {
  return active.proposal.proposal_id == event.proposal_id &&
         active.proposal.source_runtime_epoch == event.source_runtime_epoch &&
         active.proposal.source_planner_state_version ==
             event.source_planner_state_version &&
         active.proposal.base_current_plan_id == event.base_current_plan_id;
}

void update_observation_time(CurrentObservationState& observation,
                             UnixTimeMilliseconds occurred_at) {
  observation.observed_at = occurred_at;
}

void normalize_current_activity(TripState& state) {
  if (state.completed_prefix_count > state.activities.size()) {
    state.completed_prefix_count = state.activities.size();
  }
  if (state.current_activity_id.has_value() &&
      !contains_activity(state.activities, *state.current_activity_id)) {
    state.current_activity_id.reset();
  }
}

}  // namespace

bool CurrentObservationState::is_valid() const noexcept {
  if (location.has_value() && !location->is_valid()) return false;
  if (velocity_meters_per_second.has_value() &&
      (!std::isfinite(*velocity_meters_per_second) ||
       *velocity_meters_per_second < 0)) {
    return false;
  }
  if (heading_degrees.has_value() &&
      (!std::isfinite(*heading_degrees) || *heading_degrees < 0 ||
       *heading_degrees > 360)) {
    return false;
  }
  return true;
}

bool TripState::is_valid() const {
  if (default_time_zone_name.empty() || activities.size() > 64 ||
      completed_prefix_count > activities.size()) {
    return false;
  }

  std::vector<ActivityId> activity_ids;
  activity_ids.reserve(activities.size());
  for (const auto& activity : activities) {
    if (!activity.is_valid()) return false;
    activity_ids.push_back(activity.activity_id);
  }
  std::sort(activity_ids.begin(), activity_ids.end());
  if (std::adjacent_find(activity_ids.begin(), activity_ids.end()) !=
      activity_ids.end()) {
    return false;
  }
  if (!current_plan.is_valid_for(activity_ids)) return false;
  bool found_started_activity = false;
  for (std::size_t index = 0; index < current_plan.segments.size(); ++index) {
    const auto& segment = current_plan.segments[index];
    const auto* activity = activity_for(activities, segment.activity_id);
    if (activity == nullptr) return false;

    if (index < completed_prefix_count) {
      if (!is_terminal(activity->activity_state)) return false;
    } else if (is_terminal(activity->activity_state)) {
      return false;
    }

    if (activity->activity_state == ActivityState::kStarted) {
      if (found_started_activity || index != completed_prefix_count) {
        return false;
      }
      found_started_activity = true;
    }
  }
  if (current_activity_id.has_value()) {
    const auto current_index =
        current_plan_index_for(current_plan, *current_activity_id);
    const auto* current = activity_for(activities, *current_activity_id);
    if (!current_index || *current_index != completed_prefix_count ||
        current == nullptr || current->activity_state != ActivityState::kStarted ||
        !found_started_activity) {
      return false;
    }
  } else if (found_started_activity) {
    return false;
  }
  if (!current_observation.is_valid()) return false;

  std::vector<std::pair<ActivityId, ActivityId>> delay_keys;
  delay_keys.reserve(travel_delays.size());
  for (const auto& delay : travel_delays) {
    if (!contains_activity(activities, delay.from_activity_id) ||
        !contains_activity(activities, delay.to_activity_id)) {
      return false;
    }
    const auto key = std::make_pair(delay.from_activity_id, delay.to_activity_id);
    if (std::find(delay_keys.begin(), delay_keys.end(), key) !=
        delay_keys.end()) {
      return false;
    }
    delay_keys.push_back(key);
  }

  if (active_proposal.has_value()) {
    if (!active_proposal->is_valid_for(activities) ||
        active_proposal->proposal.base_current_plan_id != current_plan.plan_id) {
      return false;
    }
  }
  return true;
}

TripStateApplyResult apply_trip_event(
    TripState& state, const TripEvent& event,
    std::size_t max_advisory_payload_bytes) {
  if (!state.is_valid() ||
      !event.is_valid_for(state.activities, max_advisory_payload_bytes)) {
    return {TripStateApplyStatus::kInvalidArgument, false, false};
  }

  if (const auto* decision = std::get_if<PlanDecisionEvent>(&event.payload);
      decision != nullptr) {
    if (!state.active_proposal.has_value() ||
        !matches_active_proposal(*state.active_proposal, *decision)) {
      return {TripStateApplyStatus::kStaleProposal, false, false};
    }
  }

  TripState original_state = state;
  bool event_mutation_valid = true;
  bool planning_input_changed = true;
  bool current_plan_changed = false;
  std::visit(
      Overloaded{
          [&](const std::monostate&) { planning_input_changed = false; },
          [&](const LocationUpdated& update) {
            state.current_observation.location = update.location;
            update_observation_time(state.current_observation,
                                    event.occurred_at);
          },
          [&](const VelocityUpdated& update) {
            state.current_observation.velocity_meters_per_second =
                update.meters_per_second;
            update_observation_time(state.current_observation,
                                    event.occurred_at);
          },
          [&](const HeadingUpdated& update) {
            state.current_observation.heading_degrees = update.degrees;
            update_observation_time(state.current_observation,
                                    event.occurred_at);
          },
          [&](const ActivityStatusChanged& update) {
            event_mutation_valid =
                apply_activity_status_change(state, update);
          },
          [&](const ActivityDelayed& update) {
            activity_for(state.activities, update.activity_id)
                ->activity_delay_seconds = update.delay_seconds;
          },
          [&](const TripEdited& update) {
            std::visit(
                Overloaded{
                    [&](const AddActivity& add) {
                      state.activities.insert(
                          state.activities.begin() +
                              static_cast<std::ptrdiff_t>(add.ordinal),
                          add.activity);
                    },
                    [&](const ReplaceActivity& replace) {
                      *activity_for(state.activities,
                                    replace.activity.activity_id) =
                          replace.activity;
                    },
                    [&](const RemoveActivity& remove) {
                      const auto position = std::find_if(
                          state.activities.begin(), state.activities.end(),
                          [&](const Activity& activity) {
                            return activity.activity_id == remove.activity_id;
                          });
                      state.activities.erase(position);
                      state.travel_delays.erase(
                          std::remove_if(
                              state.travel_delays.begin(),
                              state.travel_delays.end(),
                              [&](const TravelDelayState& delay) {
                                return delay.from_activity_id ==
                                           remove.activity_id ||
                                       delay.to_activity_id ==
                                           remove.activity_id;
                              }),
                          state.travel_delays.end());
                    },
                    [&](const ReorderActivities& reorder) {
                      std::vector<Activity> reordered;
                      reordered.reserve(state.activities.size());
                      for (const auto& activity_id : reorder.activity_ids) {
                        reordered.push_back(
                            *activity_for(state.activities, activity_id));
                      }
                      state.activities = std::move(reordered);
                    }},
                update.operation);
            state.current_plan = update.resulting_current_plan;
            current_plan_changed = true;
            normalize_current_activity(state);
            state.active_proposal.reset();
          },
          [&](const ReservationChanged& update) {
            auto& timing =
                activity_for(state.activities, update.activity_id)->timing;
            timing.reservation_start = update.reservation_start;
            timing.reservation_grace_seconds = update.reservation_grace_seconds;
          },
          [&](const MandatoryDeadlineChanged& update) {
            activity_for(state.activities, update.activity_id)
                ->timing.mandatory_deadline = update.latest_finish;
          },
          [&](const RouteDeviationDetected& update) {
            state.current_observation.location = update.location;
            update_observation_time(state.current_observation,
                                    event.occurred_at);
          },
          [&](const OperatingHoursChanged& update) {
            activity_for(state.activities, update.activity_id)
                ->timing.open_windows = update.open_windows;
          },
          [&](const PlaceFoundClosed& update) {
            activity_for(state.activities, update.activity_id)
                ->found_closed_at = update.observed_at;
          },
          [&](const TravelDelay& update) {
            const auto existing = std::find_if(
                state.travel_delays.begin(), state.travel_delays.end(),
                [&](const TravelDelayState& delay) {
                  return same_delay_key(delay, update);
                });
            const TravelDelayState replacement{
                .from_activity_id = update.from_activity_id,
                .to_activity_id = update.to_activity_id,
                .additional_seconds = update.additional_seconds,
                .observed_at = event.occurred_at};
            if (existing == state.travel_delays.end()) {
              state.travel_delays.push_back(replacement);
            } else {
              *existing = replacement;
            }
          },
          [&](const PlanDecisionEvent& update) {
            if (update.decision == PlanDecision::kAccept) {
              state.current_plan = *update.resulting_current_plan;
              current_plan_changed = true;
            }
            state.active_proposal.reset();
          },
          [&](const AdvisoryUpdate&) { planning_input_changed = false; },
          [&](const CurrentPlanReplaced& update) {
            state.current_plan = update.current_plan;
            state.active_proposal.reset();
            current_plan_changed = true;
          }},
      event.payload);

  if (planning_input_changed) state.active_proposal.reset();
  if (!event_mutation_valid || !state.is_valid()) {
    state = std::move(original_state);
    return {TripStateApplyStatus::kInvalidArgument, false, false};
  }
  return {TripStateApplyStatus::kAccepted, planning_input_changed,
          current_plan_changed};
}

}  // namespace liveroute::domain
