#include "liveroute/runtime/planning_commit.hpp"

namespace liveroute::runtime {

PlanningCommitResult commit_planning_result(
    domain::TripState& state, const TripRuntimeVersions& versions,
    const PlanningWorkToken& token,
    const domain::StoredPlanProposal& proposal) {
  if (!versions.can_commit_planning_work(token) ||
      proposal.proposal.source_runtime_epoch != token.runtime_epoch ||
      proposal.proposal.source_planner_state_version !=
          domain::PlannerStateVersion{token.planner_state_version} ||
      proposal.proposal.base_current_plan_id != state.current_plan.plan_id) {
    return {PlanningCommitStatus::kStale};
  }
  if (!state.is_valid() || !proposal.is_valid_for(state.activities)) {
    return {PlanningCommitStatus::kInvalidArgument};
  }

  state.active_proposal = proposal;
  return {PlanningCommitStatus::kCommitted};
}

}  // namespace liveroute::runtime
