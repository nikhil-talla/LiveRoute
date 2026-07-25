#include "liveroute/planner/checked_int.hpp"

#include <cstdint>
#include <limits>

int main() {
  using liveroute::planner::checked_absolute_difference;
  using liveroute::planner::checked_add;
  using liveroute::planner::checked_milliseconds;
  using liveroute::planner::checked_subtract;
  const auto maximum = std::numeric_limits<std::int64_t>::max();
  const auto minimum = std::numeric_limits<std::int64_t>::min();
  return checked_add(1, 2) == 3 && !checked_add(maximum, 1) &&
                 !checked_add(minimum, -1) && checked_subtract(4, 3) == 1 &&
                 !checked_subtract(maximum, -1) &&
                 checked_milliseconds(42) == 42000 &&
                 checked_absolute_difference(4, -3) == 7 &&
                 !checked_absolute_difference(minimum, 0)
             ? 0
             : 1;
}
