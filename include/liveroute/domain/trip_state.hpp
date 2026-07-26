#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <vector>

#include "liveroute/domain/plan_proposal.hpp"
#include "liveroute/domain/trip_event.hpp"

namespace liveroute::domain {

struct CurrentObservationState {
  std::optional<Location> location;
  std::optional<UnixTimeMilliseconds> observed_at;
  std::optional<double> velocity_meters_per_second;
  std::optional<double> heading_degrees;

  [[nodiscard]] bool is_valid() const noexcept;
};

struct TravelDelayState {
  ActivityId from_activity_id;
  ActivityId to_activity_id;
  std::uint32_t additional_seconds{};
  UnixTimeMilliseconds observed_at{0};
};

// Mutable C++ runtime state. It contains only validated internal domain
// values; transport and persistence adapters assemble and serialize it at the
// service boundary.
struct TripState {
  TripId trip_id;
  std::string default_time_zone_name;
  std::vector<Activity> activities;
  std::size_t completed_prefix_count{};
  std::optional<ActivityId> current_activity_id;
  CurrentPlan current_plan;
  std::vector<TravelDelayState> travel_delays;
  CurrentObservationState current_observation;
  std::optional<StoredPlanProposal> active_proposal;

  [[nodiscard]] bool is_valid() const;
};

enum class TripStateApplyStatus : std::uint8_t {
  kAccepted,
  kInvalidArgument,
  kStaleProposal,
};

struct TripStateApplyResult {
  TripStateApplyStatus status;
  bool planning_input_changed{};
  bool current_plan_changed{};

  [[nodiscard]] constexpr bool accepted() const noexcept {
    return status == TripStateApplyStatus::kAccepted;
  }
};

// Applies a previously admitted event to one shard-owned state. Sequence and
// epoch checks happen before this function in the runtime version boundary;
// the function itself validates the complete current domain context.
[[nodiscard]] TripStateApplyResult apply_trip_event(
    TripState& state, const TripEvent& event,
    std::size_t max_advisory_payload_bytes);

}  // namespace liveroute::domain
