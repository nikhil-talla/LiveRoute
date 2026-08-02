#include "liveroute/routing/travel_time_provider.hpp"

#include <stdexcept>
#include <utility>

namespace liveroute::routing {

TravelTimeLookupResult::TravelTimeLookupResult(domain::TravelTimeMatrix matrix)
    : value_(std::move(matrix)) {}

TravelTimeLookupResult::TravelTimeLookupResult(
    domain::TravelTimeMatrix matrix, TravelTimeLookupQuality quality)
    : value_(std::move(matrix)), quality_(quality) {}

TravelTimeLookupResult::TravelTimeLookupResult(
    TravelTimeProviderError error) noexcept
    : value_(error) {}

bool TravelTimeLookupResult::has_matrix() const noexcept {
  return std::holds_alternative<domain::TravelTimeMatrix>(value_);
}

const domain::TravelTimeMatrix& TravelTimeLookupResult::matrix() const {
  if (!has_matrix()) {
    throw std::logic_error("travel-time lookup did not return a matrix");
  }
  return std::get<domain::TravelTimeMatrix>(value_);
}

TravelTimeProviderError TravelTimeLookupResult::error() const {
  if (has_matrix()) {
    throw std::logic_error("travel-time lookup did not return an error");
  }
  return std::get<TravelTimeProviderError>(value_);
}

TravelTimeLookupQuality TravelTimeLookupResult::quality() const {
  if (!has_matrix()) {
    throw std::logic_error("travel-time lookup did not return a matrix");
  }
  return quality_;
}

FixedTravelTimeProvider::FixedTravelTimeProvider(
    std::size_t max_locations, domain::TravelTimeMatrix matrix)
    : max_locations_(max_locations), matrix_(std::move(matrix)) {
  if (max_locations_ == 0 || matrix_.location_count() > max_locations_) {
    throw std::invalid_argument("invalid fixed travel-time provider limits");
  }
}

TravelTimeLookupResult FixedTravelTimeProvider::get_matrix(
    std::span<const domain::Location> locations, domain::TravelMode mode,
    std::chrono::system_clock::time_point departure_time,
    domain::Deadline deadline, std::stop_token stop_token) {
  static_cast<void>(departure_time);

  for (const auto& location : locations) {
    if (!location.is_valid()) {
      return TravelTimeLookupResult{TravelTimeProviderError::kInvalidArgument};
    }
  }
  if (mode != domain::TravelMode::kWalking &&
      mode != domain::TravelMode::kDriving) {
    return TravelTimeLookupResult{TravelTimeProviderError::kInvalidArgument};
  }
  if (locations.size() > max_locations_) {
    return TravelTimeLookupResult{TravelTimeProviderError::kMatrixTooLarge};
  }
  if (stop_token.stop_requested()) {
    return TravelTimeLookupResult{TravelTimeProviderError::kCancelled};
  }
  if (std::chrono::steady_clock::now() >= deadline) {
    return TravelTimeLookupResult{TravelTimeProviderError::kDeadlineExceeded};
  }
  if (locations.size() != matrix_.location_count()) {
    return TravelTimeLookupResult{TravelTimeProviderError::kInvalidArgument};
  }
  return TravelTimeLookupResult{matrix_};
}

}  // namespace liveroute::routing
