#include <stdexcept>
#include <vector>

#include "liveroute/domain/types.hpp"
#include "liveroute/runtime/priority_lanes.hpp"

namespace {

using liveroute::domain::EventPriority;
using liveroute::runtime::BoundedPriorityLanes;
using liveroute::runtime::PriorityLaneCapacities;

bool rejects_invalid_configuration() {
  try {
    [[maybe_unused]] BoundedPriorityLanes<int> zero_capacity(
        {1, 1, 0, 1}, 1);
  } catch (const std::invalid_argument&) {
    try {
      [[maybe_unused]] BoundedPriorityLanes<int> zero_burst({1, 1, 1, 1}, 0);
    } catch (const std::invalid_argument&) {
      return true;
    }
  }
  return false;
}

bool preserves_priority_fifo_and_normal_progress() {
  BoundedPriorityLanes<int> lanes({2, 2, 2, 2}, 2);
  if (!lanes.try_push(EventPriority::kNormal, 10) ||
      !lanes.try_push(EventPriority::kHigh, 20) ||
      !lanes.try_push(EventPriority::kCritical, 30) ||
      !lanes.try_push(EventPriority::kHigh, 21) ||
      !lanes.try_push(EventPriority::kCritical, 31)) {
    return false;
  }

  std::vector<int> observed;
  while (const auto value = lanes.try_pop()) {
    observed.push_back(*value);
  }
  return observed == std::vector<int>{30, 31, 10, 20, 21} &&
         lanes.size() == 0;
}

bool keeps_lane_capacities_independent() {
  BoundedPriorityLanes<int> lanes({1, 1, 1, 1}, 1);
  return lanes.try_push(EventPriority::kAdvisory, 1) &&
         !lanes.try_push(EventPriority::kAdvisory, 2) &&
         lanes.try_push(EventPriority::kCritical, 3) &&
         lanes.size(EventPriority::kAdvisory) == 1 && lanes.size() == 2;
}

}  // namespace

int main() {
  return rejects_invalid_configuration() &&
                 preserves_priority_fifo_and_normal_progress() &&
                 keeps_lane_capacities_independent()
             ? 0
             : 1;
}
