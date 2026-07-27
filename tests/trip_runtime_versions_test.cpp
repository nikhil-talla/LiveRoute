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
  if (versions.capture_planning_work().has_value() ||
      versions.can_commit_planning_work({})) {
    return 1;
  }
  if (!has_status(versions.accept_observation(1, 1),
                  VersionOperationStatus::kInactive) ||
      !has_status(versions.bootstrap(0, 1, 1, 0),
                  VersionOperationStatus::kInvalidArgument) ||
      !has_status(versions.bootstrap(4, 7, 11, 3),
                  VersionOperationStatus::kAccepted) ||
      !versions.snapshot_ready()) {
    return 1;
  }

  const auto initial_work = versions.capture_planning_work();
  if (!initial_work.has_value() ||
      !versions.can_commit_planning_work(*initial_work)) {
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

  const auto planner_version_stale =
      versions.accept_durable(4, 12, 7, true, 1);
  if (!has_status(versions.accept_durable(3, 12, 7, true),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_durable(4, 13, 7, true),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_durable(4, 12, 6, true),
                  VersionOperationStatus::kStale) ||
      planner_version_stale.status != VersionOperationStatus::kStale ||
      planner_version_stale.stale_reason !=
          VersionStaleReason::kPlannerStateVersion ||
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
  if (versions.can_commit_planning_work(*initial_work)) {
    return 1;
  }

  const auto post_durable_work = versions.capture_planning_work();
  if (!post_durable_work.has_value() ||
      !versions.can_commit_planning_work(*post_durable_work)) {
    return 1;
  }

  if (!has_status(versions.accept_observation(4, 3),
                  VersionOperationStatus::kStale) ||
      !has_status(versions.accept_observation(4, 9, 0),
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
  if (versions.can_commit_planning_work(*post_durable_work)) {
    return 1;
  }

  TripRuntimeVersions freshness_versions;
  if (!has_status(freshness_versions.bootstrap(8, 1, 1, 0),
                  VersionOperationStatus::kAccepted) ||
      !has_status(freshness_versions.accept_durable(8, 2, 1, true, 1),
                  VersionOperationStatus::kStale) ||
      freshness_versions.snapshot().accepted_mutation_sequence != 1 ||
      freshness_versions.snapshot().planner_state_version != 0 ||
      freshness_versions.accept_durable(8, 2, 1, true, 0).status !=
          VersionOperationStatus::kAccepted ||
      freshness_versions.snapshot().planner_state_version != 1) {
    return 1;
  }
  if (!has_status(freshness_versions.accept_observation(8, 2, 0),
                  VersionOperationStatus::kStale) ||
      freshness_versions.snapshot().accepted_observation_sequence != 0) {
    return 1;
  }

  const auto terminal_planner_version_stale =
      freshness_versions.resolve_terminal_durable(8, 3, 2, 0);
  if (terminal_planner_version_stale.status !=
          VersionOperationStatus::kStale ||
      terminal_planner_version_stale.stale_reason !=
          VersionStaleReason::kPlannerStateVersion ||
      freshness_versions.snapshot().accepted_mutation_sequence != 2) {
    return 1;
  }
  if (!has_status(freshness_versions.resolve_terminal_durable(8, 3, 2, 1),
                  VersionOperationStatus::kAccepted) ||
      freshness_versions.snapshot().accepted_mutation_sequence != 3) {
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

  TripRuntimeVersions terminal_versions;
  if (!has_status(terminal_versions.bootstrap(7, 3, 4, 0),
                  VersionOperationStatus::kAccepted)) {
    return 1;
  }
  const auto before_terminal = terminal_versions.snapshot();
  if (!has_status(terminal_versions.resolve_terminal_durable(7, 6, 3),
                  VersionOperationStatus::kStale) ||
      !has_status(terminal_versions.resolve_terminal_durable(7, 5, 4),
                  VersionOperationStatus::kStale) ||
      !has_status(terminal_versions.resolve_terminal_durable(7, 5, 3),
                  VersionOperationStatus::kAccepted)) {
    return 1;
  }
  const auto after_terminal = terminal_versions.snapshot();
  if (after_terminal.accepted_mutation_sequence != 5 ||
      after_terminal.trip_revision != before_terminal.trip_revision ||
      after_terminal.planner_state_version !=
          before_terminal.planner_state_version ||
      after_terminal.planning_generation != before_terminal.planning_generation ||
      after_terminal.finalized_mutation_sequence != 4 ||
      terminal_versions.snapshot_ready() ||
      !has_status(terminal_versions.resolve_terminal_durable(7, 5, 3),
                  VersionOperationStatus::kDuplicate) ||
      !has_status(terminal_versions.confirm_finalized(7, 5),
                  VersionOperationStatus::kAccepted) ||
      !terminal_versions.snapshot_ready()) {
    return 1;
  }

  return 0;
}
