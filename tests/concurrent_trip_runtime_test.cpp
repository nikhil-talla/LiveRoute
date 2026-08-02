#include "liveroute/runtime/concurrent_trip_runtime.hpp"

#include <algorithm>
#include <array>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <iostream>
#include <mutex>
#include <optional>
#include <vector>

namespace {

using namespace liveroute::domain;
using namespace liveroute::routing;
using namespace liveroute::runtime;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

Activity activity(std::uint8_t marker) {
  return {.activity_id = id<ActivityId>(marker),
          .place_id = PlaceId{"place"},
          .display_name = "Place",
          .location = Location{40.0, -74.0},
          .time_zone_name = "America/New_York",
          .inbound_travel_mode = TravelMode::kWalking,
          .activity_class = ActivityClass::kFlexible,
          .activity_state = ActivityState::kPlanned,
          .priority_rank = 0,
          .utility_score = 10,
          .timing = {.open_windows = {{UnixTimeMilliseconds{0},
                                       UnixTimeMilliseconds{10000}}},
                     .reservation_start = std::nullopt,
                     .reservation_grace_seconds = 0,
                     .min_duration_seconds = 1,
                     .preferred_duration_seconds = 1,
                     .max_duration_seconds = 1,
                     .mandatory = false,
                     .can_shorten = false,
                     .can_move = true,
                     .can_skip = true,
                     .mandatory_deadline = std::nullopt},
          .activity_delay_seconds = 0,
          .found_closed_at = std::nullopt};
}

TripState trip(std::uint8_t marker) {
  const auto place = activity(static_cast<std::uint8_t>(marker + 1));
  return {.trip_id = id<TripId>(marker),
          .default_time_zone_name = "America/New_York",
          .activities = {place},
          .completed_prefix_count = 0,
          .current_activity_id = std::nullopt,
          .current_plan = {.plan_id =
                               id<PlanId>(static_cast<std::uint8_t>(marker + 2)),
                           .plan_revision = 1,
                           .origin = PlanOrigin::kUserAuthored,
                           .segments = {{.activity_id = place.activity_id,
                                         .state = PlanEntryState::kOmitted,
                                         .scheduled_start = std::nullopt,
                                         .scheduled_end = std::nullopt}},
                           .created_at = UnixTimeMilliseconds{0},
                           .source_proposal_id = std::nullopt},
          .travel_delays = {},
          .current_observation = {},
          .active_proposal = std::nullopt};
}

TripState mixed_mode_trip(std::uint8_t marker) {
  auto walking = activity(static_cast<std::uint8_t>(marker + 1));
  auto driving = activity(static_cast<std::uint8_t>(marker + 2));
  driving.inbound_travel_mode = TravelMode::kDriving;
  return {.trip_id = id<TripId>(marker),
          .default_time_zone_name = "America/New_York",
          .activities = {walking, driving},
          .completed_prefix_count = 0,
          .current_activity_id = std::nullopt,
          .current_plan =
              {.plan_id = id<PlanId>(
                   static_cast<std::uint8_t>(marker + 3)),
               .plan_revision = 1,
               .origin = PlanOrigin::kUserAuthored,
               .segments =
                   {{.activity_id = walking.activity_id,
                     .state = PlanEntryState::kOmitted,
                     .scheduled_start = std::nullopt,
                     .scheduled_end = std::nullopt},
                    {.activity_id = driving.activity_id,
                     .state = PlanEntryState::kOmitted,
                     .scheduled_start = std::nullopt,
                     .scheduled_end = std::nullopt}},
               .created_at = UnixTimeMilliseconds{0},
               .source_proposal_id = std::nullopt},
          .travel_delays = {},
          .current_observation = {},
          .active_proposal = std::nullopt};
}

RuntimePlanningContext planning(std::uint8_t marker) {
  return {.current_time = UnixTimeMilliseconds{0},
          .planning_horizon_start = UnixTimeMilliseconds{0},
          .planning_horizon_end = UnixTimeMilliseconds{10000},
          .proposal_id = id<ProposalId>(marker),
          .proposal_created_at = UnixTimeMilliseconds{10},
          .deadline =
              std::chrono::steady_clock::now() + std::chrono::seconds{5},
          .max_candidates = 100,
          .beam_width = 8,
          .max_expansions = 100,
          .recovery_state = RecoveryState::kCurrent};
}

RuntimeEventRequest location_request(const TripState& state,
                                     std::uint64_t observation_sequence,
                                     std::uint8_t marker) {
  return {
      .trip_id = state.trip_id,
      .admission =
          {.runtime_epoch = 7,
           .mutation_sequence = 0,
           .observation_sequence = observation_sequence,
           .expected_trip_revision = 0,
           .expected_planner_state_version = std::nullopt,
           .event =
               {.event_id = id<EventId>(marker),
                .occurred_at = UnixTimeMilliseconds{0},
                .command_expires_at = std::nullopt,
                .payload = LocationUpdated{Location{40.0, -74.0}}}},
      .planning = planning(static_cast<std::uint8_t>(marker + 1)),
  };
}

RuntimeEventRequest route_deviation_request(
    const TripState& state, std::uint64_t observation_sequence,
    std::uint8_t marker) {
  auto request =
      location_request(state, observation_sequence, marker);
  request.admission.event.payload =
      RouteDeviationDetected{Location{40.0, -74.0}, 25};
  return request;
}

RuntimeEventRequest recommendation_refresh_request(
    const TripState& state, std::uint64_t observation_sequence,
    std::uint8_t marker) {
  auto request =
      location_request(state, observation_sequence, marker);
  request.admission.event.payload =
      AdvisoryUpdate{AdvisoryKind::kRecommendationRefresh,
                     "runtime-test", {}};
  return request;
}

RuntimeEventRequest completion_request(
    const TripState& state, std::uint64_t mutation_sequence,
    std::uint64_t expected_trip_revision, std::uint8_t marker) {
  return {
      .trip_id = state.trip_id,
      .admission =
          {.runtime_epoch = 7,
           .mutation_sequence = mutation_sequence,
           .observation_sequence = 0,
           .expected_trip_revision = expected_trip_revision,
           .expected_planner_state_version = std::nullopt,
           .event =
               {.event_id = id<EventId>(marker),
                .occurred_at = UnixTimeMilliseconds{0},
                .command_expires_at = std::nullopt,
                .payload =
                    ActivityStatusChanged{
                        state.activities.front().activity_id,
                        ActivityState::kCompleted}}},
      .planning = planning(static_cast<std::uint8_t>(marker + 1)),
  };
}

RuntimeEventRequest reservation_request(
    const TripState& state, std::uint64_t mutation_sequence,
    std::uint64_t expected_trip_revision, std::uint8_t marker) {
  return {
      .trip_id = state.trip_id,
      .admission =
          {.runtime_epoch = 7,
           .mutation_sequence = mutation_sequence,
           .observation_sequence = 0,
           .expected_trip_revision = expected_trip_revision,
           .expected_planner_state_version = std::nullopt,
           .event =
               {.event_id = id<EventId>(marker),
                .occurred_at = UnixTimeMilliseconds{0},
                .command_expires_at = std::nullopt,
                .payload =
                    ReservationChanged{
                        state.activities.front().activity_id,
                        UnixTimeMilliseconds{500}, 30}}},
      .planning = planning(static_cast<std::uint8_t>(marker + 1)),
  };
}

class BlockingProvider final : public TravelTimeProvider {
 public:
  explicit BlockingProvider(TravelTimeMatrix matrix)
      : matrix_(std::move(matrix)) {}

  TravelTimeLookupResult get_matrix(
      std::span<const Location>, TravelMode,
      std::chrono::system_clock::time_point, Deadline,
      std::stop_token stop_token) override {
    std::unique_lock lock(mutex_);
    ++calls_;
    started_ = true;
    condition_.notify_all();
    if (calls_ == blocked_call_) {
      condition_.wait(lock, [&] {
        return release_first_ || stop_token.stop_requested();
      });
    }
    if (stop_token.stop_requested()) {
      return TravelTimeLookupResult{TravelTimeProviderError::kCancelled};
    }
    return TravelTimeLookupResult{matrix_};
  }

  bool wait_until_started() {
    std::unique_lock lock(mutex_);
    return condition_.wait_for(lock, std::chrono::seconds{2},
                               [&] { return started_; });
  }

  void release_first() {
    std::scoped_lock lock(mutex_);
    release_first_ = true;
    condition_.notify_all();
  }

  void block_next() {
    std::scoped_lock lock(mutex_);
    blocked_call_ = calls_ + 1;
    started_ = false;
    release_first_ = false;
  }

  [[nodiscard]] std::size_t calls() const {
    std::scoped_lock lock(mutex_);
    return calls_;
  }

 private:
  TravelTimeMatrix matrix_;
  mutable std::mutex mutex_;
  std::condition_variable condition_;
  std::size_t calls_{};
  std::size_t blocked_call_{1};
  bool started_{};
  bool release_first_{};
};

class ModeProvider final : public TravelTimeProvider {
 public:
  TravelTimeLookupResult get_matrix(
      std::span<const Location> locations, TravelMode mode,
      std::chrono::system_clock::time_point, Deadline,
      std::stop_token) override {
    const auto seconds =
        mode == TravelMode::kWalking ? std::chrono::seconds{1}
                                     : std::chrono::seconds{2};
    std::vector<RouteEstimate> estimates(
        locations.size() * locations.size(),
        {.duration = seconds, .distance_meters = 1, .reachable = true});
    return TravelTimeLookupResult{
        TravelTimeMatrix{locations.size(), std::move(estimates)}};
  }
};

}  // namespace

int main() {
  const TravelTimeMatrix matrix{
      2,
      {{std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true}}};
  FixedTravelTimeProvider provider(2, matrix);
  std::mutex mutex;
  std::condition_variable condition;
  std::vector<RuntimePlanningDelivery> deliveries;
  std::vector<RuntimeEventAcknowledgement> acknowledgements;
  std::vector<RuntimeBootstrapResult> bootstraps;

  ConcurrentTripRuntime runtime(
      {.shard_count = 2,
       .max_active_trips = 2,
       .shard_queue_capacities = {8, 8, 8, 8},
       .completion_queue_capacity = 4,
       .priority_fairness_burst = 4,
       .provider_workers = 2,
       .provider_queue_capacity = 4,
       .planner_workers = 2,
       .planner_queue_capacity = 4,
       .essential_response_capacity = 4,
       .max_advisory_payload_bytes = 1024},
      provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        deliveries.push_back(std::move(delivery));
        condition.notify_all();
        return true;
      });
  if (runtime.queue_depths() != RuntimeQueueDepths{}) return 1;
  if (runtime.observation_metrics() != RuntimeObservationMetrics{}) return 1;
  if (runtime.execution_metrics() != RuntimeExecutionMetrics{}) return 1;

  const auto first = trip(10);
  const auto second = trip(30);
  for (const auto& state : {first, second}) {
    if (runtime.try_bootstrap(
            {.state = state,
             .owner_user_id = {},
             .runtime_epoch = 7,
             .trip_revision = 1,
             .finalized_mutation_sequence = 1,
             .current_observation_sequence = 0,
             .stream_binding = 99},
            [&](RuntimeBootstrapResult result) {
              std::scoped_lock lock(mutex);
              bootstraps.push_back(result);
              condition.notify_all();
            }) != RuntimeSubmissionStatus::kAccepted) {
      return 1;
    }
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return bootstraps.size() == 2;
        })) {
      return 1;
    }
    if (bootstraps[0].status != RuntimeBootstrapStatus::kAccepted ||
        bootstraps[1].status != RuntimeBootstrapStatus::kAccepted ||
        runtime.active_trip_count() != 2) {
      return 1;
    }
  }
  const auto third = trip(90);
  if (runtime.try_bootstrap(
          {.state = third,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 99},
          [&](RuntimeBootstrapResult result) {
            std::scoped_lock lock(mutex);
            bootstraps.push_back(result);
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return bootstraps.size() == 3;
        }) ||
        bootstraps.back().status !=
            RuntimeBootstrapStatus::kResourceExhausted ||
        runtime.active_trip_count() != 2) {
      return 1;
    }
  }

  auto submit = [&](RuntimeEventRequest request) {
    return runtime.try_apply_event(
        std::move(request), [&](RuntimeEventAcknowledgement acknowledgement) {
          std::scoped_lock lock(mutex);
          acknowledgements.push_back(std::move(acknowledgement));
          condition.notify_all();
        });
  };
  if (submit(route_deviation_request(first, 1, 50)) !=
          RuntimeSubmissionStatus::kAccepted ||
      submit(route_deviation_request(second, 1, 60)) !=
          RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3}, [&] {
          return acknowledgements.size() == 2 && deliveries.size() == 2;
        })) {
      return 1;
    }
    for (const auto& acknowledgement : acknowledgements) {
      if (acknowledgement.admission.status !=
              EventCoordinatorStatus::kAccepted ||
          acknowledgement.admission.version_snapshot
                  .accepted_observation_sequence != 1) {
        return 1;
      }
    }
    for (const auto& delivery : deliveries) {
      if (delivery.status != RuntimePlanningStatus::kOk ||
          delivery.stream_binding != std::optional<std::uint64_t>{99} ||
          !delivery.proposal.has_value() ||
          delivery.proposal->quality.plan_quality != PlanQuality::kComplete ||
          delivery.proposal->proposal.source_planner_state_version !=
              PlannerStateVersion{1}) {
        std::cerr << "delivery status=" << static_cast<int>(delivery.status)
                  << " binding=" << delivery.stream_binding.value_or(0)
                  << " proposal=" << delivery.proposal.has_value();
        if (delivery.proposal.has_value()) {
          std::cerr << " quality="
                    << static_cast<int>(
                           delivery.proposal->quality.plan_quality)
                    << " version="
                    << delivery.proposal->proposal
                           .source_planner_state_version.value();
        }
        std::cerr << '\n';
        return 1;
      }
    }
  }

  if (submit(location_request(first, 1, 70)) !=
      RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return acknowledgements.size() == 3;
        }) ||
        acknowledgements.back().admission.status !=
            EventCoordinatorStatus::kStale) {
      return 1;
    }
  }

  runtime.stop_accepting();
  if (runtime.is_accepting() ||
      submit(location_request(first, 2, 80)) !=
          RuntimeSubmissionStatus::kStopping) {
    return 1;
  }

  BlockingProvider blocking_provider(matrix);
  std::vector<RuntimePlanningDelivery> coalesced_deliveries;
  std::size_t coalesced_acknowledgements = 0;
  ConcurrentTripRuntime coalescing_runtime(
      {.shard_count = 1,
       .max_active_trips = 1,
       .shard_queue_capacities = {8, 8, 8, 8},
       .completion_queue_capacity = 2,
       .priority_fairness_burst = 4,
       .provider_workers = 1,
       .provider_queue_capacity = 2,
       .planner_workers = 1,
       .planner_queue_capacity = 2,
       .essential_response_capacity = 4,
       .max_advisory_payload_bytes = 1024},
      blocking_provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        coalesced_deliveries.push_back(std::move(delivery));
        condition.notify_all();
        return true;
      });
  bool coalescing_bootstrapped = false;
  if (coalescing_runtime.try_bootstrap(
          {.state = first,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 100},
          [&](RuntimeBootstrapResult result) {
            std::scoped_lock lock(mutex);
            coalescing_bootstrapped =
                result.status == RuntimeBootstrapStatus::kAccepted;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2},
                            [&] { return coalescing_bootstrapped; })) {
      return 1;
    }
  }
  const auto coalescing_callback =
      [&](RuntimeEventAcknowledgement acknowledgement) {
        std::scoped_lock lock(mutex);
        if (acknowledgement.admission.status ==
            EventCoordinatorStatus::kAccepted) {
          ++coalesced_acknowledgements;
        }
        condition.notify_all();
      };
  if (coalescing_runtime.try_apply_event(
          route_deviation_request(first, 1, 100),
          coalescing_callback) !=
          RuntimeSubmissionStatus::kAccepted ||
      !blocking_provider.wait_until_started() ||
      coalescing_runtime.try_apply_event(
          location_request(first, 2, 110), coalescing_callback) !=
          RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return coalesced_acknowledgements == 2;
        })) {
      return 1;
    }
  }
  blocking_provider.release_first();
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3}, [&] {
          return coalesced_acknowledgements == 2 &&
                 coalesced_deliveries.size() == 1;
        }) ||
        coalesced_deliveries.front().status != RuntimePlanningStatus::kOk ||
        !coalesced_deliveries.front().proposal.has_value() ||
        coalesced_deliveries.front()
                .proposal->proposal.source_planner_state_version !=
            PlannerStateVersion{2} ||
        blocking_provider.calls() != 2) {
      return 1;
    }
  }
  if (coalescing_runtime.execution_metrics() !=
      RuntimeExecutionMetrics{.planning_attempts_started = 2,
                              .planning_attempts_completed = 2,
                              .deadline_misses = 0,
                              .cancelled_attempts = 1,
                              .supersession_requests = 1,
                              .provider_failures = 0}) {
    return 1;
  }

  BlockingProvider concurrent_provider(matrix);
  std::vector<TripId> concurrent_deliveries;
  std::size_t concurrent_bootstraps = 0;
  ConcurrentTripRuntime concurrent_runtime(
      {.shard_count = 2,
       .max_active_trips = 2,
       .shard_queue_capacities = {4, 4, 4, 4},
       .completion_queue_capacity = 2,
       .priority_fairness_burst = 2,
       .provider_workers = 2,
       .provider_queue_capacity = 2,
       .planner_workers = 2,
       .planner_queue_capacity = 2,
       .essential_response_capacity = 4,
       .max_advisory_payload_bytes = 1024},
      concurrent_provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        concurrent_deliveries.push_back(delivery.trip_id);
        condition.notify_all();
        return true;
      });
  for (const auto& state : {first, second}) {
    if (concurrent_runtime.try_bootstrap(
            {.state = state,
             .owner_user_id = {},
             .runtime_epoch = 7,
             .trip_revision = 1,
             .finalized_mutation_sequence = 1,
             .current_observation_sequence = 0,
             .stream_binding = 102},
            [&](RuntimeBootstrapResult result) {
              std::scoped_lock lock(mutex);
              if (result.status == RuntimeBootstrapStatus::kAccepted) {
                ++concurrent_bootstraps;
              }
              condition.notify_all();
            }) != RuntimeSubmissionStatus::kAccepted) {
      return 1;
    }
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return concurrent_bootstraps == 2;
        })) {
      return 1;
    }
  }
  if (concurrent_runtime.try_apply_event(
          route_deviation_request(first, 1, 115),
          [](RuntimeEventAcknowledgement) {}) !=
          RuntimeSubmissionStatus::kAccepted ||
      !concurrent_provider.wait_until_started() ||
      concurrent_runtime.try_apply_event(
          route_deviation_request(second, 1, 116),
          [](RuntimeEventAcknowledgement) {}) !=
          RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return std::find(concurrent_deliveries.begin(),
                           concurrent_deliveries.end(),
                           second.trip_id) != concurrent_deliveries.end();
        })) {
      return 1;
    }
  }
  concurrent_provider.release_first();
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return concurrent_deliveries.size() == 2;
        })) {
      return 1;
    }
  }

  bool reserved_callback_started = false;
  bool release_reserved_callback = false;
  bool reserved_callback_finished = false;
  ConcurrentTripRuntime response_capacity_runtime(
      {.shard_count = 1,
       .max_active_trips = 2,
       .shard_queue_capacities = {2, 2, 2, 2},
       .completion_queue_capacity = 1,
       .priority_fairness_burst = 2,
       .provider_workers = 1,
       .provider_queue_capacity = 1,
       .planner_workers = 1,
       .planner_queue_capacity = 1,
       .essential_response_capacity = 1,
       .max_advisory_payload_bytes = 1024},
      provider, [](RuntimePlanningDelivery) { return true; });
  if (response_capacity_runtime.try_bootstrap(
          {.state = first,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = std::nullopt},
          [&](RuntimeBootstrapResult) {
            std::unique_lock lock(mutex);
            reserved_callback_started = true;
            condition.notify_all();
            condition.wait(lock, [&] { return release_reserved_callback; });
            reserved_callback_finished = true;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return reserved_callback_started;
        })) {
      return 1;
    }
  }
  if (response_capacity_runtime.try_bootstrap(
          {.state = second,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = std::nullopt},
          [](RuntimeBootstrapResult) {}) !=
      RuntimeSubmissionStatus::kResponseCapacityFull) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    release_reserved_callback = true;
    condition.notify_all();
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return reserved_callback_finished;
        })) {
      return 1;
    }
  }

  ModeProvider mode_provider;
  const auto mixed = mixed_mode_trip(120);
  std::optional<RuntimePlanningDelivery> mixed_delivery;
  bool mixed_bootstrapped = false;
  ConcurrentTripRuntime mixed_runtime(
      {.shard_count = 1,
       .max_active_trips = 1,
       .shard_queue_capacities = {4, 4, 4, 4},
       .completion_queue_capacity = 2,
       .priority_fairness_burst = 2,
       .provider_workers = 1,
       .provider_queue_capacity = 2,
       .planner_workers = 1,
       .planner_queue_capacity = 2,
       .essential_response_capacity = 2,
       .max_advisory_payload_bytes = 1024},
      mode_provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        mixed_delivery = std::move(delivery);
        condition.notify_all();
        return true;
      });
  if (mixed_runtime.try_bootstrap(
          {.state = mixed,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 101},
          [&](RuntimeBootstrapResult result) {
            std::scoped_lock lock(mutex);
            mixed_bootstrapped =
                result.status == RuntimeBootstrapStatus::kAccepted;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2},
                            [&] { return mixed_bootstrapped; })) {
      return 1;
    }
  }
  if (mixed_runtime.try_apply_event(
          route_deviation_request(mixed, 1, 125),
          [](RuntimeEventAcknowledgement) {}) !=
      RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3},
                            [&] { return mixed_delivery.has_value(); }) ||
        !mixed_delivery->proposal.has_value()) {
      return 1;
    }
    for (const auto& segment :
         mixed_delivery->proposal->proposal.revised_suffix) {
      if (!segment.inbound_route.has_value()) return 1;
      const auto expected =
          segment.activity_id == mixed.activities[0].activity_id
              ? std::chrono::seconds{1}
              : std::chrono::seconds{2};
      if (segment.inbound_route->duration != expected) return 1;
    }
  }

  BlockingProvider burst_provider(matrix);
  std::size_t burst_acknowledgements = 0;
  bool burst_replan_scheduled = false;
  std::uint64_t burst_planner_state_version = 0;
  std::vector<RuntimePlanningDelivery> burst_deliveries;
  bool burst_bootstrap_started = false;
  bool release_burst_bootstrap = false;
  ConcurrentTripRuntime burst_runtime(
      {.shard_count = 1,
       .max_active_trips = 1,
       .shard_queue_capacities = {2, 2, 8, 2},
       .completion_queue_capacity = 2,
       .priority_fairness_burst = 2,
       .provider_workers = 1,
       .provider_queue_capacity = 2,
       .planner_workers = 1,
       .planner_queue_capacity = 2,
       .essential_response_capacity = 8,
       .max_advisory_payload_bytes = 1024},
      burst_provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        burst_deliveries.push_back(std::move(delivery));
        condition.notify_all();
        return true;
      });
  if (burst_runtime.try_bootstrap(
          {.state = first,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 103},
          [&](RuntimeBootstrapResult result) {
            std::unique_lock lock(mutex);
            if (result.status != RuntimeBootstrapStatus::kAccepted) return;
            burst_bootstrap_started = true;
            condition.notify_all();
            condition.wait(lock,
                           [&] { return release_burst_bootstrap; });
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return burst_bootstrap_started;
        })) {
      return 1;
    }
  }
  for (std::uint64_t sequence = 1; sequence <= 5; ++sequence) {
    if (burst_runtime.try_apply_event(
            location_request(
                first, sequence,
                static_cast<std::uint8_t>(180 + sequence)),
            [&](RuntimeEventAcknowledgement acknowledgement) {
              std::scoped_lock lock(mutex);
              if (acknowledgement.admission.status ==
                  EventCoordinatorStatus::kAccepted) {
                ++burst_acknowledgements;
              }
              burst_replan_scheduled =
                  burst_replan_scheduled ||
                  acknowledgement.admission.planning_seed.has_value();
              burst_planner_state_version =
                  acknowledgement.admission.version_snapshot
                      .planner_state_version;
              condition.notify_all();
            }) != RuntimeSubmissionStatus::kAccepted) {
      return 1;
    }
  }
  if (burst_runtime.queue_depths().normal != 5) return 1;
  {
    std::scoped_lock lock(mutex);
    release_burst_bootstrap = true;
  }
  condition.notify_all();
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3}, [&] {
          return burst_acknowledgements == 5;
        }) ||
        burst_provider.calls() != 0 ||
        !burst_deliveries.empty() || burst_replan_scheduled ||
        burst_planner_state_version != 5) {
      return 1;
    }
  }
  const RuntimeObservationMetrics expected_burst_metrics{
      .received_location_events = 5,
      .coalesced_location_replans = 0,
      .dropped_stale_location_events = 0,
      .replans_avoided = 5,
  };
  if (burst_runtime.observation_metrics() !=
      expected_burst_metrics) {
    return 1;
  }
  bool stale_location_acknowledged = false;
  if (burst_runtime.try_apply_event(
          location_request(first, 4, 190),
          [&](RuntimeEventAcknowledgement acknowledgement) {
            std::scoped_lock lock(mutex);
            stale_location_acknowledged =
                acknowledgement.admission.status ==
                EventCoordinatorStatus::kStale;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return stale_location_acknowledged;
        })) {
      return 1;
    }
  }
  const auto stale_metrics = burst_runtime.observation_metrics();
  if (stale_metrics.received_location_events != 6 ||
      stale_metrics.coalesced_location_replans != 0 ||
      stale_metrics.dropped_stale_location_events != 1 ||
      stale_metrics.replans_avoided != 5) {
    return 1;
  }

  BlockingProvider boundary_provider(matrix);
  std::optional<RuntimePlanningDelivery> boundary_delivery;
  bool boundary_bootstrapped = false;
  ConcurrentTripRuntime boundary_runtime(
      {.shard_count = 1,
       .max_active_trips = 1,
       .shard_queue_capacities = {4, 4, 4, 2},
       .completion_queue_capacity = 2,
       .priority_fairness_burst = 2,
       .provider_workers = 1,
       .provider_queue_capacity = 2,
       .planner_workers = 1,
       .planner_queue_capacity = 2,
       .essential_response_capacity = 4,
       .max_advisory_payload_bytes = 1024},
      boundary_provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        boundary_delivery = std::move(delivery);
        condition.notify_all();
        return true;
      });
  if (boundary_runtime.try_bootstrap(
          {.state = first,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 104},
          [&](RuntimeBootstrapResult result) {
            std::scoped_lock lock(mutex);
            boundary_bootstrapped =
                result.status == RuntimeBootstrapStatus::kAccepted;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return boundary_bootstrapped;
        })) {
      return 1;
    }
  }
  if (boundary_runtime.try_apply_event(
          route_deviation_request(first, 1, 200),
          [](RuntimeEventAcknowledgement) {}) !=
          RuntimeSubmissionStatus::kAccepted ||
      !boundary_provider.wait_until_started() ||
      boundary_runtime.try_apply_event(
          location_request(first, 2, 201),
          [](RuntimeEventAcknowledgement) {}) !=
          RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  boundary_provider.release_first();
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3}, [&] {
          return boundary_delivery.has_value();
        }) ||
        boundary_provider.calls() != 2 ||
        !boundary_delivery->proposal.has_value() ||
        std::find(boundary_delivery->proposal->reasons.begin(),
                  boundary_delivery->proposal->reasons.end(),
                  PlanReasonCode::kRouteDeviation) ==
            boundary_delivery->proposal->reasons.end() ||
        boundary_delivery->proposal->proposal
                .source_planner_state_version !=
            PlannerStateVersion{2}) {
      return 1;
    }
  }
  if (boundary_runtime.execution_metrics() !=
      RuntimeExecutionMetrics{.planning_attempts_started = 2,
                              .planning_attempts_completed = 2,
                              .deadline_misses = 0,
                              .cancelled_attempts = 1,
                              .supersession_requests = 1,
                              .provider_failures = 0}) {
    return 1;
  }
  {
    std::scoped_lock lock(mutex);
    boundary_delivery.reset();
  }
  boundary_provider.block_next();
  if (boundary_runtime.try_apply_event(
          route_deviation_request(first, 3, 205),
          [](RuntimeEventAcknowledgement) {}) !=
          RuntimeSubmissionStatus::kAccepted ||
      !boundary_provider.wait_until_started() ||
      boundary_runtime.try_apply_event(
          reservation_request(first, 2, 1, 206),
          [](RuntimeEventAcknowledgement) {}) !=
          RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  boundary_provider.release_first();
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3}, [&] {
          return boundary_delivery.has_value();
        }) ||
        boundary_provider.calls() != 4 ||
        boundary_delivery->versions.trip_revision != 2 ||
        boundary_delivery->versions.accepted_mutation_sequence != 2 ||
        !boundary_delivery->proposal.has_value() ||
        boundary_delivery->proposal->proposal
                .source_accepted_mutation_sequence !=
            MutationSequence{2}) {
      return 1;
    }
  }
  if (boundary_runtime.execution_metrics() !=
      RuntimeExecutionMetrics{.planning_attempts_started = 4,
                              .planning_attempts_completed = 4,
                              .deadline_misses = 0,
                              .cancelled_attempts = 2,
                              .supersession_requests = 2,
                              .provider_failures = 0}) {
    return 1;
  }

  std::optional<RuntimePlanningDelivery> expired_delivery;
  bool expired_bootstrapped = false;
  ConcurrentTripRuntime expired_runtime(
      {.shard_count = 1,
       .max_active_trips = 1,
       .shard_queue_capacities = {2, 2, 2, 2},
       .completion_queue_capacity = 1,
       .priority_fairness_burst = 2,
       .provider_workers = 1,
       .provider_queue_capacity = 1,
       .planner_workers = 1,
       .planner_queue_capacity = 1,
       .essential_response_capacity = 2,
       .max_advisory_payload_bytes = 1024},
      provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        expired_delivery = std::move(delivery);
        condition.notify_all();
        return true;
      });
  if (expired_runtime.try_bootstrap(
          {.state = first,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 105},
          [&](RuntimeBootstrapResult result) {
            std::scoped_lock lock(mutex);
            expired_bootstrapped =
                result.status == RuntimeBootstrapStatus::kAccepted;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return expired_bootstrapped;
        })) {
      return 1;
    }
  }
  auto expired_request = route_deviation_request(first, 1, 210);
  expired_request.planning->deadline =
      std::chrono::steady_clock::now() - std::chrono::milliseconds{1};
  if (expired_runtime.try_apply_event(
          std::move(expired_request),
          [](RuntimeEventAcknowledgement) {}) !=
      RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return expired_delivery.has_value();
        }) ||
        expired_delivery->status !=
            RuntimePlanningStatus::kDeadlineExceeded ||
        expired_delivery->proposal.has_value()) {
      return 1;
    }
  }
  if (expired_runtime.execution_metrics() !=
      RuntimeExecutionMetrics{.planning_attempts_started = 1,
                              .planning_attempts_completed = 1,
                              .deadline_misses = 1,
                              .cancelled_attempts = 0,
                              .supersession_requests = 0,
                              .provider_failures = 1}) {
    return 1;
  }

  BlockingProvider advisory_provider(matrix);
  std::optional<RuntimePlanningDelivery> advisory_delivery;
  bool advisory_bootstrapped = false;
  ConcurrentTripRuntime advisory_runtime(
      {.shard_count = 1,
       .max_active_trips = 1,
       .shard_queue_capacities = {2, 2, 2, 2},
       .completion_queue_capacity = 2,
       .priority_fairness_burst = 2,
       .provider_workers = 1,
       .provider_queue_capacity = 2,
       .planner_workers = 1,
       .planner_queue_capacity = 2,
       .essential_response_capacity = 3,
       .max_advisory_payload_bytes = 1024},
      advisory_provider,
      [&](RuntimePlanningDelivery delivery) {
        std::scoped_lock lock(mutex);
        advisory_delivery = std::move(delivery);
        condition.notify_all();
        return true;
      });
  if (advisory_runtime.try_bootstrap(
          {.state = first,
           .owner_user_id = {},
           .runtime_epoch = 7,
           .trip_revision = 1,
           .finalized_mutation_sequence = 1,
           .current_observation_sequence = 0,
           .stream_binding = 106},
          [&](RuntimeBootstrapResult result) {
            std::scoped_lock lock(mutex);
            advisory_bootstrapped =
                result.status == RuntimeBootstrapStatus::kAccepted;
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return advisory_bootstrapped;
        })) {
      std::cerr << "advisory bootstrap callback timed out\n";
      return 1;
    }
  }
  bool advisory_location_accepted = false;
  if (advisory_runtime.try_apply_event(
          location_request(first, 1, 219),
          [&](RuntimeEventAcknowledgement acknowledgement) {
            std::scoped_lock lock(mutex);
            advisory_location_accepted =
                acknowledgement.admission.status ==
                    EventCoordinatorStatus::kAccepted &&
                !acknowledgement.admission.planning_seed.has_value();
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    std::cerr << "advisory location submission failed\n";
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return advisory_location_accepted;
        })) {
      std::cerr << "advisory location callback timed out\n";
      return 1;
    }
  }
  bool refresh_accepted = false;
  if (advisory_runtime.try_apply_event(
          recommendation_refresh_request(first, 2, 220),
          [&](RuntimeEventAcknowledgement acknowledgement) {
            std::scoped_lock lock(mutex);
            refresh_accepted =
                acknowledgement.admission.status ==
                    EventCoordinatorStatus::kAccepted &&
                acknowledgement.admission.planning_seed.has_value();
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    std::cerr << "advisory refresh submission failed\n";
    return 1;
  }
  if (!advisory_provider.wait_until_started()) {
    std::cerr << "advisory provider did not start; accepted="
              << refresh_accepted << '\n';
    return 1;
  }
  bool redundant_refresh_accepted = false;
  if (advisory_runtime.try_apply_event(
          recommendation_refresh_request(first, 3, 222),
          [&](RuntimeEventAcknowledgement acknowledgement) {
            std::scoped_lock lock(mutex);
            redundant_refresh_accepted =
                acknowledgement.admission.status ==
                    EventCoordinatorStatus::kAccepted &&
                !acknowledgement.admission.planning_seed.has_value();
            condition.notify_all();
          }) != RuntimeSubmissionStatus::kAccepted) {
    std::cerr << "redundant advisory refresh submission failed\n";
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{2}, [&] {
          return redundant_refresh_accepted;
        })) {
      std::cerr << "redundant advisory refresh callback timed out\n";
      return 1;
    }
  }
  if (advisory_runtime.try_apply_event(
          completion_request(first, 2, 1, 221),
          [](RuntimeEventAcknowledgement) {}) !=
      RuntimeSubmissionStatus::kAccepted) {
    std::cerr << "completion submission failed\n";
    return 1;
  }
  advisory_provider.release_first();
  {
    std::unique_lock lock(mutex);
    if (!condition.wait_for(lock, std::chrono::seconds{3}, [&] {
          return advisory_delivery.has_value();
        }) ||
        advisory_delivery->status != RuntimePlanningStatus::kOk ||
        !advisory_delivery->proposal.has_value() ||
        advisory_delivery->versions.trip_revision != 2 ||
        advisory_delivery->versions.accepted_mutation_sequence != 2) {
      std::cerr << "completion replacement delivery failed\n";
      return 1;
    }
  }
  if (advisory_runtime.execution_metrics() !=
      RuntimeExecutionMetrics{.planning_attempts_started = 2,
                              .planning_attempts_completed = 2,
                              .deadline_misses = 0,
                              .cancelled_attempts = 1,
                              .supersession_requests = 1,
                              .provider_failures = 0}) {
    const auto metrics = advisory_runtime.execution_metrics();
    std::cerr << "advisory metrics " << metrics.deadline_misses << ' '
              << metrics.cancelled_attempts << ' '
              << metrics.supersession_requests << '\n';
    return 1;
  }
  if (advisory_runtime.observation_metrics().replans_avoided != 2) {
    return 1;
  }
  const auto runtime_metrics = advisory_runtime.metrics();
  if (runtime_metrics.counter(MetricCounter::kAcceptedEvents) < 4 ||
      runtime_metrics.counter(MetricCounter::kReplanTriggers) < 2 ||
      runtime_metrics.counter(MetricCounter::kReplanCancellations) < 1 ||
      runtime_metrics.histogram(MetricHistogram::kQueueWait).count < 4 ||
      runtime_metrics.histogram(MetricHistogram::kPlanner).count < 1) {
    return 1;
  }

  return 0;
}
