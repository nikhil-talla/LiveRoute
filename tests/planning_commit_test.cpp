#include "liveroute/runtime/planning_commit.hpp"

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>

namespace {

using namespace liveroute::domain;
using namespace liveroute::runtime;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

TripState state() {
  return {.trip_id = id<TripId>(1),
          .default_time_zone_name = "America/New_York",
          .activities = {},
          .completed_prefix_count = 0,
          .current_activity_id = std::nullopt,
          .current_plan = {.plan_id = id<PlanId>(2),
                           .plan_revision = 1,
                           .origin = PlanOrigin::kUserAuthored,
                           .segments = {},
                           .created_at = UnixTimeMilliseconds{0},
                           .source_proposal_id = std::nullopt},
          .travel_delays = {},
          .current_observation = {},
          .active_proposal = std::nullopt};
}

StoredPlanProposal proposal(const TripState& trip) {
  return {.proposal = {.proposal_id = id<ProposalId>(3),
                       .source_runtime_epoch = 4,
                       .source_planner_state_version = PlannerStateVersion{0},
                       .base_current_plan_id = trip.current_plan.plan_id,
                       .source_trip_revision = TripRevision{1},
                       .source_accepted_mutation_sequence = MutationSequence{1},
                       .preserved_prefix = {},
                       .revised_suffix = {},
                       .created_at = UnixTimeMilliseconds{5}},
          .notification = NotificationType::kNone,
          .reasons = {},
          .stats = {},
          .quality = {.plan_quality = PlanQuality::kComplete,
                      .routing_quality = RoutingQuality::kFresh,
                      .recovery_state = RecoveryState::kCurrent}};
}

}  // namespace

int main() {
  auto trip = state();
  TripRuntimeVersions versions;
  if (versions.bootstrap(4, 1, 1, 0).status !=
      VersionOperationStatus::kAccepted) {
    return 1;
  }
  const auto token = versions.capture_planning_work();
  if (!token.has_value()) return 1;

  const auto stored = proposal(trip);
  if (!commit_planning_result(trip, versions, *token, stored).committed() ||
      !trip.active_proposal.has_value() ||
      trip.active_proposal->proposal.proposal_id != stored.proposal.proposal_id) {
    return 1;
  }

  if (versions.accept_observation(4, 1).status !=
      VersionOperationStatus::kAccepted ||
      commit_planning_result(trip, versions, *token, stored).status !=
          PlanningCommitStatus::kStale ||
      !trip.active_proposal.has_value()) {
    return 1;
  }

  const auto fresh_token = versions.capture_planning_work();
  if (!fresh_token.has_value()) return 1;
  auto wrong_plan = stored;
  wrong_plan.proposal.base_current_plan_id = id<PlanId>(9);
  if (commit_planning_result(trip, versions, *fresh_token, wrong_plan).status !=
      PlanningCommitStatus::kStale) {
    return 1;
  }

  auto invalid = stored;
  invalid.proposal.source_planner_state_version = PlannerStateVersion{1};
  invalid.quality.plan_quality = PlanQuality::kNoNewProposal;
  if (commit_planning_result(trip, versions, *fresh_token, invalid).status !=
      PlanningCommitStatus::kInvalidArgument ||
      !trip.active_proposal.has_value()) {
    return 1;
  }
  return 0;
}
