#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>

#include "liveroute/domain/trip_state.hpp"
#include "liveroute/runtime/trip_runtime_versions.hpp"

namespace liveroute::runtime {

struct EventAdmissionRequest {
  std::uint64_t runtime_epoch{};
  std::uint64_t mutation_sequence{};
  std::uint64_t observation_sequence{};
  std::uint64_t expected_trip_revision{};
  std::optional<std::uint64_t> expected_planner_state_version;
  domain::TripEvent event;
};

enum class EventCoordinatorStatus : std::uint8_t {
  kAccepted,
  kDuplicate,
  kStale,
  kInvalidArgument,
  kCommandExpired,
  kInactive,
  kInternal,
};

enum class EventCoordinatorStaleReason : std::uint8_t {
  kNone,
  kEpoch,
  kMutationSequence,
  kObservationSequence,
  kTripRevision,
  kPlannerStateVersion,
  kPlanProposal,
};

struct ImmutablePlanningSeed {
  domain::TripState state;
  domain::TripEventPayload trigger;
  TripRuntimeVersionSnapshot source_versions;
  PlanningWorkToken work_token;
};

struct EventCoordinatorResult {
  EventCoordinatorStatus status;
  EventCoordinatorStaleReason stale_reason{
      EventCoordinatorStaleReason::kNone};
  bool retryable{};
  bool planning_input_changed{};
  bool current_plan_changed{};
  TripRuntimeVersionSnapshot version_snapshot;
  std::optional<ImmutablePlanningSeed> planning_seed;
  // The plan actually installed by an accepted or duplicate canonical-first
  // mirror. Other event outcomes deliberately leave this absent.
  std::optional<domain::PlanId> resulting_current_plan_id;
};

// Called only by the trip's owner shard after transport response capacity has
// been reserved. Provider and planner work are intentionally absent.
[[nodiscard]] EventCoordinatorResult coordinate_event_admission(
    domain::TripState& state, TripRuntimeVersions& versions,
    const EventAdmissionRequest& request,
    domain::UnixTimeMilliseconds current_time,
    std::size_t max_advisory_payload_bytes);

}  // namespace liveroute::runtime
