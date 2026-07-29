#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <string_view>
#include <vector>

namespace liveroute::load {

enum class WorkloadProfile : std::uint8_t {
  kSteadyLocation,
  kBurstyGps,
  kReservationHeavy,
  kOperatingHours,
  kPlaceClosed,
  kManyTrips,
  kHotTrip,
  kProviderTimeout,
};

enum class WorkloadEventKind : std::uint8_t {
  kLocation,
  kRouteDeviation,
  kReservationChanged,
  kOperatingHoursChanged,
  kPlaceFoundClosed,
};

struct WorkloadConfiguration {
  WorkloadProfile profile{WorkloadProfile::kManyTrips};
  std::uint64_t seed{1};
  std::size_t active_trips{16};
  std::size_t event_count{1000};
  std::size_t events_per_second{1000};
  std::size_t location_burst_size{5};
  std::size_t suffix_size{8};
  std::uint32_t reservation_percent{75};
  std::chrono::milliseconds deadline{100};
  std::size_t shard_count{4};
  std::size_t worker_count{4};

  [[nodiscard]] bool is_valid() const noexcept;
};

struct WorkloadEvent {
  std::size_t event_index{};
  std::size_t trip_index{};
  WorkloadEventKind kind{WorkloadEventKind::kLocation};
  std::uint64_t observation_sequence{};
  std::uint64_t mutation_sequence{};
  std::uint64_t expected_trip_revision{};
  std::chrono::nanoseconds scheduled_offset{};

  friend bool operator==(const WorkloadEvent&,
                         const WorkloadEvent&) = default;
};

[[nodiscard]] std::optional<WorkloadProfile> parse_workload_profile(
    std::string_view value) noexcept;
[[nodiscard]] std::string_view workload_profile_name(
    WorkloadProfile profile) noexcept;
[[nodiscard]] std::vector<WorkloadEvent> generate_workload(
    const WorkloadConfiguration& configuration);
[[nodiscard]] std::uint64_t percentile(
    std::vector<std::uint64_t> values, std::uint32_t percentile_rank);

}  // namespace liveroute::load
