#include "liveroute/domain/trip_event.hpp"

#include <algorithm>
#include <cmath>
#include <utility>
#include <vector>

namespace liveroute::domain {

namespace {

template <class... Visitors>
struct Overloaded : Visitors... {
  using Visitors::operator()...;
};

template <class... Visitors>
Overloaded(Visitors...) -> Overloaded<Visitors...>;

[[nodiscard]] bool is_valid_activity_state(ActivityState state) noexcept {
  switch (state) {
    case ActivityState::kPlanned:
    case ActivityState::kStarted:
    case ActivityState::kCompleted:
    case ActivityState::kSkipped:
      return true;
  }
  return false;
}

[[nodiscard]] bool contains_activity(
    std::span<const Activity> activities,
    const ActivityId& activity_id) noexcept {
  return std::any_of(
      activities.begin(), activities.end(),
      [&activity_id](const Activity& activity) {
        return activity.activity_id == activity_id;
      });
}

[[nodiscard]] bool current_activities_are_valid(
    std::span<const Activity> activities) {
  if (activities.size() > 64) return false;
  std::vector<ActivityId> ids;
  ids.reserve(activities.size());
  for (const auto& activity : activities) {
    if (!activity.is_valid()) return false;
    ids.push_back(activity.activity_id);
  }
  std::sort(ids.begin(), ids.end());
  return std::adjacent_find(ids.begin(), ids.end()) == ids.end();
}

[[nodiscard]] std::vector<ActivityId> activity_ids(
    std::span<const Activity> activities) {
  std::vector<ActivityId> result;
  result.reserve(activities.size());
  for (const auto& activity : activities) {
    result.push_back(activity.activity_id);
  }
  return result;
}

[[nodiscard]] bool windows_are_normalized(
    std::span<const TimeWindow> windows) noexcept {
  std::optional<UnixTimeMilliseconds> previous_close;
  for (const auto& window : windows) {
    if (!window.is_valid() ||
        (previous_close.has_value() && window.opens_at < *previous_close)) {
      return false;
    }
    previous_close = window.closes_at;
  }
  return true;
}

[[nodiscard]] bool is_valid_advisory_kind(AdvisoryKind kind) noexcept {
  switch (kind) {
    case AdvisoryKind::kRecommendationRefresh:
    case AdvisoryKind::kWeatherChanged:
    case AdvisoryKind::kCrowdChanged:
    case AdvisoryKind::kSocialUpdate:
      return true;
  }
  return false;
}

}  // namespace

bool TripEdited::is_valid_for(
    std::span<const Activity> current_activities) const {
  if (!current_activities_are_valid(current_activities)) return false;

  auto resulting_ids = activity_ids(current_activities);
  const bool operation_valid = std::visit(
      Overloaded{
          [&](const AddActivity& add) {
            if (!add.activity.is_valid() ||
                resulting_ids.size() >= 64 ||
                add.ordinal > resulting_ids.size() ||
                contains_activity(current_activities,
                                  add.activity.activity_id)) {
              return false;
            }
            resulting_ids.insert(
                resulting_ids.begin() +
                    static_cast<std::ptrdiff_t>(add.ordinal),
                add.activity.activity_id);
            return true;
          },
          [&](const ReplaceActivity& replace) {
            return replace.activity.is_valid() &&
                   contains_activity(current_activities,
                                     replace.activity.activity_id);
          },
          [&](const RemoveActivity& remove) {
            const auto position =
                std::find(resulting_ids.begin(), resulting_ids.end(),
                          remove.activity_id);
            if (position == resulting_ids.end()) return false;
            resulting_ids.erase(position);
            return true;
          },
          [&](const ReorderActivities& reorder) {
            if (reorder.activity_ids.size() != resulting_ids.size()) {
              return false;
            }
            auto expected = resulting_ids;
            auto actual = reorder.activity_ids;
            std::sort(expected.begin(), expected.end());
            std::sort(actual.begin(), actual.end());
            if (std::adjacent_find(actual.begin(), actual.end()) !=
                actual.end() ||
                actual != expected) {
              return false;
            }
            resulting_ids = reorder.activity_ids;
            return true;
          }},
      operation);

  if (!operation_valid ||
      resulting_current_plan.origin != PlanOrigin::kUserAuthored ||
      !resulting_current_plan.is_valid_for(resulting_ids) ||
      resulting_current_plan.segments.size() != resulting_ids.size()) {
    return false;
  }
  for (std::size_t index = 0; index < resulting_ids.size(); ++index) {
    if (resulting_current_plan.segments[index].activity_id !=
        resulting_ids[index]) {
      return false;
    }
  }
  return true;
}

bool PlanDecisionEvent::is_valid_for(
    std::span<const Activity> current_activities) const {
  if (!current_activities_are_valid(current_activities) ||
      source_runtime_epoch == 0) {
    return false;
  }
  switch (decision) {
    case PlanDecision::kAccept: {
      if (!resulting_current_plan.has_value() ||
          resulting_current_plan->origin !=
              PlanOrigin::kAcceptedEngineProposal ||
          resulting_current_plan->source_proposal_id !=
              std::optional<ProposalId>{proposal_id}) {
        return false;
      }
      const auto ids = activity_ids(current_activities);
      return resulting_current_plan->is_valid_for(ids);
    }
    case PlanDecision::kReject:
      return !resulting_current_plan.has_value();
  }
  return false;
}

bool CurrentPlanReplaced::is_valid_for(
    std::span<const Activity> current_activities) const {
  if (!current_activities_are_valid(current_activities) ||
      current_plan.origin != PlanOrigin::kUserAuthored) {
    return false;
  }
  const auto ids = activity_ids(current_activities);
  return current_plan.is_valid_for(ids);
}

std::optional<TripEventClass> TripEvent::event_class() const noexcept {
  return std::visit(
      Overloaded{
          [](const std::monostate&)
              -> std::optional<TripEventClass> { return std::nullopt; },
          [](const LocationUpdated&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kTelemetry;
          },
          [](const VelocityUpdated&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kTelemetry;
          },
          [](const HeadingUpdated&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kTelemetry;
          },
          [](const ActivityStatusChanged&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const ActivityDelayed&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const TripEdited&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kCanonicalFirstDurableMirror;
          },
          [](const ReservationChanged&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const MandatoryDeadlineChanged&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const RouteDeviationDetected&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kHighObservation;
          },
          [](const OperatingHoursChanged&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const PlaceFoundClosed&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const TravelDelay&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurable;
          },
          [](const PlanDecisionEvent&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kDurableCompareAndSwap;
          },
          [](const AdvisoryUpdate&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kAdvisory;
          },
          [](const CurrentPlanReplaced&)
              -> std::optional<TripEventClass> {
            return TripEventClass::kCanonicalFirstDurableMirror;
          }},
      payload);
}

bool TripEvent::is_valid_for(
    std::span<const Activity> current_activities,
    std::size_t max_advisory_payload_bytes) const {
  if (!current_activities_are_valid(current_activities)) return false;

  return std::visit(
      Overloaded{
          [](const std::monostate&) { return false; },
          [](const LocationUpdated& event) {
            return event.location.is_valid();
          },
          [](const VelocityUpdated& event) {
            return std::isfinite(event.meters_per_second) &&
                   event.meters_per_second >= 0;
          },
          [](const HeadingUpdated& event) {
            return std::isfinite(event.degrees) && event.degrees >= 0 &&
                   event.degrees <= 360;
          },
          [&](const ActivityStatusChanged& event) {
            return contains_activity(current_activities, event.activity_id) &&
                   is_valid_activity_state(event.state);
          },
          [&](const ActivityDelayed& event) {
            return contains_activity(current_activities, event.activity_id);
          },
          [&](const TripEdited& event) {
            return event.is_valid_for(current_activities);
          },
          [&](const ReservationChanged& event) {
            return contains_activity(current_activities, event.activity_id);
          },
          [&](const MandatoryDeadlineChanged& event) {
            return contains_activity(current_activities, event.activity_id);
          },
          [](const RouteDeviationDetected& event) {
            return event.location.is_valid();
          },
          [&](const OperatingHoursChanged& event) {
            return contains_activity(current_activities, event.activity_id) &&
                   windows_are_normalized(event.open_windows);
          },
          [&](const PlaceFoundClosed& event) {
            return contains_activity(current_activities, event.activity_id);
          },
          [&](const TravelDelay& event) {
            return contains_activity(current_activities,
                                     event.from_activity_id) &&
                   contains_activity(current_activities,
                                     event.to_activity_id);
          },
          [&](const PlanDecisionEvent& event) {
            return event.is_valid_for(current_activities);
          },
          [&](const AdvisoryUpdate& event) {
            return is_valid_advisory_kind(event.kind) &&
                   !event.source.empty() &&
                   event.opaque_payload.size() <= max_advisory_payload_bytes;
          },
          [&](const CurrentPlanReplaced& event) {
            return event.is_valid_for(current_activities);
          }},
      payload);
}

}  // namespace liveroute::domain
