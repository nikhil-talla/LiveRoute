#include "liveroute/runtime/trip_runtime_versions.hpp"

#include <limits>

namespace liveroute::runtime {
namespace {

[[nodiscard]] constexpr VersionOperationResult accepted() {
  return {VersionOperationStatus::kAccepted};
}

[[nodiscard]] constexpr VersionOperationResult duplicate() {
  return {VersionOperationStatus::kDuplicate};
}

[[nodiscard]] constexpr VersionOperationResult stale(VersionStaleReason reason) {
  return {VersionOperationStatus::kStale, reason};
}

[[nodiscard]] constexpr VersionOperationResult invalid_argument() {
  return {VersionOperationStatus::kInvalidArgument};
}

[[nodiscard]] constexpr bool can_increment(std::uint64_t value) {
  return value != std::numeric_limits<std::uint64_t>::max();
}

}  // namespace

VersionOperationResult TripRuntimeVersions::bootstrap(
    std::uint64_t runtime_epoch, std::uint64_t trip_revision,
    std::uint64_t finalized_mutation_sequence,
    std::uint64_t current_observation_sequence) {
  if (runtime_epoch == 0 || trip_revision == 0) return invalid_argument();

  if (active_ && runtime_epoch < snapshot_.runtime_epoch) {
    return stale(VersionStaleReason::kEpoch);
  }
  if (active_ && runtime_epoch == snapshot_.runtime_epoch) {
    if (trip_revision != snapshot_.trip_revision ||
        finalized_mutation_sequence != snapshot_.finalized_mutation_sequence ||
        finalized_mutation_sequence != snapshot_.accepted_mutation_sequence ||
        current_observation_sequence < snapshot_.accepted_observation_sequence) {
      return invalid_argument();
    }
    if (current_observation_sequence != snapshot_.accepted_observation_sequence) {
      return invalid_argument();
    }
    return duplicate();
  }

  active_ = true;
  snapshot_ = {
      .runtime_epoch = runtime_epoch,
      .trip_revision = trip_revision,
      .planner_state_version = 0,
      .planning_generation = 0,
      .accepted_mutation_sequence = finalized_mutation_sequence,
      .finalized_mutation_sequence = finalized_mutation_sequence,
      .accepted_observation_sequence = current_observation_sequence,
  };
  return accepted();
}

VersionOperationResult TripRuntimeVersions::validate_epoch(
    std::uint64_t runtime_epoch) const noexcept {
  if (!active_) return {VersionOperationStatus::kInactive};
  if (runtime_epoch != snapshot_.runtime_epoch) {
    return stale(VersionStaleReason::kEpoch);
  }
  return accepted();
}

VersionOperationResult TripRuntimeVersions::accept_durable(
    std::uint64_t runtime_epoch, std::uint64_t mutation_sequence,
    std::uint64_t expected_trip_revision, bool advances_trip_revision) {
  if (const auto epoch_result = validate_epoch(runtime_epoch); !epoch_result.accepted()) {
    return epoch_result;
  }
  if (mutation_sequence <= snapshot_.accepted_mutation_sequence) return duplicate();
  if (!can_increment(snapshot_.accepted_mutation_sequence) ||
      mutation_sequence != snapshot_.accepted_mutation_sequence + 1) {
    return stale(VersionStaleReason::kMutationSequence);
  }
  if (expected_trip_revision != snapshot_.trip_revision) {
    return stale(VersionStaleReason::kTripRevision);
  }
  if (!can_increment(snapshot_.planner_state_version) ||
      (advances_trip_revision && !can_increment(snapshot_.trip_revision))) {
    return invalid_argument();
  }

  snapshot_.accepted_mutation_sequence = mutation_sequence;
  ++snapshot_.planner_state_version;
  ++snapshot_.planning_generation;
  if (advances_trip_revision) ++snapshot_.trip_revision;
  return accepted();
}

VersionOperationResult TripRuntimeVersions::accept_observation(
    std::uint64_t runtime_epoch, std::uint64_t observation_sequence) {
  if (const auto epoch_result = validate_epoch(runtime_epoch); !epoch_result.accepted()) {
    return epoch_result;
  }
  if (observation_sequence <= snapshot_.accepted_observation_sequence) {
    return stale(VersionStaleReason::kObservationSequence);
  }
  if (!can_increment(snapshot_.planner_state_version)) return invalid_argument();

  snapshot_.accepted_observation_sequence = observation_sequence;
  ++snapshot_.planner_state_version;
  ++snapshot_.planning_generation;
  return accepted();
}

VersionOperationResult TripRuntimeVersions::confirm_finalized(
    std::uint64_t runtime_epoch, std::uint64_t finalized_mutation_sequence) {
  if (const auto epoch_result = validate_epoch(runtime_epoch); !epoch_result.accepted()) {
    return epoch_result;
  }
  if (finalized_mutation_sequence <= snapshot_.finalized_mutation_sequence) {
    return duplicate();
  }
  if (finalized_mutation_sequence > snapshot_.accepted_mutation_sequence) {
    return invalid_argument();
  }
  snapshot_.finalized_mutation_sequence = finalized_mutation_sequence;
  return accepted();
}

bool TripRuntimeVersions::snapshot_ready() const noexcept {
  return active_ && snapshot_.accepted_mutation_sequence ==
                        snapshot_.finalized_mutation_sequence;
}

}  // namespace liveroute::runtime
