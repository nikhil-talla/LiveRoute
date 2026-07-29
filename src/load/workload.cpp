#include "liveroute/load/workload.hpp"

#include <algorithm>
#include <limits>
#include <stdexcept>

namespace liveroute::load {
namespace {

class DeterministicRandom {
 public:
  explicit DeterministicRandom(std::uint64_t seed) : state_(seed) {}

  [[nodiscard]] std::uint64_t next() noexcept {
    state_ = state_ * 6364136223846793005ULL + 1442695040888963407ULL;
    return state_;
  }

 private:
  std::uint64_t state_;
};

[[nodiscard]] WorkloadEventKind event_kind(
    const WorkloadConfiguration& configuration,
    DeterministicRandom& random) noexcept {
  switch (configuration.profile) {
    case WorkloadProfile::kSteadyLocation:
    case WorkloadProfile::kBurstyGps:
      return WorkloadEventKind::kLocation;
    case WorkloadProfile::kReservationHeavy:
      return random.next() % 100 < configuration.reservation_percent
                 ? WorkloadEventKind::kReservationChanged
                 : WorkloadEventKind::kRouteDeviation;
    case WorkloadProfile::kOperatingHours:
      return WorkloadEventKind::kOperatingHoursChanged;
    case WorkloadProfile::kPlaceClosed:
      return WorkloadEventKind::kPlaceFoundClosed;
    case WorkloadProfile::kManyTrips:
    case WorkloadProfile::kHotTrip:
    case WorkloadProfile::kProviderTimeout:
      return WorkloadEventKind::kRouteDeviation;
  }
  return WorkloadEventKind::kLocation;
}

[[nodiscard]] bool is_durable(WorkloadEventKind kind) noexcept {
  return kind == WorkloadEventKind::kReservationChanged ||
         kind == WorkloadEventKind::kOperatingHoursChanged ||
         kind == WorkloadEventKind::kPlaceFoundClosed;
}

}  // namespace

bool WorkloadConfiguration::is_valid() const noexcept {
  return seed != 0 && active_trips != 0 && event_count != 0 &&
         events_per_second != 0 && events_per_second <= 1000000000 &&
         location_burst_size != 0 &&
         suffix_size != 0 && suffix_size <= 64 &&
         reservation_percent <= 100 &&
         deadline > std::chrono::milliseconds::zero() &&
         shard_count != 0 && worker_count != 0 &&
         active_trips <= 65536 && event_count <= 10000000 &&
         active_trips <= 1000000 / suffix_size;
}

std::optional<WorkloadProfile> parse_workload_profile(
    std::string_view value) noexcept {
  if (value == "steady-location") return WorkloadProfile::kSteadyLocation;
  if (value == "bursty-gps") return WorkloadProfile::kBurstyGps;
  if (value == "reservation-heavy") {
    return WorkloadProfile::kReservationHeavy;
  }
  if (value == "operating-hours") return WorkloadProfile::kOperatingHours;
  if (value == "place-closed") return WorkloadProfile::kPlaceClosed;
  if (value == "many-trips") return WorkloadProfile::kManyTrips;
  if (value == "hot-trip") return WorkloadProfile::kHotTrip;
  if (value == "provider-timeout") {
    return WorkloadProfile::kProviderTimeout;
  }
  return std::nullopt;
}

std::string_view workload_profile_name(WorkloadProfile profile) noexcept {
  switch (profile) {
    case WorkloadProfile::kSteadyLocation:
      return "steady-location";
    case WorkloadProfile::kBurstyGps:
      return "bursty-gps";
    case WorkloadProfile::kReservationHeavy:
      return "reservation-heavy";
    case WorkloadProfile::kOperatingHours:
      return "operating-hours";
    case WorkloadProfile::kPlaceClosed:
      return "place-closed";
    case WorkloadProfile::kManyTrips:
      return "many-trips";
    case WorkloadProfile::kHotTrip:
      return "hot-trip";
    case WorkloadProfile::kProviderTimeout:
      return "provider-timeout";
  }
  return "unknown";
}

std::vector<WorkloadEvent> generate_workload(
    const WorkloadConfiguration& configuration) {
  if (!configuration.is_valid()) {
    throw std::invalid_argument("invalid workload configuration");
  }

  DeterministicRandom random(configuration.seed);
  // Sequence 1 is reserved for the runner's unmeasured location priming
  // event, because a higher-epoch bootstrap intentionally contains no
  // non-durable observation state.
  std::vector<std::uint64_t> observations(configuration.active_trips, 1);
  std::vector<std::uint64_t> mutations(configuration.active_trips, 1);
  std::vector<std::uint64_t> trip_revisions(configuration.active_trips, 1);
  std::vector<WorkloadEvent> result;
  result.reserve(configuration.event_count);

  const auto interval = std::chrono::nanoseconds{
      1000000000ULL / configuration.events_per_second};
  for (std::size_t index = 0; index < configuration.event_count; ++index) {
    std::size_t trip_index = 0;
    if (configuration.profile == WorkloadProfile::kManyTrips) {
      trip_index = index % configuration.active_trips;
    } else if (configuration.profile != WorkloadProfile::kHotTrip) {
      trip_index = random.next() % configuration.active_trips;
    }
    const auto kind = event_kind(configuration, random);
    const bool durable = is_durable(kind);
    auto scheduled_index = index;
    if (configuration.profile == WorkloadProfile::kBurstyGps) {
      scheduled_index =
          (index / configuration.location_burst_size) *
          configuration.location_burst_size;
    }
    WorkloadEvent event{
        .event_index = index,
        .trip_index = trip_index,
        .kind = kind,
        .observation_sequence = 0,
        .mutation_sequence = 0,
        .expected_trip_revision = 0,
        .scheduled_offset = interval * scheduled_index,
    };
    if (durable) {
      event.mutation_sequence = ++mutations[trip_index];
      event.expected_trip_revision = trip_revisions[trip_index]++;
    } else {
      event.observation_sequence = ++observations[trip_index];
    }
    result.push_back(event);
  }
  return result;
}

std::uint64_t percentile(std::vector<std::uint64_t> values,
                         std::uint32_t percentile_rank) {
  if (values.empty() || percentile_rank == 0 || percentile_rank > 100) {
    return 0;
  }
  std::sort(values.begin(), values.end());
  const auto numerator =
      static_cast<std::uint64_t>(percentile_rank) * values.size();
  const auto rank = (numerator + 99) / 100;
  return values[static_cast<std::size_t>(rank - 1)];
}

}  // namespace liveroute::load
