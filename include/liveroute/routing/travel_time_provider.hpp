#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <span>
#include <stop_token>
#include <variant>
#include <vector>

#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/domain/types.hpp"

namespace liveroute::routing {

enum class TravelTimeProviderError : std::uint8_t {
  kInvalidArgument,
  kMatrixTooLarge,
  kResourceExhausted,
  kCancelled,
  kDeadlineExceeded,
  kProviderUnavailable,
  kInternal,
};

enum class TravelTimeLookupQuality : std::uint8_t {
  kFresh,
  kStaleCache,
};

[[nodiscard]] constexpr bool is_retryable(
    TravelTimeProviderError error) noexcept {
  switch (error) {
    case TravelTimeProviderError::kResourceExhausted:
    case TravelTimeProviderError::kDeadlineExceeded:
    case TravelTimeProviderError::kProviderUnavailable:
      return true;
    case TravelTimeProviderError::kInvalidArgument:
    case TravelTimeProviderError::kMatrixTooLarge:
    case TravelTimeProviderError::kCancelled:
    case TravelTimeProviderError::kInternal:
      return false;
  }
  return false;
}

class TravelTimeLookupResult {
 public:
  explicit TravelTimeLookupResult(domain::TravelTimeMatrix matrix);
  TravelTimeLookupResult(domain::TravelTimeMatrix matrix,
                         TravelTimeLookupQuality quality);
  explicit TravelTimeLookupResult(TravelTimeProviderError error) noexcept;

  [[nodiscard]] bool has_matrix() const noexcept;
  [[nodiscard]] const domain::TravelTimeMatrix& matrix() const;
  [[nodiscard]] TravelTimeProviderError error() const;
  [[nodiscard]] TravelTimeLookupQuality quality() const;

 private:
  std::variant<domain::TravelTimeMatrix, TravelTimeProviderError> value_;
  TravelTimeLookupQuality quality_{TravelTimeLookupQuality::kFresh};
};

class TravelTimeProvider {
 public:
  virtual ~TravelTimeProvider() = default;

  virtual TravelTimeLookupResult get_matrix(
      std::span<const domain::Location> locations, domain::TravelMode mode,
      std::chrono::system_clock::time_point departure_time,
      domain::Deadline deadline, std::stop_token stop_token) = 0;
};

class FixedTravelTimeProvider final : public TravelTimeProvider {
 public:
  FixedTravelTimeProvider(std::size_t max_locations,
                          domain::TravelTimeMatrix matrix);

  TravelTimeLookupResult get_matrix(
      std::span<const domain::Location> locations, domain::TravelMode mode,
      std::chrono::system_clock::time_point departure_time,
      domain::Deadline deadline, std::stop_token stop_token) override;

 private:
  std::size_t max_locations_;
  domain::TravelTimeMatrix matrix_;
};

}  // namespace liveroute::routing
