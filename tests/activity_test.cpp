#include "liveroute/domain/activity.hpp"

#include <array>
#include <cstddef>
#include <optional>

namespace {

using liveroute::domain::Activity;
using liveroute::domain::ActivityClass;
using liveroute::domain::ActivityId;
using liveroute::domain::ActivityState;
using liveroute::domain::ActivityTiming;
using liveroute::domain::Location;
using liveroute::domain::PlaceId;
using liveroute::domain::TimeWindow;
using liveroute::domain::TravelMode;
using liveroute::domain::UnixTimeMilliseconds;

Activity valid_activity() {
  std::array<std::byte, 16> bytes{};
  bytes.front() = std::byte{1};
  return {
      .activity_id = ActivityId{bytes},
      .place_id = PlaceId{"fixture-place"},
      .display_name = "Fixture",
      .location = Location{40.0, -74.0},
      .time_zone_name = "America/New_York",
      .inbound_travel_mode = TravelMode::kWalking,
      .activity_class = ActivityClass::kFlexible,
      .activity_state = ActivityState::kPlanned,
      .priority_rank = 0,
      .utility_score = 1,
      .timing = ActivityTiming{
          .open_windows = {{UnixTimeMilliseconds{100}, UnixTimeMilliseconds{1000}}},
          .reservation_start = std::nullopt,
          .reservation_grace_seconds = 0,
          .min_duration_seconds = 60,
          .preferred_duration_seconds = 120,
          .max_duration_seconds = 180,
          .mandatory = false,
          .can_shorten = true,
          .can_move = true,
          .can_skip = true,
          .mandatory_deadline = std::nullopt,
      },
      .activity_delay_seconds = 0,
      .found_closed_at = std::nullopt,
  };
}

}  // namespace

int main() {
  const auto activity = valid_activity();
  auto invalid_location = activity;
  invalid_location.location.latitude = 91.0;
  auto invalid_mode = activity;
  invalid_mode.inbound_travel_mode = static_cast<TravelMode>(99);
  auto invalid_state = activity;
  invalid_state.activity_state = static_cast<ActivityState>(99);
  return activity.is_valid() && !invalid_location.is_valid() &&
                 !invalid_mode.is_valid() && !invalid_state.is_valid()
             ? 0
             : 1;
}
