#include "liveroute/planner/result_metadata.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <utility>
#include <vector>

namespace {

using namespace liveroute::domain;
using namespace liveroute::planner;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

CurrentPlan user_plan(ActivityId activity_id) {
  return {
      .plan_id = id<PlanId>(3),
      .plan_revision = 1,
      .origin = PlanOrigin::kUserAuthored,
      .segments = {{.activity_id = activity_id,
                    .state = PlanEntryState::kOmitted,
                    .scheduled_start = std::nullopt,
                    .scheduled_end = std::nullopt}},
      .created_at = UnixTimeMilliseconds{0},
      .source_proposal_id = std::nullopt,
  };
}

Activity activity(std::uint8_t marker,
                  std::optional<UnixTimeMilliseconds> reservation_start) {
  return {
      .activity_id = id<ActivityId>(marker),
      .place_id = PlaceId{"place"},
      .display_name = "Place",
      .location = Location{40, -74},
      .time_zone_name = "America/New_York",
      .inbound_travel_mode = TravelMode::kWalking,
      .activity_class = ActivityClass::kFlexible,
      .activity_state = ActivityState::kPlanned,
      .priority_rank = 0,
      .utility_score = 1,
      .timing =
          ActivityTiming{
              .open_windows = {{UnixTimeMilliseconds{0},
                                UnixTimeMilliseconds{100000}}},
              .reservation_start = reservation_start,
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

PlanningActivity planning_activity(const Activity& activity,
                                   std::size_t ordinal, std::int64_t start,
                                   std::int64_t end) {
  return {
      .activity = activity,
      .original_trip_ordinal = ordinal,
      .current_plan_segment =
          {.activity_id = activity.activity_id,
           .state = PlanEntryState::kScheduled,
           .scheduled_start = UnixTimeMilliseconds{start},
           .scheduled_end = UnixTimeMilliseconds{end}},
  };
}

bool equals(const std::vector<PlanReasonCode>& actual,
            std::initializer_list<PlanReasonCode> expected) {
  return actual == std::vector<PlanReasonCode>{expected};
}

}  // namespace

int main() {
  const auto activity_id = id<ActivityId>(1);
  const auto proposal_id = id<ProposalId>(2);
  const auto plan = user_plan(activity_id);

  const auto first = activity(4, std::nullopt);
  const auto second =
      activity(5, std::optional{UnixTimeMilliseconds{3500}});
  const TravelTimeMatrix travel{
      3,
      {{std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{2}, 20, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{2}, 20, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true}}};
  const BeamSearchInput fact_input{
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{100000},
      .preserved_prefix = {},
      .remaining_activities =
          {planning_activity(first, 0, 1000, 2000),
           planning_activity(second, 1, 3000, 4000)},
      .travel_time_matrix = &travel,
  };
  const auto derived_facts = derive_replan_facts(fact_input);
  if (!derived_facts || !derived_facts->late_departure ||
      !derived_facts->reservation_at_risk ||
      derived_facts->next_event_slack_ms !=
          std::optional<std::int64_t>{-1000}) {
    return 1;
  }

  auto unreachable_estimates =
      std::vector<RouteEstimate>(
          9, RouteEstimate{std::chrono::seconds{0}, 0, true});
  unreachable_estimates[1].reachable = false;
  const TravelTimeMatrix unreachable_matrix{
      3, std::move(unreachable_estimates)};
  auto unreachable_input = fact_input;
  unreachable_input.travel_time_matrix = &unreachable_matrix;
  const auto unreachable_facts = derive_replan_facts(unreachable_input);
  if (!unreachable_facts || !unreachable_facts->late_departure ||
      !unreachable_facts->reservation_at_risk ||
      unreachable_facts->next_event_slack_ms.has_value()) {
    return 1;
  }

  struct TriggerCase {
    TripEventPayload trigger;
    std::optional<PlanReasonCode> reason;
  };
  const std::vector<TriggerCase> cases{
      {LocationUpdated{Location{40, -74}}, std::nullopt},
      {VelocityUpdated{1}, std::nullopt},
      {HeadingUpdated{90}, std::nullopt},
      {ActivityStatusChanged{activity_id, ActivityState::kStarted},
       PlanReasonCode::kUserEdit},
      {ActivityDelayed{activity_id, 10},
       PlanReasonCode::kActivityDelay},
      {TripEdited{RemoveActivity{activity_id}, plan},
       PlanReasonCode::kUserEdit},
      {ReservationChanged{activity_id, std::nullopt, 0},
       PlanReasonCode::kUserEdit},
      {MandatoryDeadlineChanged{activity_id, UnixTimeMilliseconds{1000}},
       PlanReasonCode::kUserEdit},
      {RouteDeviationDetected{Location{40, -74}, 10},
       PlanReasonCode::kRouteDeviation},
      {OperatingHoursChanged{
           activity_id,
           {{UnixTimeMilliseconds{0}, UnixTimeMilliseconds{1000}}}},
       PlanReasonCode::kHoursChanged},
      {PlaceFoundClosed{activity_id, UnixTimeMilliseconds{0}},
       PlanReasonCode::kPlaceClosed},
      {TravelDelay{activity_id, activity_id, 10},
       PlanReasonCode::kTravelDelay},
      {PlanDecisionEvent{
           .decision = PlanDecision::kReject,
           .proposal_id = proposal_id,
           .source_runtime_epoch = 1,
           .source_planner_state_version = PlannerStateVersion{1},
           .base_current_plan_id = plan.plan_id,
           .resulting_current_plan = std::nullopt},
       PlanReasonCode::kUserEdit},
      {AdvisoryUpdate{AdvisoryKind::kWeatherChanged, "fixture", {}},
       std::nullopt},
      {CurrentPlanReplaced{plan}, PlanReasonCode::kUserEdit},
  };
  for (const auto& test_case : cases) {
    const auto reasons =
        derive_causal_reasons(test_case.trigger, ReplanFacts{});
    if (test_case.reason.has_value()) {
      if (!equals(reasons, {*test_case.reason})) return 1;
    } else if (!reasons.empty()) {
      return 1;
    }
  }

  const TripEventPayload delayed = ActivityDelayed{activity_id, 10};
  const ReplanFacts risky{
      .late_departure = true,
      .reservation_at_risk = true,
      .next_event_slack_ms = -1,
  };
  if (!equals(derive_causal_reasons(delayed, risky),
              {PlanReasonCode::kLateDeparture,
               PlanReasonCode::kActivityDelay,
               PlanReasonCode::kReservationAtRisk}) ||
      !derive_segment_reasons(delayed, risky, false).empty() ||
      !equals(derive_segment_reasons(delayed, risky, true),
              {PlanReasonCode::kLateDeparture,
               PlanReasonCode::kActivityDelay,
               PlanReasonCode::kReservationAtRisk})) {
    return 1;
  }

  const auto critical =
      derive_result_metadata(delayed, risky, BeamSearchOutcome::kComplete,
                             true);
  if (critical.notification != NotificationType::kCriticalLateness ||
      !equals(critical.reasons,
              {PlanReasonCode::kLateDeparture,
               PlanReasonCode::kActivityDelay,
               PlanReasonCode::kReservationAtRisk})) {
    return 1;
  }

  const ReplanFacts zero_slack{
      .late_departure = false,
      .reservation_at_risk = false,
      .next_event_slack_ms = 0,
  };
  const auto changed =
      derive_result_metadata(delayed, zero_slack,
                             BeamSearchOutcome::kComplete, true);
  const auto low =
      derive_result_metadata(delayed, zero_slack,
                             BeamSearchOutcome::kComplete, false);
  auto healthy_facts = zero_slack;
  healthy_facts.next_event_slack_ms = 20 * 60 * 1000 + 1;
  auto boundary_facts = zero_slack;
  boundary_facts.next_event_slack_ms = 20 * 60 * 1000;
  auto unknown_facts = zero_slack;
  unknown_facts.next_event_slack_ms = std::nullopt;
  const auto healthy =
      derive_result_metadata(delayed, healthy_facts,
                             BeamSearchOutcome::kComplete, false);
  const auto boundary =
      derive_result_metadata(delayed, boundary_facts,
                             BeamSearchOutcome::kComplete, false);
  const auto unknown =
      derive_result_metadata(delayed, unknown_facts,
                             BeamSearchOutcome::kComplete, false);
  if (changed.notification != NotificationType::kPlanChangeSuggested ||
      low.notification != NotificationType::kLowSlackWarning ||
      boundary.notification != NotificationType::kLowSlackWarning ||
      healthy.notification != NotificationType::kNone ||
      unknown.notification != NotificationType::kNone) {
    return 1;
  }

  const auto best =
      derive_result_metadata(delayed, zero_slack,
                             BeamSearchOutcome::kBestSoFar, true);
  const auto limited =
      derive_result_metadata(delayed, risky,
                             BeamSearchOutcome::kSearchLimited, false);
  const auto deadline =
      derive_result_metadata(delayed, zero_slack,
                             BeamSearchOutcome::kDeadlineExceeded, false);
  const auto cancelled =
      derive_result_metadata(delayed, zero_slack,
                             BeamSearchOutcome::kCancelled, false);
  const auto infeasible =
      derive_result_metadata(delayed, risky,
                             BeamSearchOutcome::kExhaustiveInfeasible, false);
  const auto invalid =
      derive_result_metadata(delayed, risky,
                             BeamSearchOutcome::kInvalidInput, false);
  return best.notification != NotificationType::kPlanChangeSuggested ||
                 !equals(best.reasons,
                         {PlanReasonCode::kActivityDelay,
                          PlanReasonCode::kDeadlineBudget}) ||
                 limited.notification != NotificationType::kNone ||
                 !equals(limited.reasons,
                         {PlanReasonCode::kLateDeparture,
                          PlanReasonCode::kActivityDelay,
                          PlanReasonCode::kReservationAtRisk,
                          PlanReasonCode::kDeadlineBudget}) ||
                 deadline.notification != NotificationType::kNone ||
                 !equals(deadline.reasons,
                         {PlanReasonCode::kActivityDelay,
                          PlanReasonCode::kDeadlineBudget}) ||
                 cancelled.notification != NotificationType::kNone ||
                 !equals(cancelled.reasons,
                         {PlanReasonCode::kActivityDelay,
                          PlanReasonCode::kDeadlineBudget}) ||
                 infeasible.notification !=
                     NotificationType::kInfeasibleSchedule ||
                 !equals(infeasible.reasons,
                         {PlanReasonCode::kLateDeparture,
                          PlanReasonCode::kActivityDelay,
                          PlanReasonCode::kReservationAtRisk,
                          PlanReasonCode::kNoFeasiblePlan}) ||
                 invalid.notification != NotificationType::kNone ||
                 !invalid.reasons.empty()
             ? 1
             : 0;
}
