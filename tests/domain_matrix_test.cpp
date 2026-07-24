#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/domain/types.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <stdexcept>
#include <vector>

namespace {

using liveroute::domain::Location;
using liveroute::domain::RouteEstimate;
using liveroute::domain::ActivityTiming;
using liveroute::domain::TimeWindow;
using liveroute::domain::TravelTimeMatrix;
using liveroute::domain::UnixTimeMilliseconds;

bool rejects_invalid_dimensions() {
  try {
    [[maybe_unused]] TravelTimeMatrix matrix(
        2, {{std::chrono::seconds{0}, 0, true}});
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

bool rejects_negative_duration() {
  try {
    [[maybe_unused]] TravelTimeMatrix matrix(
        1, {{std::chrono::seconds{-1}, 0, true}});
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

bool rejects_out_of_range_lookup(const TravelTimeMatrix& matrix) {
  try {
    static_cast<void>(matrix.at(2, 0));
  } catch (const std::out_of_range&) {
    return true;
  }
  return false;
}

}  // namespace

int main() {
  const TravelTimeMatrix matrix(
      2,
      {
          {std::chrono::seconds{0}, 0, true},
          {std::chrono::seconds{180}, 1250, true},
          {std::chrono::seconds{0}, 0, false},
          {std::chrono::seconds{0}, 0, true},
      });

  const auto& forward = matrix.at(0, 1);
  const auto& unreachable = matrix.at(1, 0);
  const TimeWindow valid_window{UnixTimeMilliseconds{100},
                                UnixTimeMilliseconds{200}};
  const TimeWindow invalid_window{UnixTimeMilliseconds{200},
                                  UnixTimeMilliseconds{100}};
  ActivityTiming valid_timing{};
  valid_timing.open_windows = {valid_window};
  valid_timing.min_duration_seconds = 300;
  valid_timing.preferred_duration_seconds = 600;
  valid_timing.max_duration_seconds = 900;
  valid_timing.mandatory = true;
  valid_timing.can_shorten = true;

  ActivityTiming overlapping_timing{};
  overlapping_timing.open_windows = {
      {UnixTimeMilliseconds{100}, UnixTimeMilliseconds{200}},
      {UnixTimeMilliseconds{150}, UnixTimeMilliseconds{250}},
  };
  overlapping_timing.min_duration_seconds = 300;
  overlapping_timing.preferred_duration_seconds = 600;
  overlapping_timing.max_duration_seconds = 900;

  ActivityTiming invalid_duration_timing{};
  invalid_duration_timing.min_duration_seconds = 900;
  invalid_duration_timing.preferred_duration_seconds = 600;
  invalid_duration_timing.max_duration_seconds = 300;
  const Location valid_location{41.8240, -71.4128};
  const Location invalid_location{91.0, -71.4128};

  if (matrix.location_count() != 2 || forward.duration != std::chrono::seconds{180} ||
      forward.distance_meters != 1250 || !forward.reachable ||
      unreachable.reachable || !valid_window.is_valid() ||
      invalid_window.is_valid() || !valid_location.is_valid() ||
      invalid_location.is_valid() || !valid_timing.is_valid() ||
      overlapping_timing.is_valid() || invalid_duration_timing.is_valid() ||
      !rejects_invalid_dimensions() ||
      !rejects_negative_duration() || !rejects_out_of_range_lookup(matrix)) {
    return 1;
  }

  return 0;
}
