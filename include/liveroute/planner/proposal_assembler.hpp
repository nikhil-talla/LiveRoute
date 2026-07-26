#pragma once

#include <cstdint>
#include <optional>
#include <span>

#include "liveroute/domain/activity.hpp"
#include "liveroute/domain/plan_proposal.hpp"
#include "liveroute/domain/trip_event.hpp"
#include "liveroute/planner/beam_search.hpp"
#include "liveroute/planner/result_metadata.hpp"

namespace liveroute::planner {

struct ProposalSource {
  domain::ProposalId proposal_id;
  std::uint64_t runtime_epoch{};
  domain::PlannerStateVersion planner_state_version{0};
  domain::PlanId base_current_plan_id;
  domain::TripRevision trip_revision{0};
  domain::MutationSequence accepted_mutation_sequence{0};
  domain::UnixTimeMilliseconds created_at{0};
};

struct AssembledProposal {
  domain::PlanProposal proposal;
  bool changes_current_plan{};
};

// Converts a complete beam result into the advisory proposal domain model.
// activities is the immutable complete trip activity snapshot; it supplies
// metadata for preserved-prefix entries that is intentionally absent from
// BeamSearchInput. Revised scheduled routes come only from the input matrix.
[[nodiscard]] std::optional<AssembledProposal> assemble_plan_proposal(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    std::span<const domain::Activity> activities,
    const ProposalSource& source, const domain::TripEventPayload& trigger,
    const ReplanFacts& facts);

}  // namespace liveroute::planner
