#include "liveroute/planner/result_metadata.hpp"

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <limits>
#include <optional>
#include <variant>
#include <vector>

#include "liveroute/planner/checked_int.hpp"

namespace liveroute::planner {

namespace {

template <class... Visitors>
struct Overloaded : Visitors... {
  using Visitors::operator()...;
};

template <class... Visitors>
Overloaded(Visitors...) -> Overloaded<Visitors...>;

void add_reason(domain::PlanReasonCode reason,
                std::vector<domain::PlanReasonCode>* reasons) {
  reasons->push_back(reason);
}

void canonicalize(std::vector<domain::PlanReasonCode>* reasons) {
  std::sort(reasons->begin(), reasons->end(),
            [](domain::PlanReasonCode left, domain::PlanReasonCode right) {
              return static_cast<std::uint8_t>(left) <
                     static_cast<std::uint8_t>(right);
            });
  reasons->erase(std::unique(reasons->begin(), reasons->end()),
                 reasons->end());
}

[[nodiscard]] std::optional<std::int64_t> route_duration_milliseconds(
    const domain::RouteEstimate& estimate) noexcept {
  const auto seconds = estimate.duration.count();
  constexpr auto kMillisecondsPerSecond = std::int64_t{1000};
  if (seconds < 0 ||
      seconds > std::numeric_limits<std::int64_t>::max() /
                    kMillisecondsPerSecond) {
    return std::nullopt;
  }
  return seconds * kMillisecondsPerSecond;
}

[[nodiscard]] bool has_scheduled_reservation_from(
    const BeamSearchInput& input, std::size_t start_index) noexcept {
  for (std::size_t index = start_index;
       index < input.remaining_activities.size(); ++index) {
    const auto& activity = input.remaining_activities[index];
    if (activity.current_plan_segment.state ==
            domain::PlanEntryState::kScheduled &&
        activity.activity.timing.reservation_start.has_value()) {
      return true;
    }
  }
  return false;
}

[[nodiscard]] domain::NotificationType derive_notification(
    BeamSearchOutcome outcome, bool proposal_changes_current_plan,
    std::optional<std::int64_t> next_event_slack_ms) noexcept {
  switch (outcome) {
    case BeamSearchOutcome::kExhaustiveInfeasible:
      return domain::NotificationType::kInfeasibleSchedule;
    case BeamSearchOutcome::kSearchLimited:
    case BeamSearchOutcome::kDeadlineExceeded:
    case BeamSearchOutcome::kCancelled:
    case BeamSearchOutcome::kInvalidInput:
      return domain::NotificationType::kNone;
    case BeamSearchOutcome::kComplete:
    case BeamSearchOutcome::kBestSoFar:
      break;
  }

  if (next_event_slack_ms.has_value() && *next_event_slack_ms < 0) {
    return domain::NotificationType::kCriticalLateness;
  }
  if (proposal_changes_current_plan) {
    return domain::NotificationType::kPlanChangeSuggested;
  }
  constexpr auto kLowSlackUpperBoundMs = std::int64_t{20 * 60 * 1000};
  if (next_event_slack_ms.has_value() &&
      *next_event_slack_ms <= kLowSlackUpperBoundMs) {
    return domain::NotificationType::kLowSlackWarning;
  }
  return domain::NotificationType::kNone;
}

}  // namespace

std::optional<ReplanFacts> derive_replan_facts(
    const BeamSearchInput& input) {
  if (!input.is_valid()) return std::nullopt;

  ReplanFacts facts{};
  const auto suffix_start = input.suffix_start_time().value();
  std::optional<std::size_t> next_scheduled_index;
  for (std::size_t index = 0; index < input.remaining_activities.size();
       ++index) {
    if (input.remaining_activities[index].current_plan_segment.state ==
        domain::PlanEntryState::kScheduled) {
      next_scheduled_index = index;
      break;
    }
  }

  if (next_scheduled_index.has_value()) {
    const auto& route =
        input.travel_time_matrix->at(0, *next_scheduled_index + 1);
    if (!route.reachable) {
      facts.late_departure = true;
    } else {
      const auto duration_ms = route_duration_milliseconds(route);
      const auto arrival =
          duration_ms ? checked_add(suffix_start, *duration_ms) : std::nullopt;
      if (!arrival) return std::nullopt;
      const auto scheduled_start =
          input.remaining_activities[*next_scheduled_index]
              .current_plan_segment.scheduled_start->value();
      facts.next_event_slack_ms =
          checked_subtract(scheduled_start, *arrival);
      if (!facts.next_event_slack_ms) return std::nullopt;
      facts.late_departure = *facts.next_event_slack_ms < 0;
    }
  }

  std::int64_t replay_time = suffix_start;
  std::size_t prior_matrix_index = 0;
  for (std::size_t index = 0; index < input.remaining_activities.size();
       ++index) {
    const auto& planning_activity = input.remaining_activities[index];
    const auto& segment = planning_activity.current_plan_segment;
    if (segment.state == domain::PlanEntryState::kOmitted) continue;

    const auto matrix_index = index + 1;
    const auto& route =
        input.travel_time_matrix->at(prior_matrix_index, matrix_index);
    if (!route.reachable) {
      facts.reservation_at_risk =
          has_scheduled_reservation_from(input, index);
      break;
    }
    const auto route_ms = route_duration_milliseconds(route);
    const auto arrival =
        route_ms ? checked_add(replay_time, *route_ms) : std::nullopt;
    if (!arrival) return std::nullopt;

    const auto& timing = planning_activity.activity.timing;
    if (timing.reservation_start.has_value()) {
      const auto grace_ms =
          checked_milliseconds(timing.reservation_grace_seconds);
      const auto reservation_latest =
          grace_ms ? checked_add(timing.reservation_start->value(), *grace_ms)
                   : std::nullopt;
      if (!reservation_latest) return std::nullopt;
      facts.reservation_at_risk =
          facts.reservation_at_risk || *arrival > *reservation_latest;
    }

    const auto actual_start =
        std::max(*arrival, segment.scheduled_start->value());
    const auto duration = checked_subtract(segment.scheduled_end->value(),
                                           segment.scheduled_start->value());
    const auto actual_end =
        duration ? checked_add(actual_start, *duration) : std::nullopt;
    if (!actual_end) return std::nullopt;
    replay_time = *actual_end;
    prior_matrix_index = matrix_index;
  }
  return facts;
}

std::vector<domain::PlanReasonCode> derive_causal_reasons(
    const domain::TripEventPayload& trigger, const ReplanFacts& facts) {
  std::vector<domain::PlanReasonCode> reasons;
  reasons.reserve(3);
  if (facts.late_departure) {
    add_reason(domain::PlanReasonCode::kLateDeparture, &reasons);
  }
  if (facts.reservation_at_risk) {
    add_reason(domain::PlanReasonCode::kReservationAtRisk, &reasons);
  }

  std::visit(
      Overloaded{
          [](const std::monostate&) {},
          [](const domain::LocationUpdated&) {},
          [](const domain::VelocityUpdated&) {},
          [](const domain::HeadingUpdated&) {},
          [&](const domain::ActivityStatusChanged&) {
            add_reason(domain::PlanReasonCode::kUserEdit, &reasons);
          },
          [&](const domain::ActivityDelayed&) {
            add_reason(domain::PlanReasonCode::kActivityDelay, &reasons);
          },
          [&](const domain::TripEdited&) {
            add_reason(domain::PlanReasonCode::kUserEdit, &reasons);
          },
          [&](const domain::ReservationChanged&) {
            add_reason(domain::PlanReasonCode::kUserEdit, &reasons);
          },
          [&](const domain::MandatoryDeadlineChanged&) {
            add_reason(domain::PlanReasonCode::kUserEdit, &reasons);
          },
          [&](const domain::RouteDeviationDetected&) {
            add_reason(domain::PlanReasonCode::kRouteDeviation, &reasons);
          },
          [&](const domain::OperatingHoursChanged&) {
            add_reason(domain::PlanReasonCode::kHoursChanged, &reasons);
          },
          [&](const domain::PlaceFoundClosed&) {
            add_reason(domain::PlanReasonCode::kPlaceClosed, &reasons);
          },
          [&](const domain::TravelDelay&) {
            add_reason(domain::PlanReasonCode::kTravelDelay, &reasons);
          },
          [&](const domain::PlanDecisionEvent&) {
            add_reason(domain::PlanReasonCode::kUserEdit, &reasons);
          },
          [](const domain::AdvisoryUpdate&) {},
          [&](const domain::CurrentPlanReplaced&) {
            add_reason(domain::PlanReasonCode::kUserEdit, &reasons);
          }},
      trigger);

  canonicalize(&reasons);
  return reasons;
}

std::vector<domain::PlanReasonCode> derive_segment_reasons(
    const domain::TripEventPayload& trigger, const ReplanFacts& facts,
    bool segment_changed) {
  return segment_changed ? derive_causal_reasons(trigger, facts)
                         : std::vector<domain::PlanReasonCode>{};
}

ResultMetadata derive_result_metadata(
    const domain::TripEventPayload& trigger, const ReplanFacts& facts,
    BeamSearchOutcome outcome, bool proposal_changes_current_plan) {
  auto reasons = outcome == BeamSearchOutcome::kInvalidInput
                     ? std::vector<domain::PlanReasonCode>{}
                     : derive_causal_reasons(trigger, facts);
  switch (outcome) {
    case BeamSearchOutcome::kBestSoFar:
    case BeamSearchOutcome::kSearchLimited:
    case BeamSearchOutcome::kDeadlineExceeded:
    case BeamSearchOutcome::kCancelled:
      add_reason(domain::PlanReasonCode::kDeadlineBudget, &reasons);
      break;
    case BeamSearchOutcome::kExhaustiveInfeasible:
      add_reason(domain::PlanReasonCode::kNoFeasiblePlan, &reasons);
      break;
    case BeamSearchOutcome::kComplete:
    case BeamSearchOutcome::kInvalidInput:
      break;
  }
  canonicalize(&reasons);
  return {
      .notification =
          derive_notification(outcome, proposal_changes_current_plan,
                              facts.next_event_slack_ms),
      .reasons = std::move(reasons),
  };
}

}  // namespace liveroute::planner
