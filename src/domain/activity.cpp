#include "liveroute/domain/activity.hpp"

namespace liveroute::domain {

bool Activity::is_valid() const noexcept {
  if (place_id.value.empty() || display_name.empty() || time_zone_name.empty() ||
      !location.is_valid() || !timing.is_valid()) {
    return false;
  }
  switch (inbound_travel_mode) {
    case TravelMode::kWalking:
    case TravelMode::kDriving:
      break;
    default:
      return false;
  }
  switch (activity_class) {
    case ActivityClass::kFixed:
    case ActivityClass::kFlexible:
      break;
    default:
      return false;
  }
  switch (activity_state) {
    case ActivityState::kPlanned:
    case ActivityState::kStarted:
    case ActivityState::kCompleted:
    case ActivityState::kSkipped:
      return true;
  }
  return false;
}

}  // namespace liveroute::domain
