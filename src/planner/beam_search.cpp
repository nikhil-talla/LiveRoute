#include "liveroute/planner/beam_search.hpp"

#include <algorithm>

namespace liveroute::planner {

bool BeamSearchInput::is_valid() const noexcept {
  if (planning_horizon_start >= planning_horizon_end ||
      current_time < planning_horizon_start || current_time > planning_horizon_end ||
      remaining_activities.size() > 64 || travel_time_matrix == nullptr ||
      travel_time_matrix->location_count() != remaining_activities.size() + 1) {
    return false;
  }
  std::vector<std::size_t> ordinals;
  ordinals.reserve(remaining_activities.size());
  for (const auto& activity : remaining_activities) {
    if (!activity.is_valid()) return false;
    ordinals.push_back(activity.original_trip_ordinal);
  }
  std::sort(ordinals.begin(), ordinals.end());
  return std::adjacent_find(ordinals.begin(), ordinals.end()) == ordinals.end();
}

}  // namespace liveroute::planner
