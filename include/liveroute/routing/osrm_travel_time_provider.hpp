#pragma once

#include <chrono>
#include <cstddef>
#include <memory>
#include <string>

#include "liveroute/routing/travel_time_provider.hpp"

namespace liveroute::routing {

struct OsrmTravelTimeProviderConfig {
  std::string car_endpoint;
  std::string foot_endpoint;
  std::size_t max_locations{};
  std::size_t max_matrix_cells{};
  std::size_t max_encoded_request_bytes{};
  std::size_t max_response_bytes{};
  std::size_t per_profile_concurrency{};
  std::chrono::milliseconds connect_timeout{};
  std::chrono::milliseconds request_timeout{};
};

class OsrmTravelTimeProvider final : public TravelTimeProvider {
 public:
  explicit OsrmTravelTimeProvider(OsrmTravelTimeProviderConfig config);
  ~OsrmTravelTimeProvider() override;

  OsrmTravelTimeProvider(const OsrmTravelTimeProvider&) = delete;
  OsrmTravelTimeProvider& operator=(const OsrmTravelTimeProvider&) = delete;

  TravelTimeLookupResult get_matrix(
      std::span<const domain::Location> locations, domain::TravelMode mode,
      std::chrono::system_clock::time_point departure_time,
      domain::Deadline deadline, std::stop_token stop_token) override;

 private:
  class Impl;
  std::unique_ptr<Impl> impl_;
};

}  // namespace liveroute::routing
