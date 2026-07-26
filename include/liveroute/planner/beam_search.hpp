#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <stop_token>
#include <vector>

#include "liveroute/domain/activity.hpp"
#include "liveroute/domain/current_plan.hpp"
#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/planner/candidate_score.hpp"

namespace liveroute::planner {

struct PlanningActivity {
  domain::Activity activity;
  std::size_t original_trip_ordinal{};
  domain::CurrentPlanSegment current_plan_segment;

  [[nodiscard]] bool is_valid() const noexcept {
    return activity.is_valid() &&
           activity.activity_id == current_plan_segment.activity_id &&
           current_plan_segment.is_valid();
  }
};

struct ReplanBudget {
  domain::Deadline deadline;
  std::size_t max_candidates{};
  std::size_t beam_width{};
  std::size_t max_expansions{};
  std::stop_token stop_token;

  [[nodiscard]] bool is_valid() const noexcept {
    return max_candidates != 0 && beam_width != 0 && max_expansions != 0;
  }
};

// preserved_prefix contains every immutable completed/skipped entry and any
// started activity. remaining_activities is in exact authoritative CurrentPlan
// suffix order, including omitted entries; original_trip_ordinal is a separate
// stable trip identity. Matrix index zero is the current location at the
// effective suffix start and remaining activity i is matrix index i + 1. All
// values are normalized UTC/in-memory constraints.
struct BeamSearchInput {
  domain::UnixTimeMilliseconds current_time;
  domain::UnixTimeMilliseconds planning_horizon_start;
  domain::UnixTimeMilliseconds planning_horizon_end;
  std::vector<domain::CurrentPlanSegment> preserved_prefix;
  std::vector<PlanningActivity> remaining_activities;
  const domain::TravelTimeMatrix* travel_time_matrix{};

  [[nodiscard]] bool is_valid() const noexcept;
  [[nodiscard]] domain::UnixTimeMilliseconds suffix_start_time() const noexcept;
};

enum class CandidateAlternativeKind : std::uint8_t {
  kScheduled,
  kSkipped,
};

// An alternative for one activity in the exact order in which a parent must
// consider it. Scheduled alternatives precede a possible skip alternative.
// The generator uses only normalized domain data; reachability is supplied as
// the already-checked arrival time by the beam traversal.
struct CandidateAlternative {
  CandidateAlternativeKind kind;
  std::size_t activity_ordinal{};
  domain::UnixTimeMilliseconds start{0};
  domain::UnixTimeMilliseconds end{0};
  bool is_exact_current_plan{};

  [[nodiscard]] bool is_valid() const noexcept {
    return kind == CandidateAlternativeKind::kSkipped
               ? start == domain::UnixTimeMilliseconds{0} &&
                     end == domain::UnixTimeMilliseconds{0} &&
                     !is_exact_current_plan
               : start < end;
  }
};

// Generates the finite, boundary-derived V1 proposal set for one activity.
// It deliberately does not search a time grid or derive new durations.
[[nodiscard]] std::vector<CandidateAlternative> generate_candidate_alternatives(
    const BeamSearchInput& input, const PlanningActivity& activity,
    domain::UnixTimeMilliseconds arrival);

// Scores a unique expansion-decision prefix under
// liveroute-v1-lexicographic-1. Undecided activities retain optimistic utility
// and contribute no future costs. A full decision sequence produces the exact
// complete-candidate score. Invalid, hard-infeasible, non-generated, or
// overflow-prone sequences return nullopt.
[[nodiscard]] std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions);

enum class BeamSearchOutcome : std::uint8_t {
  kComplete,
  kBestSoFar,
  kExhaustiveInfeasible,
  kSearchLimited,
  kDeadlineExceeded,
  kCancelled,
  kInvalidInput,
};

// Transport-independent search result. The RPC/domain adapter maps
// kSearchLimited to OK/NO_NEW_PROPOSAL and kExhaustiveInfeasible to
// INFEASIBLE; the planner itself does not depend on wire status types.
struct BeamSearchResult {
  BeamSearchOutcome outcome;
  std::optional<std::vector<ExpansionDecision>> best_decisions;
  std::optional<CandidateScore> best_score;
  std::size_t expansion_count{};
  std::size_t candidate_count{};
  bool search_was_truncated{};

  [[nodiscard]] bool has_complete_candidate() const noexcept {
    return best_decisions.has_value() && best_score.has_value();
  }
};

struct BeamPathNode {
  std::optional<std::size_t> parent_path_index;
  ExpansionDecision decision;
};

struct BeamScratchCandidate {
  std::optional<std::size_t> path_index;
  std::size_t depth{};
  CandidateScore score;
};

// Worker-owned reusable storage. Candidate paths are represented by parent
// indices so expansion does not copy every prior decision into every child.
struct PlannerScratch {
  std::vector<BeamPathNode> path_nodes;
  std::vector<BeamScratchCandidate> beam;
  std::vector<BeamScratchCandidate> children;
  std::vector<std::size_t> activity_order;
  std::vector<std::uint8_t> decided;
  std::vector<ExpansionDecision> working_decisions;
  std::vector<ExpansionDecision> comparison_left;
  std::vector<ExpansionDecision> comparison_right;

  void reset() noexcept {
    path_nodes.clear();
    beam.clear();
    children.clear();
    activity_order.clear();
    decided.clear();
    working_decisions.clear();
    comparison_left.clear();
    comparison_right.clear();
  }
};

// Runs the deterministic finite V1 beam traversal. A bounded search that
// discarded feasible partials or exhausted a candidate budget never reports
// exhaustive infeasibility.
[[nodiscard]] BeamSearchResult run_beam_search(
    const BeamSearchInput& input, const ReplanBudget& budget);
[[nodiscard]] BeamSearchResult run_beam_search(
    const BeamSearchInput& input, const ReplanBudget& budget,
    PlannerScratch& scratch);

}  // namespace liveroute::planner
