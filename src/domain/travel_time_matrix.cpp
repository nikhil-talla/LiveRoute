#include "liveroute/domain/travel_time_matrix.hpp"

#include <limits>
#include <stdexcept>
#include <string>
#include <utility>

namespace liveroute::domain {

TravelTimeMatrix::TravelTimeMatrix(std::size_t location_count,
                                   std::vector<RouteEstimate> estimates)
    : location_count_(location_count), estimates_(std::move(estimates)) {
  if (location_count_ > std::numeric_limits<std::size_t>::max() /
                            location_count_) {
    throw std::invalid_argument("travel-time matrix dimensions overflow");
  }

  const auto expected_entries = location_count_ * location_count_;
  if (estimates_.size() != expected_entries) {
    throw std::invalid_argument("travel-time matrix has invalid dimensions");
  }

  for (const auto& estimate : estimates_) {
    if (!estimate.is_valid()) {
      throw std::invalid_argument("travel-time matrix has negative duration");
    }
  }
}

const RouteEstimate& TravelTimeMatrix::at(std::size_t origin,
                                          std::size_t destination) const {
  if (origin >= location_count_ || destination >= location_count_) {
    throw std::out_of_range("travel-time matrix index out of range");
  }

  return estimates_[origin * location_count_ + destination];
}

}  // namespace liveroute::domain
