#include <chrono>
#include <cstdint>
#include <iostream>
#include <limits>
#include <stop_token>
#include <vector>

#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/routing/travel_time_provider.hpp"

namespace {

using liveroute::domain::Location;
using liveroute::domain::RouteEstimate;
using liveroute::domain::TravelMode;
using liveroute::domain::TravelTimeMatrix;
using liveroute::routing::FixedTravelTimeProvider;
using liveroute::routing::TravelTimeProviderError;

TravelTimeMatrix two_location_matrix() {
  return TravelTimeMatrix(
      2, {{std::chrono::seconds{0}, 0, true},
          {std::chrono::seconds{10}, 100, true},
          {std::chrono::seconds{12}, 120, true},
          {std::chrono::seconds{0}, 0, true}});
}

bool check_preflight_and_success() {
  FixedTravelTimeProvider provider(2, two_location_matrix());
  const std::vector<Location> locations = {{41.8, -71.4}, {41.7, -71.5}};
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds{1};

  const auto success = provider.get_matrix(
      locations, TravelMode::kDriving, std::chrono::system_clock::now(), deadline,
      {});
  if (!success.has_matrix() || success.matrix().at(0, 1).distance_meters != 100) {
    return false;
  }

  const std::vector<Location> invalid = {
      {std::numeric_limits<double>::infinity(), -71.4}, {41.7, -71.5}};
  const auto invalid_result = provider.get_matrix(
      invalid, TravelMode::kDriving, std::chrono::system_clock::now(), deadline,
      {});
  if (invalid_result.has_matrix() ||
      invalid_result.error() != TravelTimeProviderError::kInvalidArgument) {
    return false;
  }

  const std::vector<Location> too_many = {
      {41.8, -71.4}, {41.7, -71.5}, {41.6, -71.6}};
  const auto too_large = provider.get_matrix(
      too_many, TravelMode::kDriving, std::chrono::system_clock::now(), deadline,
      {});
  return !too_large.has_matrix() &&
         too_large.error() == TravelTimeProviderError::kMatrixTooLarge;
}

bool check_cancellation_and_deadline() {
  FixedTravelTimeProvider provider(2, two_location_matrix());
  const std::vector<Location> locations = {{41.8, -71.4}, {41.7, -71.5}};
  std::stop_source cancelled;
  cancelled.request_stop();

  const auto cancelled_result = provider.get_matrix(
      locations, TravelMode::kWalking, std::chrono::system_clock::now(),
      std::chrono::steady_clock::now() + std::chrono::seconds{1},
      cancelled.get_token());
  const auto expired_result = provider.get_matrix(
      locations, TravelMode::kWalking, std::chrono::system_clock::now(),
      std::chrono::steady_clock::now() - std::chrono::milliseconds{1}, {});

  return !cancelled_result.has_matrix() &&
         cancelled_result.error() == TravelTimeProviderError::kCancelled &&
         !expired_result.has_matrix() &&
         expired_result.error() == TravelTimeProviderError::kDeadlineExceeded;
}

bool check_retryability() {
  using liveroute::routing::is_retryable;
  return !is_retryable(TravelTimeProviderError::kInvalidArgument) &&
         !is_retryable(TravelTimeProviderError::kMatrixTooLarge) &&
         is_retryable(TravelTimeProviderError::kResourceExhausted) &&
         is_retryable(TravelTimeProviderError::kDeadlineExceeded) &&
         is_retryable(TravelTimeProviderError::kProviderUnavailable) &&
         !is_retryable(TravelTimeProviderError::kInternal);
}

}  // namespace

int main() {
  if (!check_preflight_and_success() || !check_cancellation_and_deadline() ||
      !check_retryability()) {
    std::cerr << "travel-time provider test failed\n";
    return 1;
  }
  return 0;
}
