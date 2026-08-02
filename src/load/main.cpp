#include "liveroute/load/workload.hpp"

#include <algorithm>
#include <array>
#include <atomic>
#include <charconv>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <iostream>
#include <limits>
#include <mutex>
#include <optional>
#include <span>
#include <stop_token>
#include <string_view>
#include <thread>
#include <vector>

#include "liveroute/domain/trip_state.hpp"
#include "liveroute/routing/travel_time_provider.hpp"
#include "liveroute/runtime/concurrent_trip_runtime.hpp"

namespace {

using namespace std::chrono_literals;
using namespace liveroute::domain;
using namespace liveroute::load;
using namespace liveroute::routing;
using namespace liveroute::runtime;

template <typename Id>
Id id(std::uint64_t value) {
  std::array<std::byte, 16> bytes{};
  for (std::size_t index = 0; index < 8; ++index) {
    bytes[15 - index] =
        static_cast<std::byte>((value >> (index * 8)) & 0xffU);
  }
  return Id{bytes};
}

[[nodiscard]] Activity load_activity(std::size_t trip_index,
                                     std::size_t activity_index) {
  const auto marker =
      (static_cast<std::uint64_t>(trip_index + 1) << 32U) |
      static_cast<std::uint64_t>(activity_index + 1);
  return {
      .activity_id = id<ActivityId>(marker),
      .place_id = PlaceId{"load-place"},
      .display_name = "Load activity",
      .location =
          Location{40.0 + static_cast<double>(activity_index) * 0.001,
                   -74.0},
      .time_zone_name = "America/New_York",
      .inbound_travel_mode = TravelMode::kWalking,
      .activity_class = ActivityClass::kFlexible,
      .activity_state = ActivityState::kPlanned,
      .priority_rank = static_cast<std::int32_t>(activity_index),
      .utility_score = 10,
      .timing =
          {.open_windows = {{UnixTimeMilliseconds{0},
                             UnixTimeMilliseconds{3600000}}},
           .reservation_start = std::nullopt,
           .reservation_grace_seconds = 0,
           .min_duration_seconds = 60,
           .preferred_duration_seconds = 60,
           .max_duration_seconds = 60,
           .mandatory = false,
           .can_shorten = false,
           .can_move = true,
           .can_skip = true,
           .mandatory_deadline = std::nullopt},
      .activity_delay_seconds = 0,
      .found_closed_at = std::nullopt,
  };
}

[[nodiscard]] TripState load_trip(std::size_t trip_index,
                                  std::size_t suffix_size) {
  std::vector<Activity> activities;
  std::vector<CurrentPlanSegment> segments;
  activities.reserve(suffix_size);
  segments.reserve(suffix_size);
  for (std::size_t index = 0; index < suffix_size; ++index) {
    activities.push_back(load_activity(trip_index, index));
    segments.push_back(
        {.activity_id = activities.back().activity_id,
         .state = PlanEntryState::kOmitted,
         .scheduled_start = std::nullopt,
         .scheduled_end = std::nullopt});
  }
  return {
      .trip_id = id<TripId>(trip_index + 1),
      .default_time_zone_name = "America/New_York",
      .activities = std::move(activities),
      .completed_prefix_count = 0,
      .current_activity_id = std::nullopt,
      .current_plan =
          {.plan_id = id<PlanId>(1000000 + trip_index),
           .plan_revision = 1,
           .origin = PlanOrigin::kUserAuthored,
           .segments = std::move(segments),
           .created_at = UnixTimeMilliseconds{0},
           .source_proposal_id = std::nullopt},
      .travel_delays = {},
      .current_observation = {},
      .active_proposal = std::nullopt,
  };
}

class LoadProvider final : public TravelTimeProvider {
 public:
  explicit LoadProvider(bool inject_timeout)
      : inject_timeout_(inject_timeout) {}

  TravelTimeLookupResult get_matrix(
      std::span<const Location> locations, TravelMode,
      std::chrono::system_clock::time_point, Deadline deadline,
      std::stop_token stop_token) override {
    if (stop_token.stop_requested()) {
      return TravelTimeLookupResult{TravelTimeProviderError::kCancelled};
    }
    if (inject_timeout_ ||
        std::chrono::steady_clock::now() >= deadline) {
      return TravelTimeLookupResult{
          TravelTimeProviderError::kDeadlineExceeded};
    }
    std::vector<RouteEstimate> estimates(
        locations.size() * locations.size(),
        {.duration = std::chrono::seconds{1},
         .distance_meters = 1,
         .reachable = true});
    return TravelTimeLookupResult{
        TravelTimeMatrix{locations.size(), std::move(estimates)}};
  }

 private:
  bool inject_timeout_;
};

[[nodiscard]] RuntimePlanningContext planning_context(
    const WorkloadConfiguration& configuration,
    std::size_t event_index) {
  return {
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{3600000},
      .proposal_id = id<ProposalId>(2000000 + event_index),
      .proposal_created_at =
          UnixTimeMilliseconds{static_cast<std::int64_t>(event_index + 1)},
      .deadline = std::chrono::steady_clock::now() +
                  configuration.deadline,
      .max_candidates = 4096,
      .beam_width = 32,
      .max_expansions = 4096,
      .recovery_state = RecoveryState::kCurrent,
  };
}

[[nodiscard]] RuntimeEventRequest event_request(
    const WorkloadConfiguration& configuration,
    const std::vector<TripState>& trips, const WorkloadEvent& event) {
  const auto& trip = trips[event.trip_index];
  TripEventPayload payload;
  switch (event.kind) {
    case WorkloadEventKind::kLocation:
      payload = LocationUpdated{Location{40.0, -74.0}};
      break;
    case WorkloadEventKind::kRouteDeviation:
      payload = RouteDeviationDetected{Location{40.0, -74.0}, 25};
      break;
    case WorkloadEventKind::kReservationChanged:
      payload = ReservationChanged{
          trip.activities.front().activity_id,
          UnixTimeMilliseconds{
              600000 + static_cast<std::int64_t>(
                           (event.event_index % 120) * 1000)},
          30};
      break;
    case WorkloadEventKind::kOperatingHoursChanged:
      payload = OperatingHoursChanged{
          trip.activities.front().activity_id,
          {{UnixTimeMilliseconds{0}, UnixTimeMilliseconds{3600000}}}};
      break;
    case WorkloadEventKind::kPlaceFoundClosed:
      payload = PlaceFoundClosed{
          trip.activities.front().activity_id,
          UnixTimeMilliseconds{
              static_cast<std::int64_t>(event.event_index + 1)}};
      break;
  }
  return {
      .trip_id = trip.trip_id,
      .admission =
          {.runtime_epoch = 1,
           .mutation_sequence = event.mutation_sequence,
           .observation_sequence = event.observation_sequence,
           .expected_trip_revision = event.expected_trip_revision,
           .expected_planner_state_version = std::nullopt,
           .event =
               {.event_id = id<EventId>(3000000 + event.event_index),
                .occurred_at = UnixTimeMilliseconds{
                    static_cast<std::int64_t>(event.event_index + 1)},
                .command_expires_at = std::nullopt,
                .payload = std::move(payload)}},
      .planning = planning_context(configuration, event.event_index),
  };
}

[[nodiscard]] std::optional<std::size_t> parse_size(
    std::string_view value) {
  std::size_t result = 0;
  const auto parsed =
      std::from_chars(value.data(), value.data() + value.size(), result);
  if (parsed.ec != std::errc{} ||
      parsed.ptr != value.data() + value.size()) {
    return std::nullopt;
  }
  return result;
}

[[nodiscard]] bool parse_arguments(int argc, char** argv,
                                   WorkloadConfiguration* configuration,
                                   bool* self_check) {
  for (int index = 1; index < argc; ++index) {
    const std::string_view argument{argv[index]};
    if (argument == "--self-check") {
      *self_check = true;
      continue;
    }
    if (argument == "--help") return false;
    if (index + 1 >= argc) return false;
    const std::string_view value{argv[++index]};
    if (argument == "--profile") {
      const auto parsed = parse_workload_profile(value);
      if (!parsed) return false;
      configuration->profile = *parsed;
      continue;
    }
    const auto parsed = parse_size(value);
    if (!parsed) return false;
    if (argument == "--seed") configuration->seed = *parsed;
    else if (argument == "--trips") configuration->active_trips = *parsed;
    else if (argument == "--events") configuration->event_count = *parsed;
    else if (argument == "--events-per-second") {
      configuration->events_per_second = *parsed;
    } else if (argument == "--burst-size") {
      configuration->location_burst_size = *parsed;
    } else if (argument == "--suffix-size") {
      configuration->suffix_size = *parsed;
    } else if (argument == "--reservation-percent") {
      if (*parsed > std::numeric_limits<std::uint32_t>::max()) return false;
      configuration->reservation_percent =
          static_cast<std::uint32_t>(*parsed);
    } else if (argument == "--deadline-ms") {
      configuration->deadline = std::chrono::milliseconds{*parsed};
    } else if (argument == "--shards") {
      configuration->shard_count = *parsed;
    } else if (argument == "--workers") {
      configuration->worker_count = *parsed;
    } else {
      return false;
    }
  }
  if (*self_check) {
    configuration->profile = WorkloadProfile::kManyTrips;
    configuration->seed = 1;
    configuration->active_trips = 4;
    configuration->event_count = 32;
    configuration->events_per_second = 100000;
    configuration->location_burst_size = 4;
    configuration->suffix_size = 4;
    configuration->deadline = 100ms;
    configuration->shard_count = 2;
    configuration->worker_count = 2;
  }
  return configuration->is_valid();
}

void usage() {
  std::cerr
      << "usage: liveroute_loadgen [--profile NAME] [--seed N]"
         " [--trips N] [--events N] [--events-per-second N]"
         " [--burst-size N] [--suffix-size N]"
         " [--reservation-percent N] [--deadline-ms N]"
         " [--shards N] [--workers N] [--self-check]\n";
}

[[nodiscard]] bool queues_empty(const RuntimeQueueDepths& depths) {
  return depths.critical == 0 && depths.high == 0 &&
         depths.normal == 0 && depths.advisory == 0 &&
         depths.completions == 0 && depths.provider_jobs == 0 &&
         depths.planner_jobs == 0;
}

}  // namespace

int main(int argc, char** argv) {
  WorkloadConfiguration configuration;
  bool self_check = false;
  if (!parse_arguments(argc, argv, &configuration, &self_check)) {
    usage();
    return 2;
  }

  const auto workload = generate_workload(configuration);
  std::vector<TripState> trips;
  trips.reserve(configuration.active_trips);
  for (std::size_t index = 0; index < configuration.active_trips; ++index) {
    trips.push_back(load_trip(index, configuration.suffix_size));
  }

  LoadProvider provider(
      configuration.profile == WorkloadProfile::kProviderTimeout);
  std::mutex mutex;
  std::condition_variable condition;
  std::size_t bootstrap_count = 0;
  std::size_t prime_count = 0;
  std::size_t acknowledgement_count = 0;
  std::array<std::uint64_t, 6> acknowledgement_statuses{};
  std::vector<std::uint64_t> acknowledgement_latencies;
  std::vector<std::uint64_t> planning_latencies;
  std::vector<std::uint64_t> event_queue_wait_latencies;
  std::vector<std::uint64_t> event_application_latencies;
  std::vector<std::uint64_t> event_serialization_latencies;
  std::vector<std::uint64_t> event_total_latencies;
  std::vector<std::uint64_t> planning_queue_wait_latencies;
  std::vector<std::uint64_t> provider_latencies;
  std::vector<std::uint64_t> matrix_conversion_latencies;
  std::vector<std::uint64_t> planner_latencies;
  std::vector<std::uint64_t> planning_serialization_latencies;
  std::vector<std::uint64_t> planning_total_latencies;
  acknowledgement_latencies.reserve(configuration.event_count);
  planning_latencies.reserve(configuration.event_count);
  event_queue_wait_latencies.reserve(configuration.event_count);
  event_application_latencies.reserve(configuration.event_count);
  event_serialization_latencies.reserve(configuration.event_count);
  event_total_latencies.reserve(configuration.event_count);
  planning_queue_wait_latencies.reserve(configuration.event_count);
  provider_latencies.reserve(configuration.event_count);
  matrix_conversion_latencies.reserve(configuration.event_count);
  planner_latencies.reserve(configuration.event_count);
  planning_serialization_latencies.reserve(configuration.event_count);
  planning_total_latencies.reserve(configuration.event_count);

  const auto capacity =
      std::max({std::size_t{128}, configuration.active_trips,
                std::min<std::size_t>(
                    configuration.event_count, 65536)});
  ConcurrentTripRuntime runtime(
      {.shard_count = configuration.shard_count,
       .max_active_trips = configuration.active_trips,
       .shard_queue_capacities =
           {capacity, capacity, capacity, capacity},
       .completion_queue_capacity = capacity,
       .priority_fairness_burst = 8,
       .provider_workers = configuration.worker_count,
       .provider_queue_capacity = capacity,
       .planner_workers = configuration.worker_count,
       .planner_queue_capacity = capacity,
       .essential_response_capacity = capacity,
       .max_advisory_payload_bytes = 4096},
      provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        planning_latencies.push_back(
            delivery.timings.total_microseconds);
        planning_queue_wait_latencies.push_back(
            delivery.timings.queue_wait_microseconds);
        provider_latencies.push_back(delivery.timings.provider_microseconds);
        matrix_conversion_latencies.push_back(
            delivery.timings.matrix_conversion_microseconds);
        planner_latencies.push_back(delivery.timings.planner_microseconds);
        planning_serialization_latencies.push_back(
            delivery.timings.response_assembly_microseconds);
        planning_total_latencies.push_back(
            delivery.timings.total_microseconds);
        condition.notify_all();
        return true;
      });

  for (const auto& trip : trips) {
    if (runtime.try_bootstrap(
            {.state = trip,
             .owner_user_id = "load-user",
             .runtime_epoch = 1,
             .trip_revision = 1,
             .finalized_mutation_sequence = 1,
             .current_observation_sequence = 0,
             .stream_binding = 1},
            [&](RuntimeBootstrapResult result) {
              std::scoped_lock lock(mutex);
              if (result.status == RuntimeBootstrapStatus::kAccepted) {
                ++bootstrap_count;
              }
              condition.notify_all();
            }) != RuntimeSubmissionStatus::kAccepted) {
      return 3;
    }
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, 10s, [&] {
          return bootstrap_count == configuration.active_trips;
        })) {
      return 3;
    }
  }

  for (std::size_t index = 0; index < trips.size(); ++index) {
    RuntimeEventRequest prime{
        .trip_id = trips[index].trip_id,
        .admission =
            {.runtime_epoch = 1,
             .mutation_sequence = 0,
             .observation_sequence = 1,
             .expected_trip_revision = 0,
             .expected_planner_state_version = std::nullopt,
             .event =
                 {.event_id = id<EventId>(4000000 + index),
                  .occurred_at = UnixTimeMilliseconds{0},
                  .command_expires_at = std::nullopt,
                  .payload = LocationUpdated{Location{40.0, -74.0}}}},
        .planning = std::nullopt,
    };
    if (runtime.try_apply_event(
            std::move(prime),
            [&](RuntimeEventAcknowledgement acknowledgement) {
              std::scoped_lock lock(mutex);
              if (acknowledgement.admission.status ==
                  EventCoordinatorStatus::kAccepted) {
                ++prime_count;
              }
              condition.notify_all();
            }) != RuntimeSubmissionStatus::kAccepted) {
      return 3;
    }
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, 10s, [&] {
          return prime_count == configuration.active_trips;
        })) {
      return 3;
    }
  }

  const auto observation_before = runtime.observation_metrics();
  const auto execution_before = runtime.execution_metrics();
  std::array<std::uint64_t, 4> dropped_by_priority{};
  std::size_t accepted_submissions = 0;
  const auto run_start = std::chrono::steady_clock::now();
  for (const auto& event : workload) {
    std::this_thread::sleep_until(run_start + event.scheduled_offset);
    const auto submitted_at = std::chrono::steady_clock::now();
    auto request = event_request(configuration, trips, event);
    const auto priority =
        request.admission.event.priority_for({});
    const auto status = runtime.try_apply_event(
        std::move(request),
        [&, submitted_at](
            RuntimeEventAcknowledgement acknowledgement) {
          const auto elapsed = std::chrono::duration_cast<
              std::chrono::microseconds>(
              std::chrono::steady_clock::now() - submitted_at);
          std::scoped_lock lock(mutex);
          acknowledgement_latencies.push_back(
              static_cast<std::uint64_t>(elapsed.count()));
          event_queue_wait_latencies.push_back(
              acknowledgement.timings.queue_wait_microseconds);
          event_application_latencies.push_back(
              acknowledgement.timings.event_application_microseconds);
          event_serialization_latencies.push_back(
              acknowledgement.timings.response_assembly_microseconds);
          event_total_latencies.push_back(
              acknowledgement.timings.total_microseconds);
          ++acknowledgement_statuses[static_cast<std::size_t>(
              acknowledgement.admission.status)];
          ++acknowledgement_count;
          condition.notify_all();
        });
    if (status == RuntimeSubmissionStatus::kAccepted) {
      ++accepted_submissions;
    } else {
      ++dropped_by_priority[static_cast<std::size_t>(priority)];
    }
  }

  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, 15s, [&] {
          return acknowledgement_count == accepted_submissions;
        })) {
      return 4;
    }
  }

  auto stable_since = std::chrono::steady_clock::now();
  auto prior_execution = runtime.execution_metrics();
  const auto drain_deadline =
      std::chrono::steady_clock::now() + 15s;
  while (std::chrono::steady_clock::now() < drain_deadline) {
    std::this_thread::sleep_for(5ms);
    const auto execution = runtime.execution_metrics();
    if (execution == prior_execution &&
        execution.planning_attempts_started ==
            execution.planning_attempts_completed &&
        queues_empty(runtime.queue_depths())) {
      if (std::chrono::steady_clock::now() - stable_since >= 25ms) break;
    } else {
      stable_since = std::chrono::steady_clock::now();
      prior_execution = execution;
    }
  }
  const auto run_end = std::chrono::steady_clock::now();
  const auto execution = runtime.execution_metrics();
  if (execution.planning_attempts_started !=
          execution.planning_attempts_completed ||
      !queues_empty(runtime.queue_depths())) {
    return 4;
  }

  const auto observation = runtime.observation_metrics();
  const auto elapsed_seconds =
      std::chrono::duration<double>(run_end - run_start).count();
  const auto measured_received =
      observation.received_location_events -
      observation_before.received_location_events;
  const auto measured_coalesced =
      observation.coalesced_location_replans -
      observation_before.coalesced_location_replans;
  const auto attempts =
      execution.planning_attempts_started -
      execution_before.planning_attempts_started;
  const auto completed =
      execution.planning_attempts_completed -
      execution_before.planning_attempts_completed;

  std::cout
      << "mode=runtime"
      << " profile=" << workload_profile_name(configuration.profile)
      << " seed=" << configuration.seed
      << " trips=" << configuration.active_trips
      << " events=" << configuration.event_count
      << " target_events_per_second="
      << configuration.events_per_second
      << " burst_size=" << configuration.location_burst_size
      << " suffix_size=" << configuration.suffix_size
      << " reservation_percent=" << configuration.reservation_percent
      << " deadline_ms=" << configuration.deadline.count()
      << " shards=" << configuration.shard_count
      << " workers=" << configuration.worker_count
      << " submitted=" << accepted_submissions
      << " acknowledged=" << acknowledgement_count
      << " accepted_acks=" << acknowledgement_statuses[0]
      << " duplicate_acks=" << acknowledgement_statuses[1]
      << " stale_acks=" << acknowledgement_statuses[2]
      << " invalid_acks=" << acknowledgement_statuses[3]
      << " inactive_acks=" << acknowledgement_statuses[4]
      << " internal_acks=" << acknowledgement_statuses[5]
      << " throughput_events_per_second="
      << (elapsed_seconds == 0.0
              ? 0.0
              : acknowledgement_count / elapsed_seconds)
      << " elapsed_ms="
      << std::chrono::duration_cast<std::chrono::milliseconds>(
             run_end - run_start)
             .count()
      << " ack_p50_us=" << percentile(acknowledgement_latencies, 50)
      << " ack_p95_us=" << percentile(acknowledgement_latencies, 95)
      << " ack_p99_us=" << percentile(acknowledgement_latencies, 99)
      << " event_queue_wait_p50_us="
      << percentile(event_queue_wait_latencies, 50)
      << " event_queue_wait_p95_us="
      << percentile(event_queue_wait_latencies, 95)
      << " event_queue_wait_p99_us="
      << percentile(event_queue_wait_latencies, 99)
      << " event_application_p50_us="
      << percentile(event_application_latencies, 50)
      << " event_application_p95_us="
      << percentile(event_application_latencies, 95)
      << " event_application_p99_us="
      << percentile(event_application_latencies, 99)
      << " event_serialization_p50_us="
      << percentile(event_serialization_latencies, 50)
      << " event_serialization_p95_us="
      << percentile(event_serialization_latencies, 95)
      << " event_serialization_p99_us="
      << percentile(event_serialization_latencies, 99)
      << " event_total_p50_us=" << percentile(event_total_latencies, 50)
      << " event_total_p95_us=" << percentile(event_total_latencies, 95)
      << " event_total_p99_us=" << percentile(event_total_latencies, 99)
      << " plan_p50_us=" << percentile(planning_latencies, 50)
      << " plan_p95_us=" << percentile(planning_latencies, 95)
      << " plan_p99_us=" << percentile(planning_latencies, 99)
      << " planning_queue_wait_p50_us="
      << percentile(planning_queue_wait_latencies, 50)
      << " planning_queue_wait_p95_us="
      << percentile(planning_queue_wait_latencies, 95)
      << " planning_queue_wait_p99_us="
      << percentile(planning_queue_wait_latencies, 99)
      << " provider_p50_us=" << percentile(provider_latencies, 50)
      << " provider_p95_us=" << percentile(provider_latencies, 95)
      << " provider_p99_us=" << percentile(provider_latencies, 99)
      << " matrix_conversion_p50_us="
      << percentile(matrix_conversion_latencies, 50)
      << " matrix_conversion_p95_us="
      << percentile(matrix_conversion_latencies, 95)
      << " matrix_conversion_p99_us="
      << percentile(matrix_conversion_latencies, 99)
      << " planner_p50_us=" << percentile(planner_latencies, 50)
      << " planner_p95_us=" << percentile(planner_latencies, 95)
      << " planner_p99_us=" << percentile(planner_latencies, 99)
      << " planning_serialization_p50_us="
      << percentile(planning_serialization_latencies, 50)
      << " planning_serialization_p95_us="
      << percentile(planning_serialization_latencies, 95)
      << " planning_serialization_p99_us="
      << percentile(planning_serialization_latencies, 99)
      << " planning_total_p50_us="
      << percentile(planning_total_latencies, 50)
      << " planning_total_p95_us="
      << percentile(planning_total_latencies, 95)
      << " planning_total_p99_us="
      << percentile(planning_total_latencies, 99)
      << " attempts_started=" << attempts
      << " attempts_completed=" << completed
      << " deadline_misses="
      << execution.deadline_misses - execution_before.deadline_misses
      << " cancellations="
      << execution.cancelled_attempts -
             execution_before.cancelled_attempts
      << " supersessions="
      << execution.supersession_requests -
             execution_before.supersession_requests
      << " provider_failures="
      << execution.provider_failures -
             execution_before.provider_failures
      << " coalescing_rate="
      << (measured_received == 0
              ? 0.0
              : static_cast<double>(measured_coalesced) /
                    static_cast<double>(measured_received))
      << " replans_avoided="
      << observation.replans_avoided -
             observation_before.replans_avoided
      << " drops_critical=" << dropped_by_priority[0]
      << " drops_high=" << dropped_by_priority[1]
      << " drops_normal=" << dropped_by_priority[2]
      << " drops_advisory=" << dropped_by_priority[3] << '\n';

  if (self_check &&
      (acknowledgement_count != configuration.event_count ||
       acknowledgement_statuses[0] != configuration.event_count ||
       attempts == 0 || attempts != completed)) {
    return 5;
  }
  return 0;
}
