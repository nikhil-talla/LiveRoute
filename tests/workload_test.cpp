#include "liveroute/load/workload.hpp"

#include <chrono>
#include <stdexcept>

namespace {

using namespace liveroute::load;

}  // namespace

int main() {
  WorkloadConfiguration invalid;
  invalid.active_trips = 0;
  if (invalid.is_valid()) return 1;

  WorkloadConfiguration burst{
      .profile = WorkloadProfile::kBurstyGps,
      .seed = 7,
      .active_trips = 3,
      .event_count = 7,
      .events_per_second = 100,
      .location_burst_size = 3,
      .suffix_size = 2,
      .reservation_percent = 75,
      .deadline = std::chrono::milliseconds{50},
      .shard_count = 2,
      .worker_count = 2,
  };
  const auto first = generate_workload(burst);
  const auto second = generate_workload(burst);
  if (first != second || first.size() != 7 ||
      first[0].scheduled_offset != first[2].scheduled_offset ||
      first[3].scheduled_offset != std::chrono::milliseconds{30} ||
      first[0].observation_sequence == 0 ||
      first[0].mutation_sequence != 0) {
    return 1;
  }

  auto durable = burst;
  durable.profile = WorkloadProfile::kReservationHeavy;
  durable.reservation_percent = 100;
  const auto reservations = generate_workload(durable);
  for (const auto& event : reservations) {
    if (event.kind != WorkloadEventKind::kReservationChanged ||
        event.mutation_sequence < 2 ||
        event.expected_trip_revision == 0 ||
        event.observation_sequence != 0) {
      return 1;
    }
  }

  if (parse_workload_profile("hot-trip") !=
          WorkloadProfile::kHotTrip ||
      parse_workload_profile("missing").has_value() ||
      workload_profile_name(WorkloadProfile::kProviderTimeout) !=
          "provider-timeout" ||
      percentile({9, 1, 5, 3}, 50) != 3 ||
      percentile({9, 1, 5, 3}, 95) != 9 ||
      percentile({}, 99) != 0) {
    return 1;
  }

  bool threw = false;
  try {
    (void)generate_workload(invalid);
  } catch (const std::invalid_argument&) {
    threw = true;
  }
  return threw ? 0 : 1;
}
