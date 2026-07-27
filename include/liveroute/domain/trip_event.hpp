#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <variant>
#include <vector>

#include "liveroute/domain/activity.hpp"
#include "liveroute/domain/current_plan.hpp"
#include "liveroute/domain/types.hpp"

namespace liveroute::domain {

struct LocationUpdated {
  Location location;
};

struct VelocityUpdated {
  double meters_per_second{};
};

struct HeadingUpdated {
  double degrees{};
};

struct ActivityStatusChanged {
  ActivityId activity_id;
  ActivityState state;
};

struct ActivityDelayed {
  ActivityId activity_id;
  std::uint32_t delay_seconds{};
};

struct AddActivity {
  Activity activity;
  std::uint32_t ordinal{};
};

struct ReplaceActivity {
  Activity activity;
};

struct RemoveActivity {
  ActivityId activity_id;
};

struct ReorderActivities {
  std::vector<ActivityId> activity_ids;
};

using TripEditOperation =
    std::variant<AddActivity, ReplaceActivity, RemoveActivity,
                 ReorderActivities>;

struct TripEdited {
  TripEditOperation operation;
  CurrentPlan resulting_current_plan;

  [[nodiscard]] bool is_valid_for(
      std::span<const Activity> current_activities) const;
};

struct ReservationChanged {
  ActivityId activity_id;
  std::optional<UnixTimeMilliseconds> reservation_start;
  std::uint32_t reservation_grace_seconds{};
};

struct MandatoryDeadlineChanged {
  ActivityId activity_id;
  UnixTimeMilliseconds latest_finish{0};
};

struct RouteDeviationDetected {
  Location location;
  std::uint32_t distance_from_route_meters{};
};

struct OperatingHoursChanged {
  ActivityId activity_id;
  std::vector<TimeWindow> open_windows;
};

struct PlaceFoundClosed {
  ActivityId activity_id;
  UnixTimeMilliseconds observed_at{0};
};

struct TravelDelay {
  ActivityId from_activity_id;
  ActivityId to_activity_id;
  std::uint32_t additional_seconds{};
};

enum class PlanDecision : std::uint8_t {
  kAccept = 1,
  kReject = 2,
};

struct PlanDecisionEvent {
  PlanDecision decision;
  ProposalId proposal_id;
  std::uint64_t source_runtime_epoch{};
  PlannerStateVersion source_planner_state_version{0};
  PlanId base_current_plan_id;
  std::optional<CurrentPlan> resulting_current_plan;

  [[nodiscard]] bool is_valid_for(
      std::span<const Activity> current_activities) const;
};

enum class AdvisoryKind : std::uint8_t {
  kRecommendationRefresh = 1,
  kWeatherChanged = 2,
  kCrowdChanged = 3,
  kSocialUpdate = 4,
};

struct AdvisoryUpdate {
  AdvisoryKind kind;
  std::string source;
  std::vector<std::byte> opaque_payload;
};

struct CurrentPlanReplaced {
  CurrentPlan current_plan;

  [[nodiscard]] bool is_valid_for(
      std::span<const Activity> current_activities) const;
};

using TripEventPayload =
    std::variant<std::monostate, LocationUpdated, VelocityUpdated,
                 HeadingUpdated, ActivityStatusChanged, ActivityDelayed,
                 TripEdited, ReservationChanged, MandatoryDeadlineChanged,
                 RouteDeviationDetected, OperatingHoursChanged,
                 PlaceFoundClosed, TravelDelay, PlanDecisionEvent,
                 AdvisoryUpdate, CurrentPlanReplaced>;

enum class TripEventClass : std::uint8_t {
  kTelemetry,
  kDurable,
  kCanonicalFirstDurableMirror,
  kHighObservation,
  kDurableCompareAndSwap,
  kAdvisory,
};

struct TripEvent {
  EventId event_id;
  UnixTimeMilliseconds occurred_at{0};
  std::optional<UnixTimeMilliseconds> command_expires_at;
  TripEventPayload payload;

  [[nodiscard]] std::optional<TripEventClass> event_class() const noexcept;
  [[nodiscard]] EventPriority priority_for(
      std::span<const Activity> current_activities) const noexcept;
  [[nodiscard]] bool is_valid_for(
      std::span<const Activity> current_activities,
      std::size_t max_advisory_payload_bytes) const;
};

}  // namespace liveroute::domain
