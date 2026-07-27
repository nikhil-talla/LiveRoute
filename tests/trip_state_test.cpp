#include "liveroute/domain/trip_state.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <utility>
#include <vector>

namespace {

using namespace liveroute::domain;

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
      .timing = ActivityTiming{
          .open_windows = {{UnixTimeMilliseconds{0},
                            UnixTimeMilliseconds{100000}}},
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

CurrentPlan plan(std::span<const Activity> activities, std::uint8_t marker) {
  std::vector<CurrentPlanSegment> segments;
  segments.reserve(activities.size());
  std::int64_t start = 0;
  for (const auto& activity_value : activities) {
    segments.push_back({.activity_id = activity_value.activity_id,
                        .state = PlanEntryState::kScheduled,
                        .scheduled_start = UnixTimeMilliseconds{start},
                        .scheduled_end = UnixTimeMilliseconds{start + 1000}});
    start += 1000;
  }
  return {.plan_id = id<PlanId>(marker),
          .plan_revision = 1,
          .origin = PlanOrigin::kUserAuthored,
          .segments = std::move(segments),
          .created_at = UnixTimeMilliseconds{0},
          .source_proposal_id = std::nullopt};
}

StoredPlanProposal proposal(const TripState& state) {
  std::vector<ProposalSegment> segments;
  segments.reserve(state.activities.size());
  std::int64_t start = 0;
  for (const auto& activity_value : state.activities) {
    segments.push_back(
        {.activity_id = activity_value.activity_id,
         .location = activity_value.location,
         .time_zone_name = activity_value.time_zone_name,
         .scheduled_start = UnixTimeMilliseconds{start},
         .scheduled_end = UnixTimeMilliseconds{start + 1000},
         .inbound_route = RouteEstimate{std::chrono::seconds{1}, 1, true},
         .disposition = SegmentDisposition::kPreserved,
         .reasons = {}});
    start += 1000;
  }
  return {
      .proposal = PlanProposal{
          .proposal_id = id<ProposalId>(12),
          .source_runtime_epoch = 2,
          .source_planner_state_version = PlannerStateVersion{4},
          .base_current_plan_id = state.current_plan.plan_id,
          .source_trip_revision = TripRevision{1},
          .source_accepted_mutation_sequence = MutationSequence{1},
          .preserved_prefix = {},
          .revised_suffix = std::move(segments),
          .created_at = UnixTimeMilliseconds{500}},
      .notification = NotificationType::kNone,
      .reasons = {},
      .stats = {},
      .quality = ResultQuality{PlanQuality::kComplete, RoutingQuality::kFresh,
                               RecoveryState::kCurrent}};
}

CurrentPlan accepted_plan(std::span<const Activity> activities,
                          std::uint8_t marker, ProposalId source_proposal_id) {
  auto result = plan(activities, marker);
  result.plan_revision = 2;
  result.origin = PlanOrigin::kAcceptedEngineProposal;
  result.source_proposal_id = source_proposal_id;
  return result;
}

TripEvent event(TripEventPayload payload) {
  return {.event_id = id<EventId>(90),
          .occurred_at = UnixTimeMilliseconds{500},
          .command_expires_at = std::nullopt,
          .payload = std::move(payload)};
}

TripState make_state() {
  const std::vector<Activity> activities{activity(1), activity(2)};
  return {.trip_id = id<TripId>(80),
          .default_time_zone_name = "America/New_York",
          .activities = activities,
          .completed_prefix_count = 0,
          .current_activity_id = std::nullopt,
          .current_plan = plan(activities, 10),
          .travel_delays = {},
          .current_observation = {},
          .active_proposal = std::nullopt};
}

bool accepted(const TripStateApplyResult& result, bool planning_changed = true,
              bool plan_changed = false) {
  return result.status == TripStateApplyStatus::kAccepted &&
         result.planning_input_changed == planning_changed &&
         result.current_plan_changed == plan_changed;
}

}  // namespace

int main() {
  TripState state = make_state();
  if (!state.is_valid()) return 1;

  if (!accepted(apply_trip_event(
                    state, event(LocationUpdated{Location{41.0, -73.0}}), 16)) ||
      !state.current_observation.location.has_value() ||
      state.current_observation.location->latitude != 41.0 ||
      state.current_observation.location->longitude != -73.0) {
    return 1;
  }

  if (!accepted(apply_trip_event(state, event(VelocityUpdated{12.0}), 16)) ||
      !state.current_observation.velocity_meters_per_second.has_value() ||
      *state.current_observation.velocity_meters_per_second != 12.0 ||
      !accepted(apply_trip_event(state, event(HeadingUpdated{180.0}), 16))) {
    return 1;
  }

  const auto first_id = state.activities[0].activity_id;
  const auto second_id = state.activities[1].activity_id;
  if (!accepted(apply_trip_event(
                    state, event(ActivityStatusChanged{
                                     first_id, ActivityState::kStarted}),
                    16)) ||
      state.current_activity_id != std::optional<ActivityId>{first_id} ||
      !accepted(apply_trip_event(state, event(ActivityDelayed{first_id, 20}),
                                 16)) ||
      state.activities[0].activity_delay_seconds != 20 ||
      !accepted(apply_trip_event(
                    state, event(ReservationChanged{first_id,
                                                    UnixTimeMilliseconds{900},
                                                    30}),
                    16)) ||
      state.activities[0].timing.reservation_start !=
          std::optional<UnixTimeMilliseconds>{UnixTimeMilliseconds{900}} ||
      !accepted(apply_trip_event(
                    state, event(MandatoryDeadlineChanged{
                                     first_id, UnixTimeMilliseconds{2000}}),
                    16)) ||
      state.activities[0].timing.mandatory_deadline !=
          std::optional<UnixTimeMilliseconds>{UnixTimeMilliseconds{2000}} ||
      !accepted(apply_trip_event(
                    state, event(OperatingHoursChanged{
                                     first_id,
                                     {{UnixTimeMilliseconds{0},
                                       UnixTimeMilliseconds{3000}}}}),
                    16)) ||
      !accepted(apply_trip_event(
                    state, event(PlaceFoundClosed{
                                     first_id, UnixTimeMilliseconds{700}}),
                    16)) ||
      !accepted(apply_trip_event(
                    state, event(TravelDelay{first_id, second_id, 30}), 16)) ||
      state.travel_delays.size() != 1 ||
      state.travel_delays.front().additional_seconds != 30) {
    return 1;
  }

  if (!accepted(apply_trip_event(
                    state, event(ActivityStatusChanged{
                                     first_id, ActivityState::kCompleted}),
                    16)) ||
      state.completed_prefix_count != 1 || state.current_activity_id.has_value() ||
      state.activities[0].activity_state != ActivityState::kCompleted ||
      !state.is_valid()) {
    return 1;
  }

  TripState invalid_progress = make_state();
  if (apply_trip_event(
          invalid_progress,
          event(ActivityStatusChanged{second_id, ActivityState::kCompleted}),
          16)
          .status != TripStateApplyStatus::kInvalidArgument ||
      invalid_progress.completed_prefix_count != 0 ||
      invalid_progress.current_activity_id.has_value() ||
      invalid_progress.activities[1].activity_state != ActivityState::kPlanned ||
      !invalid_progress.is_valid()) {
    return 1;
  }

  const auto third = activity(3);
  auto added_activities = state.activities;
  added_activities.insert(added_activities.begin() + 1, third);
  if (!accepted(apply_trip_event(
                    state,
                    event(TripEdited{
                        .operation = AddActivity{third, 1},
                        .resulting_current_plan = plan(added_activities, 11)}),
                    16),
               true, true) ||
      state.activities.size() != 3 ||
      state.activities[1].activity_id != third.activity_id) {
    return 1;
  }

  auto changed_first = state.activities.front();
  changed_first.display_name = "Changed place";
  auto replaced_activities = state.activities;
  replaced_activities.front() = changed_first;
  if (!accepted(apply_trip_event(
                    state,
                    event(TripEdited{
                        .operation = ReplaceActivity{changed_first},
                        .resulting_current_plan =
                            plan(replaced_activities, 12)}),
                    16),
               true, true) ||
      state.activities.front().display_name != "Changed place") {
    return 1;
  }

  if (!accepted(apply_trip_event(state, event(TravelDelay{
                                                   third.activity_id,
                                                   first_id,
                                                   5}),
                                 16)) ||
      state.travel_delays.size() != 2) {
    return 1;
  }

  auto removed_activities = state.activities;
  removed_activities.erase(removed_activities.begin() + 1);
  if (!accepted(apply_trip_event(
                    state,
                    event(TripEdited{
                        .operation = RemoveActivity{third.activity_id},
                        .resulting_current_plan =
                            plan(removed_activities, 13)}),
                    16),
               true, true) ||
      state.activities.size() != 2 ||
      state.current_plan.segments.size() != 2 ||
      state.travel_delays.size() != 1) {
    return 1;
  }

  TripState reorder_state = make_state();
  auto reordered_activities = reorder_state.activities;
  std::swap(reordered_activities[0], reordered_activities[1]);
  if (!accepted(apply_trip_event(
                    reorder_state,
                    event(TripEdited{
                        .operation = ReorderActivities{
                            {reordered_activities[0].activity_id,
                             reordered_activities[1].activity_id}},
                        .resulting_current_plan =
                            plan(reordered_activities, 14)}),
                    16),
               true, true) ||
      reorder_state.activities[0].activity_id !=
          reorder_state.current_plan.segments[0].activity_id) {
    return 1;
  }

  const auto replacement = plan(std::span<const Activity>{state.activities}, 11);
  if (!accepted(apply_trip_event(state, event(CurrentPlanReplaced{replacement}),
                                 16),
               true, true) ||
      state.current_plan.plan_id != id<PlanId>(11) ||
      state.active_proposal.has_value()) {
    return 1;
  }

  auto advisory = event(AdvisoryUpdate{
      AdvisoryKind::kWeatherChanged, "fixture", {std::byte{1}}});
  if (!accepted(apply_trip_event(state, advisory, 1), false, false) ||
      accepted(apply_trip_event(state, advisory, 0), false, false)) {
    return 1;
  }

  state.active_proposal = proposal(state);
  if (!state.is_valid()) return 1;
  if (!accepted(apply_trip_event(
                    state, event(LocationUpdated{Location{42.0, -72.0}}),
                    16)) ||
      state.active_proposal.has_value()) {
    return 1;
  }

  state.active_proposal = proposal(state);
  if (!state.is_valid()) return 1;
  auto reject = event(PlanDecisionEvent{
      .decision = PlanDecision::kReject,
      .proposal_id = id<ProposalId>(12),
      .source_runtime_epoch = 2,
      .source_planner_state_version = PlannerStateVersion{4},
      .base_current_plan_id = state.current_plan.plan_id,
      .resulting_current_plan = std::nullopt});
  if (!accepted(apply_trip_event(state, reject, 16), true, false) ||
      state.active_proposal.has_value()) {
    return 1;
  }

  state.active_proposal = proposal(state);
  const auto accepted_current_plan = accepted_plan(
      std::span<const Activity>{state.activities}, 13, id<ProposalId>(12));
  auto accept = event(PlanDecisionEvent{
      .decision = PlanDecision::kAccept,
      .proposal_id = id<ProposalId>(12),
      .source_runtime_epoch = 2,
      .source_planner_state_version = PlannerStateVersion{4},
      .base_current_plan_id = state.current_plan.plan_id,
      .resulting_current_plan = accepted_current_plan});
  if (!accepted(apply_trip_event(state, accept, 16), true, true) ||
      state.current_plan.plan_id != accepted_current_plan.plan_id ||
      state.current_plan.origin != PlanOrigin::kAcceptedEngineProposal ||
      state.active_proposal.has_value()) {
    return 1;
  }

  auto invalid = event(LocationUpdated{Location{91.0, -73.0}});
  if (apply_trip_event(state, invalid, 16).status !=
      TripStateApplyStatus::kInvalidArgument) {
    return 1;
  }

  auto decision = event(PlanDecisionEvent{
      .decision = PlanDecision::kReject,
      .proposal_id = id<ProposalId>(12),
      .source_runtime_epoch = 2,
      .source_planner_state_version = PlannerStateVersion{4},
      .base_current_plan_id = state.current_plan.plan_id,
      .resulting_current_plan = std::nullopt});
  if (apply_trip_event(state, decision, 16).status !=
      TripStateApplyStatus::kStaleProposal) {
    return 1;
  }

  return 0;
}
