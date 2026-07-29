#include "liveroute/runtime/event_coordinator.hpp"

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <utility>
#include <vector>

namespace {

using namespace liveroute::domain;
using namespace liveroute::runtime;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

Activity activity(std::uint8_t marker) {
  return {.activity_id = id<ActivityId>(marker),
          .place_id = PlaceId{"place"},
          .display_name = "Place",
          .location = Location{40, -74},
          .time_zone_name = "America/New_York",
          .inbound_travel_mode = TravelMode::kWalking,
          .activity_class = ActivityClass::kFlexible,
          .activity_state = ActivityState::kPlanned,
          .priority_rank = 0,
          .utility_score = 1,
          .timing = {.open_windows = {{UnixTimeMilliseconds{0},
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
          .found_closed_at = std::nullopt};
}

TripState state() {
  const auto first = activity(1);
  const auto second = activity(2);
  return {.trip_id = id<TripId>(3),
          .default_time_zone_name = "America/New_York",
          .activities = {first, second},
          .completed_prefix_count = 0,
          .current_activity_id = std::nullopt,
          .current_plan = {.plan_id = id<PlanId>(4),
                           .plan_revision = 1,
                           .origin = PlanOrigin::kUserAuthored,
                           .segments = {
                               {.activity_id = first.activity_id,
                                .state = PlanEntryState::kScheduled,
                                .scheduled_start = UnixTimeMilliseconds{0},
                                .scheduled_end = UnixTimeMilliseconds{1000}},
                               {.activity_id = second.activity_id,
                                .state = PlanEntryState::kScheduled,
                                .scheduled_start = UnixTimeMilliseconds{1000},
                                .scheduled_end = UnixTimeMilliseconds{2000}}},
                           .created_at = UnixTimeMilliseconds{0},
                           .source_proposal_id = std::nullopt},
          .travel_delays = {},
          .current_observation = {},
          .active_proposal = std::nullopt};
}

TripEvent event(TripEventPayload payload, std::uint8_t marker) {
  return {.event_id = id<EventId>(marker),
          .occurred_at = UnixTimeMilliseconds{10},
          .command_expires_at = std::nullopt,
          .payload = std::move(payload)};
}

EventAdmissionRequest durable_request(TripEvent value,
                                      std::uint64_t sequence) {
  return {.runtime_epoch = 7,
          .mutation_sequence = sequence,
          .observation_sequence = 0,
          .expected_trip_revision = 1,
          .expected_planner_state_version = std::nullopt,
          .event = std::move(value)};
}

}  // namespace

int main() {
  auto trip = state();
  TripRuntimeVersions versions;
  if (!versions.bootstrap(7, 1, 0, 0).accepted()) return 1;

  const auto invalid_transition = coordinate_event_admission(
      trip, versions,
      durable_request(
          event(ActivityStatusChanged{trip.activities[1].activity_id,
                                      ActivityState::kCompleted},
                10),
          1),
      16);
  if (invalid_transition.status !=
          EventCoordinatorStatus::kInvalidArgument ||
      versions.snapshot().accepted_mutation_sequence != 1 ||
      versions.snapshot().planner_state_version != 0 ||
      versions.snapshot().trip_revision != 1 ||
      invalid_transition.version_snapshot.accepted_mutation_sequence != 1 ||
      trip.completed_prefix_count != 0) {
    return 1;
  }

  auto accepted_request = durable_request(
      event(ActivityStatusChanged{trip.activities[0].activity_id,
                                  ActivityState::kStarted},
            11),
      2);
  const auto accepted = coordinate_event_admission(
      trip, versions, accepted_request, 16);
  if (accepted.status != EventCoordinatorStatus::kAccepted ||
      !accepted.planning_seed.has_value() ||
      accepted.planning_seed->source_versions.accepted_mutation_sequence != 2 ||
      accepted.planning_seed->source_versions.planner_state_version != 1 ||
      accepted.planning_seed->source_versions.planning_generation != 1 ||
      trip.current_activity_id !=
          std::optional<ActivityId>{trip.activities[0].activity_id}) {
    return 1;
  }

  const auto duplicate = coordinate_event_admission(
      trip, versions, accepted_request, 16);
  if (duplicate.status != EventCoordinatorStatus::kDuplicate ||
      versions.snapshot().accepted_mutation_sequence != 2 ||
      versions.snapshot().planner_state_version != 1) {
    return 1;
  }

  const EventAdmissionRequest invalid_observation{
      .runtime_epoch = 7,
      .mutation_sequence = 0,
      .observation_sequence = 1,
      .expected_trip_revision = 0,
      .expected_planner_state_version = std::nullopt,
      .event = event(LocationUpdated{Location{91, 0}}, 12)};
  const auto observation = coordinate_event_admission(
      trip, versions, invalid_observation, 16);
  if (observation.status != EventCoordinatorStatus::kInvalidArgument ||
      versions.snapshot().accepted_observation_sequence != 0 ||
      versions.snapshot().planner_state_version != 1) {
    return 1;
  }

  const EventAdmissionRequest advisory_request{
      .runtime_epoch = 7,
      .mutation_sequence = 0,
      .observation_sequence = 1,
      .expected_trip_revision = 0,
      .expected_planner_state_version = std::nullopt,
      .event = event(
          AdvisoryUpdate{AdvisoryKind::kWeatherChanged, "fixture", {}}, 14)};
  const auto generation_before_advisory =
      versions.snapshot().planning_generation;
  const auto advisory = coordinate_event_admission(
      trip, versions, advisory_request, 16);
  if (advisory.status != EventCoordinatorStatus::kAccepted ||
      advisory.planning_input_changed || advisory.planning_seed.has_value() ||
      versions.snapshot().accepted_observation_sequence != 1 ||
      versions.snapshot().planner_state_version != 1 ||
      versions.snapshot().planning_generation !=
          generation_before_advisory) {
    return 1;
  }

  auto canonical = state();
  canonical.activities[0].activity_state = ActivityState::kStarted;
  canonical.current_activity_id = canonical.activities[0].activity_id;
  TripRuntimeVersions canonical_versions;
  if (!canonical_versions.bootstrap(7, 1, 0, 0).accepted()) return 1;
  auto invalid_plan = canonical.current_plan;
  std::swap(invalid_plan.segments[0], invalid_plan.segments[1]);
  invalid_plan.plan_id = id<PlanId>(8);
  const auto mirror = coordinate_event_admission(
      canonical, canonical_versions,
      durable_request(event(CurrentPlanReplaced{invalid_plan}, 13), 1), 16);
  return mirror.status != EventCoordinatorStatus::kInvalidArgument ||
                 canonical_versions.snapshot().accepted_mutation_sequence != 0
             ? 1
             : 0;
}
