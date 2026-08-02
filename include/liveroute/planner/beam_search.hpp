#pragma once

#include <algorithm>
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
// Moving a scheduled activity preserves its exact baseline duration; adding
// back an omitted activity uses its preferred duration. V1 never shortens.
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
  // These preserve the actual interruption cause when a valid complete
  // candidate changes the visible outcome to kBestSoFar.
  bool deadline_hit{};
  bool cancellation_requested{};

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

// Private worker-owned columnar view. It is derived from one valid
// BeamSearchInput and never crosses the planner boundary.
struct PlannerActivityColumns {
  std::vector<domain::ActivityId> activity_ids;
  std::vector<std::size_t> original_trip_ordinals;
  std::vector<std::size_t> matrix_location_indices;
  std::vector<std::int32_t> priority_ranks;
  std::vector<std::int32_t> utility_scores;
  std::vector<std::int64_t> minimum_duration_ms;
  std::vector<std::int64_t> scheduled_duration_ms;
  std::vector<std::int64_t> preferred_duration_ms;
  std::vector<std::int64_t> maximum_duration_ms;
  std::vector<std::int64_t> earliest_open_ms;
  std::vector<std::int64_t> latest_close_ms;
  std::vector<std::int64_t> baseline_start_ms;
  std::vector<std::int64_t> baseline_end_ms;
  std::vector<std::int64_t> reservation_start_ms;
  std::vector<std::int64_t> reservation_latest_start_ms;
  std::vector<std::int64_t> mandatory_deadline_ms;
  std::vector<std::uint16_t> flags;
  std::vector<std::int64_t> window_opens_ms;
  std::vector<std::int64_t> window_closes_ms;
  std::vector<std::size_t> window_offsets;
  std::vector<std::size_t> sorted_ordinals;
  std::vector<std::uint8_t> sorted_activity_indices;

  void reset() noexcept {
    activity_ids.clear();
    original_trip_ordinals.clear();
    matrix_location_indices.clear();
    priority_ranks.clear();
    utility_scores.clear();
    minimum_duration_ms.clear();
    scheduled_duration_ms.clear();
    preferred_duration_ms.clear();
    maximum_duration_ms.clear();
    earliest_open_ms.clear();
    latest_close_ms.clear();
    baseline_start_ms.clear();
    baseline_end_ms.clear();
    reservation_start_ms.clear();
    reservation_latest_start_ms.clear();
    mandatory_deadline_ms.clear();
    flags.clear();
    window_opens_ms.clear();
    window_closes_ms.clear();
    window_offsets.clear();
    sorted_ordinals.clear();
    sorted_activity_indices.clear();
  }
};

struct PlannerScoreScratch {
  std::vector<bool> decided;
  std::vector<bool> changed;
  std::vector<bool> common_scheduled;
  std::vector<std::int32_t> priority_ranks;
  std::vector<std::size_t> candidate_common_order;
  std::vector<std::size_t> baseline_common_order;
  std::vector<std::size_t> baseline_common_positions;
  std::vector<std::size_t> candidate_common_positions;

  void reset() noexcept {
    decided.clear();
    changed.clear();
    common_scheduled.clear();
    priority_ranks.clear();
    candidate_common_order.clear();
    baseline_common_order.clear();
    baseline_common_positions.clear();
    candidate_common_positions.clear();
  }

  void prepare(std::size_t activity_count, std::size_t decision_count) {
    decided.resize(activity_count);
    changed.resize(activity_count);
    common_scheduled.resize(activity_count);
    std::fill(decided.begin(), decided.end(), false);
    std::fill(changed.begin(), changed.end(), false);
    std::fill(common_scheduled.begin(), common_scheduled.end(), false);
    priority_ranks.clear();
    candidate_common_order.clear();
    candidate_common_order.reserve(decision_count);
    baseline_common_order.clear();
    baseline_common_positions.resize(activity_count);
    candidate_common_positions.resize(activity_count);
  }
};

inline constexpr std::uint8_t kTailValidatedInput = 1U << 0U;
inline constexpr std::uint8_t kTailLowerBoundScratch = 1U << 1U;
inline constexpr std::uint8_t kTailPartialBeamSelection = 1U << 2U;
inline constexpr std::uint8_t kTailAllOptimizations =
    kTailValidatedInput | kTailLowerBoundScratch |
    kTailPartialBeamSelection;

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
  PlannerActivityColumns activity_columns;
  std::vector<std::uint8_t> protected_decided;
  // The measured Phase 19 SoA experiment is disabled in serving/runtime
  // scratch after its native timing gate failed. Benchmark code may opt in to
  // reproduce the rejected candidate against this accepted AoS path.
  bool use_soa{false};
  // The measured Phase 20 experiments are disabled in serving/runtime scratch
  // after their predeclared tail-latency gates failed. Benchmark code may opt
  // in to reproduce each candidate against this accepted mask-zero path.
  std::uint8_t tail_optimization_mask{0};
  PlannerScoreScratch score;

  void reset() noexcept {
    path_nodes.clear();
    beam.clear();
    children.clear();
    activity_order.clear();
    decided.clear();
    working_decisions.clear();
    comparison_left.clear();
    comparison_right.clear();
    activity_columns.reset();
    protected_decided.clear();
    score.reset();
  }
};

[[nodiscard]] std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input, std::span<const ExpansionDecision> decisions,
    PlannerScoreScratch& scratch);
[[nodiscard]] std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input, std::span<const ExpansionDecision> decisions,
    PlannerScoreScratch& scratch, const PlannerActivityColumns& columns);

// Runs the deterministic finite V1 beam traversal. A bounded search that
// discarded feasible partials or exhausted a candidate budget never reports
// exhaustive infeasibility.
[[nodiscard]] BeamSearchResult run_beam_search(
    const BeamSearchInput& input, const ReplanBudget& budget);
[[nodiscard]] BeamSearchResult run_beam_search(
    const BeamSearchInput& input, const ReplanBudget& budget,
    PlannerScratch& scratch);

}  // namespace liveroute::planner
