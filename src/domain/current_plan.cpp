#include "liveroute/domain/current_plan.hpp"

#include <algorithm>

namespace liveroute::domain {

bool CurrentPlanSegment::is_valid() const noexcept {
  switch (state) {
    case PlanEntryState::kScheduled:
      return scheduled_start.has_value() && scheduled_end.has_value() &&
             *scheduled_start < *scheduled_end;
    case PlanEntryState::kOmitted:
      return !scheduled_start.has_value() && !scheduled_end.has_value();
  }
  return false;
}

bool CurrentPlan::is_valid_for(std::span<const ActivityId> activity_ids) const {
  if (plan_revision == 0 || segments.size() != activity_ids.size()) return false;
  if ((origin == PlanOrigin::kUserAuthored && source_proposal_id.has_value()) ||
      (origin == PlanOrigin::kAcceptedEngineProposal && !source_proposal_id.has_value())) {
    return false;
  }

  std::vector<ActivityId> expected{activity_ids.begin(), activity_ids.end()};
  std::vector<ActivityId> actual;
  actual.reserve(segments.size());
  std::optional<UnixTimeMilliseconds> prior_scheduled_end;
  for (const auto& segment : segments) {
    if (!segment.is_valid()) return false;
    actual.push_back(segment.activity_id);
    if (segment.state == PlanEntryState::kScheduled) {
      if (prior_scheduled_end.has_value() &&
          *segment.scheduled_start < *prior_scheduled_end) {
        return false;
      }
      prior_scheduled_end = segment.scheduled_end;
    }
  }

  std::sort(expected.begin(), expected.end());
  std::sort(actual.begin(), actual.end());
  if (std::adjacent_find(expected.begin(), expected.end()) != expected.end() ||
      std::adjacent_find(actual.begin(), actual.end()) != actual.end()) {
    return false;
  }
  return expected == actual;
}

}  // namespace liveroute::domain
