#pragma once

#include <cstdint>

#include "liveroute/domain/plan_proposal.hpp"
#include "liveroute/domain/trip_state.hpp"
#include "liveroute/runtime/trip_runtime_versions.hpp"

namespace liveroute::runtime {

enum class PlanningCommitStatus : std::uint8_t {
  kCommitted,
  kStale,
  kInvalidArgument,
};

struct PlanningCommitResult {
  PlanningCommitStatus status;

  [[nodiscard]] constexpr bool committed() const noexcept {
    return status == PlanningCommitStatus::kCommitted;
  }
};

// Installs one planner result into shard-owned state only when the captured
// work token is still current. This changes neither runtime versions nor the
// authoritative current plan.
[[nodiscard]] PlanningCommitResult commit_planning_result(
    domain::TripState& state, const TripRuntimeVersions& versions,
    const PlanningWorkToken& token,
    const domain::StoredPlanProposal& proposal);

}  // namespace liveroute::runtime
