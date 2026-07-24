#pragma once

#include <array>
#include <chrono>
#include <compare>
#include <cstddef>
#include <cstdint>
#include <cmath>
#include <optional>
#include <string>
#include <utility>
#include <vector>

namespace liveroute::domain {

template <typename Tag, typename Rep>
class StrongValue {
 public:
  constexpr explicit StrongValue(Rep value) : value_(value) {}

  [[nodiscard]] constexpr Rep value() const noexcept { return value_; }
  constexpr auto operator<=>(const StrongValue&) const = default;

 private:
  Rep value_;
};

struct TripIdTag;
struct ActivityIdTag;
struct MutationSequenceTag;
struct ObservationSequenceTag;
struct TripRevisionTag;
struct PlannerStateVersionTag;
struct UnixTimeMillisecondsTag;

using TripId = StrongValue<TripIdTag, std::array<std::byte, 16>>;
using ActivityId = StrongValue<ActivityIdTag, std::array<std::byte, 16>>;
using MutationSequence = StrongValue<MutationSequenceTag, std::uint64_t>;
using ObservationSequence = StrongValue<ObservationSequenceTag, std::uint64_t>;
using TripRevision = StrongValue<TripRevisionTag, std::uint64_t>;
using PlannerStateVersion = StrongValue<PlannerStateVersionTag, std::uint64_t>;
using UnixTimeMilliseconds = StrongValue<UnixTimeMillisecondsTag, std::int64_t>;

struct Location {
  double latitude{};
  double longitude{};

  [[nodiscard]] bool is_valid() const noexcept {
    return std::isfinite(latitude) && std::isfinite(longitude) &&
           latitude >= -90.0 && latitude <= 90.0 && longitude >= -180.0 &&
           longitude <= 180.0;
  }
};

struct TimeWindow {
  UnixTimeMilliseconds opens_at{0};
  UnixTimeMilliseconds closes_at{0};

  [[nodiscard]] constexpr bool is_valid() const noexcept {
    return opens_at < closes_at;
  }
};

struct ActivityTiming {
  std::vector<TimeWindow> open_windows;
  std::optional<UnixTimeMilliseconds> reservation_start;
  std::uint32_t reservation_grace_seconds{};
  std::uint32_t min_duration_seconds{};
  std::uint32_t preferred_duration_seconds{};
  std::uint32_t max_duration_seconds{};
  bool mandatory{};
  bool can_shorten{};
  bool can_move{};
  bool can_skip{};
  std::optional<UnixTimeMilliseconds> mandatory_deadline;

  [[nodiscard]] bool is_valid() const noexcept {
    if (min_duration_seconds > preferred_duration_seconds ||
        preferred_duration_seconds > max_duration_seconds) {
      return false;
    }

    std::optional<UnixTimeMilliseconds> previous_close;
    for (const auto& window : open_windows) {
      if (!window.is_valid() ||
          (previous_close.has_value() && window.opens_at < *previous_close)) {
        return false;
      }
      previous_close = window.closes_at;
    }
    return true;
  }
};

struct PlaceId {
  std::string value;

  friend bool operator==(const PlaceId&, const PlaceId&) = default;
};

enum class TravelMode : std::uint8_t {
  kWalking,
  kDriving,
};

enum class EventPriority : std::uint8_t {
  kCritical,
  kHigh,
  kNormal,
  kAdvisory,
};

using Deadline = std::chrono::steady_clock::time_point;

}  // namespace liveroute::domain
