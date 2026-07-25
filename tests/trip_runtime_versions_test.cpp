#include "liveroute/runtime/trip_runtime_versions.hpp"

#include <cstdint>

namespace {

using liveroute::runtime::TripRuntimeVersions;
using liveroute::runtime::VersionOperationStatus;
using liveroute::runtime::VersionStaleReason;

bool has_status(liveroute::runtime::VersionOperationResult result,
                VersionOperationStatus status) {
  return result.status == status;
}

}  // namespace

int main() {
  TripRuntimeVersions versions;
  if (!has_status(versions.accept_observation(1, 1),
                  VersionOperationStatus::kInactive) ||
      !has_status(versions.bootstrap(0, 1, 1, 0),
                  VersionOperationStatus::kInvalidArgument) ||
      !has_status(versions.bootstrap(4, 7, 11, 3),
                  VersionOperationStatus::kAccepted) ||
      !versions.snapshot_ready()) {
    return 1;
  }

  const auto first = versions.snapshot();
  if (first.runtime_epoch != 4 || first.trip_revision != 7 ||
      first.planner_state_version != 0 || first.planning_generation != 0 ||
      first.accepted_mutation_sequence != 11 ||
      first.finalized_mutation_sequence != 11 ||
      first.accepted_observation_sequence != 3) {
    return 1;
  }

  if (!has_status(versions.accept_durable(3, 12, 7, true),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_durable(4, 13, 7, true),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_durable(4, 12, 6, true),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_durable(4, 12, 7, true),
                  VersionOperationStatus::kAccepted) ||
      versions.snapshot_ready()) {
    return 1;
  }

  const auto after_durable = versions.snapshot();
  if (after_durable.trip_revision != 8 ||
      after_durable.planner_state_version != 1 ||
      after_durable.planning_generation != 1 ||
      after_durable.accepted_mutation_sequence != 12 ||
      !has_status(versions.accept_durable(4, 12, 7, true),
                  VersionOperationStatus::kDuplicate) ||
      !has_status(versions.confirm_finalized(4, 13),
                  VersionOperationStatus::kInvalidArgument) ||
      !has_status(versions.confirm_finalized(4, 12),
                  VersionOperationStatus::kAccepted) ||
      !versions.snapshot_ready() ||
      !has_status(versions.confirm_finalized(4, 12),
                  VersionOperationStatus::kDuplicate)) {
    return 1;
  }

  if (!has_status(versions.accept_observation(4, 3),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_observation(4, 9),
                  VersionOperationStatus::kAccepted) ||
      versions.snapshot().planner_state_version != 2 ||
      versions.snapshot().planning_generation != 2 ||
      versions.snapshot().accepted_observation_sequence != 9 ||
      !has_status(versions.bootstrap(4, 8, 12, 8),
                  VersionOperationStatus::kInvalidArgument) ||
      !has_status(versions.bootstrap(4, 8, 12, 9),
                  VersionOperationStatus::kDuplicate)) {
    return 1;
  }

  if (!has_status(versions.bootstrap(5, 8, 12, 0),
                  VersionOperationStatus::kAccepted) ||
      versions.snapshot().planner_state_version != 0 ||
      versions.snapshot().planning_generation != 0 ||
      versions.snapshot().accepted_observation_sequence != 0 ||
      !has_status(versions.bootstrap(4, 8, 12, 0),
                  VersionOperationStatus::kStale)) {
    return 1;
  }

  const auto epoch_stale = versions.accept_durable(4, 13, 8, false);
  const auto sequence_stale = versions.accept_durable(5, 14, 8, false);
  const auto revision_stale = versions.accept_durable(5, 13, 7, false);
  if (epoch_stale.stale_reason != VersionStaleReason::kEpoch ||
      sequence_stale.stale_reason != VersionStaleReason::kMutationSequence ||
      revision_stale.stale_reason != VersionStaleReason::kTripRevision) {
    return 1;
  }

  return 0;
}
