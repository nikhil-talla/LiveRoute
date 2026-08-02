#include <chrono>
#include <cstdlib>
#include <iostream>
#include <string>
#include <vector>

#include "liveroute/routing/osrm_travel_time_provider.hpp"

namespace {

std::string endpoint(const char* name, const char* fallback) {
  const auto* value = std::getenv(name);
  return value == nullptr ? fallback : value;
}

bool check_mode(liveroute::routing::OsrmTravelTimeProvider& provider,
                liveroute::domain::TravelMode mode) {
  const std::vector<liveroute::domain::Location> locations = {
      {41.8240, -71.4128}, {41.8300, -71.4150}};
  const auto result = provider.get_matrix(
      locations, mode, std::chrono::system_clock::now(),
      std::chrono::steady_clock::now() + std::chrono::seconds{2}, {});
  if (!result.has_matrix()) {
    std::cerr << "OSRM lookup error " << static_cast<int>(result.error()) << '\n';
    return false;
  }
  const auto& matrix = result.matrix();
  if (result.quality() != liveroute::routing::TravelTimeLookupQuality::kFresh ||
      matrix.location_count() != 2 || !matrix.at(0, 0).reachable ||
      !matrix.at(1, 1).reachable || !matrix.at(0, 1).reachable ||
      matrix.at(0, 1).duration <= std::chrono::seconds::zero() ||
      matrix.at(0, 1).distance_meters == 0) {
    return false;
  }
  const auto cached = provider.get_matrix(
      locations, mode, std::chrono::system_clock::now(),
      std::chrono::steady_clock::now() + std::chrono::seconds{2}, {});
  return cached.has_matrix() &&
         cached.quality() == liveroute::routing::TravelTimeLookupQuality::kFresh &&
         cached.matrix().location_count() == 2 && cached.matrix().at(0, 1).reachable &&
         cached.matrix().at(0, 1).duration == matrix.at(0, 1).duration &&
         cached.matrix().at(0, 1).distance_meters == matrix.at(0, 1).distance_meters;
}

}  // namespace

int main() {
  liveroute::routing::OsrmTravelTimeProvider provider({
      .dataset_version = "test-osrm-dataset-v1",
      .car_endpoint = endpoint("LIVEROUTE_OSRM_CAR_ENDPOINT", "http://osrm-car:5000"),
      .foot_endpoint = endpoint("LIVEROUTE_OSRM_FOOT_ENDPOINT", "http://osrm-foot:5000"),
      .max_locations = 65,
      .max_matrix_cells = 4225,
      .max_encoded_request_bytes = 8192,
      .max_response_bytes = 1048576,
      .per_profile_concurrency = 2,
      .connect_timeout = std::chrono::milliseconds{100},
      .request_timeout = std::chrono::milliseconds{750},
      .route_cache = liveroute::routing::RouteMatrixCacheConfig{},
  });
  if (!check_mode(provider, liveroute::domain::TravelMode::kDriving) ||
      !check_mode(provider, liveroute::domain::TravelMode::kWalking)) {
    std::cerr << "OSRM provider integration test failed\n";
    return 1;
  }
  return 0;
}
