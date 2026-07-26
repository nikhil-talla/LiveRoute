#pragma once

#include <optional>
#include <span>
#include <vector>

#include "liveroute/domain/current_plan.hpp"
#include "liveroute/planner/beam_search.hpp"

namespace liveroute::planner {

// Structural planner output before proposal metadata, dispositions, reasons,
// and serialization are assigned by the domain/service result assembler.
struct CandidateItinerary {
  std::vector<domain::CurrentPlanSegment> preserved_prefix;
  std::vector<domain::CurrentPlanSegment> revised_suffix;

  [[nodiscard]] bool is_valid_for(const BeamSearchInput& input) const;
};

// A complete decision set becomes scheduled suffix entries in route order,
// followed by omitted entries in ascending original trip ordinal. The
// preserved prefix is copied unchanged.
[[nodiscard]] std::optional<CandidateItinerary>
reconstruct_candidate_itinerary(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions);

}  // namespace liveroute::planner
