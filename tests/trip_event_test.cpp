#include "liveroute/domain/trip_event.hpp"

#include <array>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <optional>
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

CurrentPlan plan(std::span<const ActivityId> ids, PlanOrigin origin,
                 std::uint8_t plan_marker,
                 std::optional<ProposalId> proposal_id = std::nullopt) {
  std::vector<CurrentPlanSegment> segments;
  segments.reserve(ids.size());
  std::int64_t start = 0;
  for (const auto& activity_id : ids) {
    segments.push_back(
        {.activity_id = activity_id,
         .state = PlanEntryState::kScheduled,
         .scheduled_start = UnixTimeMilliseconds{start},
         .scheduled_end = UnixTimeMilliseconds{start + 1000}});
    start += 1000;
  }
  return {.plan_id = id<PlanId>(plan_marker),
          .plan_revision = 1,
          .origin = origin,
          .segments = std::move(segments),
          .created_at = UnixTimeMilliseconds{0},
          .source_proposal_id = proposal_id};
}

TripEvent event(TripEventPayload payload) {
  return {.event_id = id<EventId>(30),
          .occurred_at = UnixTimeMilliseconds{100},
          .command_expires_at = std::nullopt,
          .payload = std::move(payload)};
}

}  // namespace

int main() {
  const std::vector<Activity> activities{activity(1), activity(2)};
  const auto first_id = activities[0].activity_id;
  const auto second_id = activities[1].activity_id;
  const auto unknown_id = id<ActivityId>(9);

  const auto location = event(LocationUpdated{Location{40, -74}});
  const auto velocity = event(VelocityUpdated{12.5});
  const auto heading = event(HeadingUpdated{360});
  const auto status =
      event(ActivityStatusChanged{first_id, ActivityState::kStarted});
  const auto delayed = event(ActivityDelayed{first_id, 10});
  const auto reservation =
      event(ReservationChanged{first_id, std::nullopt, 30});
  const auto deadline =
      event(MandatoryDeadlineChanged{first_id, UnixTimeMilliseconds{5000}});
  const auto deviation =
      event(RouteDeviationDetected{Location{40, -74}, 100});
  const auto hours = event(OperatingHoursChanged{
      first_id,
      {{UnixTimeMilliseconds{0}, UnixTimeMilliseconds{1000}},
       {UnixTimeMilliseconds{1000}, UnixTimeMilliseconds{2000}}}});
  const auto closed =
      event(PlaceFoundClosed{first_id, UnixTimeMilliseconds{100}});
  const auto travel = event(TravelDelay{first_id, second_id, 20});
  const auto advisory =
      event(AdvisoryUpdate{AdvisoryKind::kWeatherChanged, "fixture",
                           {std::byte{1}, std::byte{2}}});

  if (!location.is_valid_for(activities, 2) ||
      location.event_class() != TripEventClass::kTelemetry ||
      !velocity.is_valid_for(activities, 2) ||
      !heading.is_valid_for(activities, 2) ||
      !status.is_valid_for(activities, 2) ||
      status.event_class() != TripEventClass::kDurable ||
      !delayed.is_valid_for(activities, 2) ||
      !reservation.is_valid_for(activities, 2) ||
      !deadline.is_valid_for(activities, 2) ||
      !deviation.is_valid_for(activities, 2) ||
      deviation.event_class() != TripEventClass::kHighObservation ||
      !hours.is_valid_for(activities, 2) ||
      !closed.is_valid_for(activities, 2) ||
      !travel.is_valid_for(activities, 2) ||
      !advisory.is_valid_for(activities, 2) ||
      advisory.event_class() != TripEventClass::kAdvisory) {
    return 1;
  }

  const std::vector<ActivityId> current_ids{first_id, second_id};
  const auto replacement_plan =
      plan(current_ids, PlanOrigin::kUserAuthored, 10);
  const auto replaced = event(CurrentPlanReplaced{replacement_plan});
  if (!replaced.is_valid_for(activities, 2) ||
      replaced.event_class() !=
          TripEventClass::kCanonicalFirstDurableMirror) {
    return 1;
  }

  const auto proposal_id = id<ProposalId>(20);
  const auto accepted_plan =
      plan(current_ids, PlanOrigin::kAcceptedEngineProposal, 11,
           proposal_id);
  const auto accept = event(PlanDecisionEvent{
      .decision = PlanDecision::kAccept,
      .proposal_id = proposal_id,
      .source_runtime_epoch = 2,
      .source_planner_state_version = PlannerStateVersion{3},
      .base_current_plan_id = replacement_plan.plan_id,
      .resulting_current_plan = accepted_plan});
  const auto reject = event(PlanDecisionEvent{
      .decision = PlanDecision::kReject,
      .proposal_id = proposal_id,
      .source_runtime_epoch = 2,
      .source_planner_state_version = PlannerStateVersion{3},
      .base_current_plan_id = replacement_plan.plan_id,
      .resulting_current_plan = std::nullopt});
  if (!accept.is_valid_for(activities, 2) ||
      accept.event_class() != TripEventClass::kDurableCompareAndSwap ||
      !reject.is_valid_for(activities, 2)) {
    return 1;
  }

  const auto third = activity(3);
  const std::vector<ActivityId> added_ids{first_id, third.activity_id,
                                          second_id};
  const auto add = event(TripEdited{
      .operation = AddActivity{third, 1},
      .resulting_current_plan =
          plan(added_ids, PlanOrigin::kUserAuthored, 12)});
  const std::vector<ActivityId> removed_ids{first_id};
  const auto remove = event(TripEdited{
      .operation = RemoveActivity{second_id},
      .resulting_current_plan =
          plan(removed_ids, PlanOrigin::kUserAuthored, 13)});
  const std::vector<ActivityId> reordered_ids{second_id, first_id};
  const auto reorder = event(TripEdited{
      .operation = ReorderActivities{reordered_ids},
      .resulting_current_plan =
          plan(reordered_ids, PlanOrigin::kUserAuthored, 14)});
  const auto replace = event(TripEdited{
      .operation = ReplaceActivity{activities[0]},
      .resulting_current_plan =
          plan(current_ids, PlanOrigin::kUserAuthored, 15)});
  if (!add.is_valid_for(activities, 2) ||
      !remove.is_valid_for(activities, 2) ||
      !reorder.is_valid_for(activities, 2) ||
      !replace.is_valid_for(activities, 2) ||
      add.event_class() !=
          TripEventClass::kCanonicalFirstDurableMirror) {
    return 1;
  }

  auto invalid_location = location;
  invalid_location.payload =
      LocationUpdated{Location{std::nan(""), -74}};
  auto invalid_velocity = velocity;
  invalid_velocity.payload = VelocityUpdated{-1};
  auto invalid_heading = heading;
  invalid_heading.payload = HeadingUpdated{361};
  auto unknown_activity = status;
  unknown_activity.payload =
      ActivityStatusChanged{unknown_id, ActivityState::kStarted};
  auto invalid_windows = hours;
  invalid_windows.payload = OperatingHoursChanged{
      first_id,
      {{UnixTimeMilliseconds{1000}, UnixTimeMilliseconds{2000}},
       {UnixTimeMilliseconds{1500}, UnixTimeMilliseconds{2500}}}};
  auto oversized_advisory = advisory;
  auto invalid_accept = accept;
  std::get<PlanDecisionEvent>(invalid_accept.payload)
      .resulting_current_plan = std::nullopt;
  auto invalid_reject = reject;
  std::get<PlanDecisionEvent>(invalid_reject.payload)
      .resulting_current_plan = accepted_plan;
  auto bad_add = add;
  std::get<TripEdited>(bad_add.payload).operation =
      AddActivity{third, 3};
  const std::vector<ActivityId> inconsistent_ids{
      first_id, second_id, third.activity_id};
  auto inconsistent_add = add;
  std::get<TripEdited>(inconsistent_add.payload).resulting_current_plan =
      plan(inconsistent_ids, PlanOrigin::kUserAuthored, 16);
  auto bad_reorder = reorder;
  std::get<TripEdited>(bad_reorder.payload).operation =
      ReorderActivities{{first_id, first_id}};
  const auto absent = event(std::monostate{});

  return invalid_location.is_valid_for(activities, 2) ||
                 invalid_velocity.is_valid_for(activities, 2) ||
                 invalid_heading.is_valid_for(activities, 2) ||
                 unknown_activity.is_valid_for(activities, 2) ||
                 invalid_windows.is_valid_for(activities, 2) ||
                 oversized_advisory.is_valid_for(activities, 1) ||
                 invalid_accept.is_valid_for(activities, 2) ||
                 invalid_reject.is_valid_for(activities, 2) ||
                 bad_add.is_valid_for(activities, 2) ||
                 inconsistent_add.is_valid_for(activities, 2) ||
                 bad_reorder.is_valid_for(activities, 2) ||
                 absent.is_valid_for(activities, 2) ||
                 absent.event_class().has_value()
             ? 1
             : 0;
}
