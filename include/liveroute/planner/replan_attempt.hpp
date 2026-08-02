#pragma once

#include <optional>
#include <span>

#include "liveroute/domain/activity.hpp"
#include "liveroute/domain/trip_event.hpp"
#include "liveroute/planner/beam_search.hpp"
#include "liveroute/planner/proposal_assembler.hpp"
#include "liveroute/planner/result_metadata.hpp"

namespace liveroute::planner {

// The result of one immutable, transport-independent planner attempt. Runtime
// code owns status mapping, version fencing, and proposal persistence.
struct ReplanAttemptResult {
  BeamSearchResult search;
  std::optional<AssembledProposal> proposal;
  ResultMetadata metadata;
};

// Runs the in-memory planner stages in their contract-defined order. The
// caller supplies the immutable matrix-backed input, complete activity
// snapshot, proposal source metadata, trigger, and already-derived facts from
// that same snapshot.
[[nodiscard]] ReplanAttemptResult run_replan_attempt(
    const BeamSearchInput& input, std::span<const domain::Activity> activities,
    const ProposalSource& source, const domain::TripEventPayload& trigger,
    const ReplanFacts& facts, const ReplanBudget& budget);

// Worker-owned scratch overload. The storage is reset and reused by the beam
// traversal; the caller must keep it confined to one planner worker.
[[nodiscard]] ReplanAttemptResult run_replan_attempt(
    const BeamSearchInput& input, std::span<const domain::Activity> activities,
    const ProposalSource& source, const domain::TripEventPayload& trigger,
    const ReplanFacts& facts, const ReplanBudget& budget,
    PlannerScratch& scratch);

// Wraps a successful attempt with the exact stored-proposal quality and
// metadata fields. Stage timings and routing/recovery quality are supplied by
// the owning runtime; failed/no-proposal attempts return nullopt.
[[nodiscard]] std::optional<domain::StoredPlanProposal>
assemble_stored_plan_proposal(
    const ReplanAttemptResult& attempt,
    std::span<const domain::Activity> activities,
    const domain::PlannerStats& stats, domain::RoutingQuality routing_quality,
    domain::RecoveryState recovery_state);

}  // namespace liveroute::planner
