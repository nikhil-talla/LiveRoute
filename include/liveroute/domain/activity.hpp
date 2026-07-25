#pragma once

#include <cstdint>
#include <optional>
#include <string>

#include "liveroute/domain/types.hpp"

namespace liveroute::domain {

enum class ActivityClass : std::uint8_t {
  kFixed,
  kFlexible,
};

enum class ActivityState : std::uint8_t {
  kPlanned,
  kStarted,
  kCompleted,
  kSkipped,
};

struct Activity {
  ActivityId activity_id;
  PlaceId place_id;
  std::string display_name;
  Location location;
  std::string time_zone_name;
  TravelMode inbound_travel_mode;
  ActivityClass activity_class;
  ActivityState activity_state;
  std::int32_t priority_rank{};
  std::int32_t utility_score{};
  ActivityTiming timing;
  std::uint32_t activity_delay_seconds{};
  std::optional<UnixTimeMilliseconds> found_closed_at;

  [[nodiscard]] bool is_valid() const noexcept;
};

}  // namespace liveroute::domain
