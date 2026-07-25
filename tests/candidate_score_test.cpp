#include "liveroute/planner/candidate_score.hpp"

namespace {

using liveroute::planner::CandidateScore;
using liveroute::planner::ExpansionDecision;
using liveroute::planner::is_better_complete;
using liveroute::planner::is_better_partial;

CandidateScore base_score() {
  return {
      .skips_by_priority = {0, 0},
      .scheduled_utility = 10,
      .total_lateness_ms = 0,
      .total_preferred_shortfall_ms = 0,
      .total_travel_ms = 0,
      .changed_activity_count = 0,
      .total_start_shift_ms = 0,
      .final_scheduled_end_unix_ms = 100,
      .canonical_plan_key = {},
  };
}

}  // namespace

int main() {
  const auto baseline = base_score();
  auto lower_priority_skip = baseline;
  lower_priority_skip.skips_by_priority[1] = 1;
  auto higher_priority_skip = baseline;
  higher_priority_skip.skips_by_priority[0] = 1;
  higher_priority_skip.scheduled_utility = 1000000;
  if (!is_better_complete(baseline, lower_priority_skip) ||
      !is_better_complete(lower_priority_skip, higher_priority_skip) ||
      is_better_complete(higher_priority_skip, lower_priority_skip)) {
    return 1;
  }

  auto utility = baseline;
  utility.scheduled_utility = 11;
  auto late = utility;
  late.total_lateness_ms = 1;
  if (!is_better_complete(utility, late) || !is_better_complete(utility, baseline)) {
    return 1;
  }

  auto first_key = baseline;
  first_key.canonical_plan_key.scheduled_ordinals_in_order = {1, 2};
  auto second_key = baseline;
  second_key.canonical_plan_key.scheduled_ordinals_in_order = {2, 1};
  if (!is_better_complete(first_key, second_key) ||
      is_better_complete(second_key, first_key)) {
    return 1;
  }

  const std::vector<ExpansionDecision> first_decisions{{1, 0, 10, 20}};
  const std::vector<ExpansionDecision> second_decisions{{1, 1, 0, 0}};
  if (!is_better_partial(baseline, first_decisions, baseline, second_decisions) ||
      is_better_partial(baseline, second_decisions, baseline, first_decisions)) {
    return 1;
  }
  auto invalid = base_score();
  invalid.total_travel_ms = -1;
  auto invalid_key = base_score();
  invalid_key.canonical_plan_key.scheduled_ordinals_in_order = {1};
  invalid_key.canonical_plan_key.scheduled_entries = {{1, 10, 10}};
  return baseline.is_valid() && !invalid.is_valid() && !invalid_key.is_valid() ? 0 : 1;
}
