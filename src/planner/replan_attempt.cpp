#include "liveroute/planner/replan_attempt.hpp"

#include <utility>

namespace liveroute::planner {

ReplanAttemptResult run_replan_attempt(
    const BeamSearchInput& input, std::span<const domain::Activity> activities,
    const ProposalSource& source, const domain::TripEventPayload& trigger,
    const ReplanFacts& facts, const ReplanBudget& budget) {
  auto search = run_beam_search(input, budget);

  std::optional<AssembledProposal> proposal;
  if (search.has_complete_candidate() && search.best_decisions.has_value() &&
      (search.outcome == BeamSearchOutcome::kComplete ||
       search.outcome == BeamSearchOutcome::kBestSoFar)) {
    proposal = assemble_plan_proposal(input, *search.best_decisions, activities,
                                      source, trigger, facts);
  }

  const auto outcome = search.outcome;
  const bool changes_current_plan =
      proposal.has_value() && proposal->changes_current_plan;
  return {.search = std::move(search),
          .proposal = std::move(proposal),
          .metadata = derive_result_metadata(
              trigger, facts, outcome, changes_current_plan)};
}

std::optional<domain::StoredPlanProposal> assemble_stored_plan_proposal(
    const ReplanAttemptResult& attempt,
    std::span<const domain::Activity> activities,
    const domain::PlannerStats& stats, domain::RoutingQuality routing_quality,
    domain::RecoveryState recovery_state) {
  if (!attempt.proposal.has_value() ||
      !attempt.search.has_complete_candidate() ||
      (attempt.search.outcome != BeamSearchOutcome::kComplete &&
       attempt.search.outcome != BeamSearchOutcome::kBestSoFar)) {
    return std::nullopt;
  }

  const auto plan_quality =
      attempt.search.outcome == BeamSearchOutcome::kComplete
          ? domain::PlanQuality::kComplete
          : domain::PlanQuality::kBestSoFar;
  domain::StoredPlanProposal stored{
      .proposal = attempt.proposal->proposal,
      .notification = attempt.metadata.notification,
      .reasons = attempt.metadata.reasons,
      .stats = stats,
      .quality = {.plan_quality = plan_quality,
                  .routing_quality = routing_quality,
                  .recovery_state = recovery_state}};
  return stored.is_valid_for(activities)
             ? std::optional<domain::StoredPlanProposal>{std::move(stored)}
             : std::nullopt;
}

}  // namespace liveroute::planner
