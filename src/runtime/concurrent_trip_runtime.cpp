#include "liveroute/runtime/concurrent_trip_runtime.hpp"

#include <algorithm>
#include <atomic>
#include <chrono>
#include <limits>
#include <map>
#include <mutex>
#include <stdexcept>
#include <stop_token>
#include <utility>
#include <variant>
#include <vector>

#include "liveroute/planner/planning_input_assembler.hpp"
#include "liveroute/planner/replan_attempt.hpp"
#include "liveroute/planner/result_metadata.hpp"
#include "liveroute/runtime/bounded_executor.hpp"
#include "liveroute/runtime/planning_commit.hpp"
#include "liveroute/runtime/sharded_executor.hpp"

namespace liveroute::runtime {
namespace {

using SteadyTime = std::chrono::steady_clock::time_point;

[[nodiscard]] std::uint64_t elapsed_microseconds(SteadyTime start,
                                                 SteadyTime end) noexcept {
  if (end <= start) return 0;
  return static_cast<std::uint64_t>(
      std::chrono::duration_cast<std::chrono::microseconds>(end - start)
          .count());
}

[[nodiscard]] std::uint32_t bounded_u32(std::uint64_t value) noexcept {
  return static_cast<std::uint32_t>(
      std::min<std::uint64_t>(value,
                              std::numeric_limits<std::uint32_t>::max()));
}

[[nodiscard]] bool is_location_update(
    const domain::TripEventPayload& payload) noexcept {
  return std::holds_alternative<domain::LocationUpdated>(payload);
}

[[nodiscard]] bool is_recommendation_refresh(
    const domain::TripEventPayload& payload) noexcept {
  const auto* advisory =
      std::get_if<domain::AdvisoryUpdate>(&payload);
  return advisory != nullptr &&
         advisory->kind ==
             domain::AdvisoryKind::kRecommendationRefresh;
}

[[nodiscard]] bool is_ordinary_observation(
    const domain::TripEventPayload& payload) noexcept {
  return std::holds_alternative<domain::LocationUpdated>(payload) ||
         std::holds_alternative<domain::VelocityUpdated>(payload) ||
         std::holds_alternative<domain::HeadingUpdated>(payload);
}

[[nodiscard]] bool has_higher_priority(
    domain::EventPriority candidate,
    domain::EventPriority current) noexcept {
  return static_cast<std::uint8_t>(candidate) <
         static_cast<std::uint8_t>(current);
}

class PermitPool {
 public:
  explicit PermitPool(std::size_t capacity) : state_(std::make_shared<State>()) {
    state_->capacity = capacity;
  }

  [[nodiscard]] std::shared_ptr<void> try_acquire() {
    auto current = state_->used.load(std::memory_order_relaxed);
    while (current < state_->capacity) {
      if (state_->used.compare_exchange_weak(
              current, current + 1, std::memory_order_acq_rel,
              std::memory_order_relaxed)) {
        return {state_.get(), [state = state_](void*) {
                  state->used.fetch_sub(1, std::memory_order_release);
                }};
      }
    }
    return {};
  }

  [[nodiscard]] std::size_t used() const noexcept {
    return state_->used.load(std::memory_order_acquire);
  }

 private:
  struct State {
    std::atomic<std::size_t> used{};
    std::size_t capacity{};
  };

  std::shared_ptr<State> state_;
};

struct MatrixFailure {
  RuntimePlanningStatus status;
  bool retryable;
};

using MatrixAcquisition =
    std::variant<domain::TravelTimeMatrix, MatrixFailure>;

[[nodiscard]] MatrixFailure map_provider_error(
    routing::TravelTimeProviderError error) noexcept {
  switch (error) {
    case routing::TravelTimeProviderError::kInvalidArgument:
      return {RuntimePlanningStatus::kInvalidArgument, false};
    case routing::TravelTimeProviderError::kMatrixTooLarge:
      return {RuntimePlanningStatus::kMatrixTooLarge, false};
    case routing::TravelTimeProviderError::kResourceExhausted:
      return {RuntimePlanningStatus::kResourceExhausted, true};
    case routing::TravelTimeProviderError::kCancelled:
      return {RuntimePlanningStatus::kCancelled, false};
    case routing::TravelTimeProviderError::kDeadlineExceeded:
      return {RuntimePlanningStatus::kDeadlineExceeded, true};
    case routing::TravelTimeProviderError::kProviderUnavailable:
      return {RuntimePlanningStatus::kProviderUnavailable, true};
    case routing::TravelTimeProviderError::kInternal:
      return {RuntimePlanningStatus::kInternal, false};
  }
  return {RuntimePlanningStatus::kInternal, false};
}

[[nodiscard]] const domain::Activity* find_activity(
    const domain::TripState& state,
    const domain::ActivityId& activity_id) noexcept {
  for (const auto& activity : state.activities) {
    if (activity.activity_id == activity_id) return &activity;
  }
  return nullptr;
}

struct RouteRequest {
  std::vector<domain::Location> locations;
  std::vector<domain::TravelMode> destination_modes;
};

[[nodiscard]] std::optional<RouteRequest> make_route_request(
    const domain::TripState& state) {
  std::size_t preserved_count = state.completed_prefix_count;
  if (state.current_activity_id.has_value()) ++preserved_count;
  if (preserved_count > state.current_plan.segments.size()) {
    return std::nullopt;
  }

  RouteRequest request;
  const auto remaining_count =
      state.current_plan.segments.size() - preserved_count;
  if (remaining_count == 0) {
    request.locations.push_back(
        state.current_observation.location.value_or(domain::Location{}));
    return request;
  }
  if (!state.current_observation.location.has_value()) return std::nullopt;

  request.locations.reserve(remaining_count + 1);
  request.destination_modes.reserve(remaining_count);
  request.locations.push_back(*state.current_observation.location);
  for (std::size_t index = preserved_count;
       index < state.current_plan.segments.size(); ++index) {
    const auto* activity =
        find_activity(state, state.current_plan.segments[index].activity_id);
    if (activity == nullptr) return std::nullopt;
    request.locations.push_back(activity->location);
    request.destination_modes.push_back(activity->inbound_travel_mode);
  }
  return request;
}

[[nodiscard]] MatrixAcquisition acquire_matrix(
    const domain::TripState& state,
    const RuntimePlanningContext& context,
    routing::TravelTimeProvider& provider, std::stop_token stop_token,
    RuntimeStageTimings& timings) {
  const auto request = make_route_request(state);
  if (!request.has_value()) {
    return MatrixFailure{RuntimePlanningStatus::kInvalidArgument, false};
  }
  if (request->locations.size() == 1) {
    return domain::TravelTimeMatrix{
        1, {domain::RouteEstimate{.duration = std::chrono::seconds::zero(),
                                  .distance_meters = 0,
                                  .reachable = true}}};
  }

  const auto provider_start = std::chrono::steady_clock::now();
  std::optional<domain::TravelTimeMatrix> walking;
  std::optional<domain::TravelTimeMatrix> driving;
  for (const auto mode :
       {domain::TravelMode::kWalking, domain::TravelMode::kDriving}) {
    if (std::find(request->destination_modes.begin(),
                  request->destination_modes.end(),
                  mode) == request->destination_modes.end()) {
      continue;
    }
    auto result = provider.get_matrix(
        request->locations, mode,
        std::chrono::system_clock::time_point{
            std::chrono::milliseconds{context.current_time.value()}},
        context.deadline, stop_token);
    if (!result.has_matrix()) {
      timings.provider_microseconds = elapsed_microseconds(
          provider_start, std::chrono::steady_clock::now());
      return map_provider_error(result.error());
    }
    if (result.matrix().location_count() != request->locations.size()) {
      timings.provider_microseconds = elapsed_microseconds(
          provider_start, std::chrono::steady_clock::now());
      return MatrixFailure{RuntimePlanningStatus::kProviderUnavailable, true};
    }
    if (mode == domain::TravelMode::kWalking) {
      walking = result.matrix();
    } else {
      driving = result.matrix();
    }
  }
  const auto provider_end = std::chrono::steady_clock::now();
  timings.provider_microseconds =
      elapsed_microseconds(provider_start, provider_end);

  const auto conversion_start = std::chrono::steady_clock::now();
  const auto count = request->locations.size();
  std::vector<domain::RouteEstimate> estimates;
  estimates.reserve(count * count);
  for (std::size_t origin = 0; origin < count; ++origin) {
    for (std::size_t destination = 0; destination < count; ++destination) {
      if (destination == 0) {
        estimates.push_back(
            {.duration = std::chrono::seconds::zero(),
             .distance_meters = 0,
             .reachable = origin == 0});
        continue;
      }
      const auto mode = request->destination_modes[destination - 1];
      const auto& source =
          mode == domain::TravelMode::kWalking ? *walking : *driving;
      estimates.push_back(source.at(origin, destination));
    }
  }
  const auto conversion_end = std::chrono::steady_clock::now();
  timings.matrix_conversion_microseconds =
      elapsed_microseconds(conversion_start, conversion_end);
  return domain::TravelTimeMatrix{count, std::move(estimates)};
}

[[nodiscard]] RuntimePlanningStatus map_search_outcome(
    planner::BeamSearchOutcome outcome) noexcept {
  switch (outcome) {
    case planner::BeamSearchOutcome::kComplete:
    case planner::BeamSearchOutcome::kBestSoFar:
      return RuntimePlanningStatus::kOk;
    case planner::BeamSearchOutcome::kSearchLimited:
      return RuntimePlanningStatus::kNoNewProposal;
    case planner::BeamSearchOutcome::kExhaustiveInfeasible:
      return RuntimePlanningStatus::kInfeasible;
    case planner::BeamSearchOutcome::kDeadlineExceeded:
      return RuntimePlanningStatus::kDeadlineExceeded;
    case planner::BeamSearchOutcome::kCancelled:
      return RuntimePlanningStatus::kCancelled;
    case planner::BeamSearchOutcome::kInvalidInput:
      return RuntimePlanningStatus::kInvalidArgument;
  }
  return RuntimePlanningStatus::kInternal;
}

[[nodiscard]] bool retryable(RuntimePlanningStatus status) noexcept {
  return status == RuntimePlanningStatus::kResourceExhausted ||
         status == RuntimePlanningStatus::kDeadlineExceeded ||
         status == RuntimePlanningStatus::kProviderUnavailable;
}

[[nodiscard]] RuntimeBootstrapStatus map_bootstrap_status(
    VersionOperationStatus status) noexcept {
  switch (status) {
    case VersionOperationStatus::kAccepted:
      return RuntimeBootstrapStatus::kAccepted;
    case VersionOperationStatus::kDuplicate:
      return RuntimeBootstrapStatus::kDuplicate;
    case VersionOperationStatus::kStale:
      return RuntimeBootstrapStatus::kStale;
    case VersionOperationStatus::kInvalidArgument:
    case VersionOperationStatus::kInactive:
      return RuntimeBootstrapStatus::kInvalidArgument;
  }
  return RuntimeBootstrapStatus::kInvalidArgument;
}

[[nodiscard]] RuntimeControlResult map_control_result(
    VersionOperationResult result,
    const TripRuntimeVersionSnapshot& versions) noexcept {
  switch (result.status) {
    case VersionOperationStatus::kAccepted:
      return {RuntimeControlStatus::kOk, VersionStaleReason::kNone, false,
              versions, std::nullopt, {}};
    case VersionOperationStatus::kDuplicate:
      return {RuntimeControlStatus::kDuplicate, VersionStaleReason::kNone,
              false, versions, std::nullopt, {}};
    case VersionOperationStatus::kStale:
      return {RuntimeControlStatus::kStale, result.stale_reason, false,
              versions, std::nullopt, {}};
    case VersionOperationStatus::kInvalidArgument:
      return {RuntimeControlStatus::kInvalidArgument,
              VersionStaleReason::kNone, false, versions, std::nullopt, {}};
    case VersionOperationStatus::kInactive:
      return {RuntimeControlStatus::kInactive, VersionStaleReason::kNone, true,
              versions, std::nullopt, {}};
  }
  return {RuntimeControlStatus::kInvalidArgument, VersionStaleReason::kNone,
          false, versions, std::nullopt, {}};
}

}  // namespace

bool ConcurrentRuntimeConfiguration::is_valid() const noexcept {
  return shard_count != 0 && max_active_trips != 0 &&
         shard_queue_capacities.is_valid() &&
         completion_queue_capacity != 0 && priority_fairness_burst != 0 &&
         provider_workers != 0 && provider_queue_capacity != 0 &&
         planner_workers != 0 && planner_queue_capacity != 0 &&
         essential_response_capacity != 0 &&
         max_advisory_payload_bytes != 0;
}

bool RuntimePlanningContext::is_valid() const noexcept {
  return planning_horizon_start < planning_horizon_end &&
         current_time >= planning_horizon_start &&
         current_time < planning_horizon_end && max_candidates != 0 &&
         beam_width != 0 && max_expansions != 0;
}

class ConcurrentTripRuntime::Impl {
 public:
  Impl(ConcurrentRuntimeConfiguration configuration,
       routing::TravelTimeProvider& provider, ProposalSink sink)
      : configuration_(configuration),
        provider_(provider),
        proposal_sink_(std::move(sink)),
        responses_(configuration.essential_response_capacity),
        trip_maps_(configuration.shard_count),
        shards_(configuration.shard_count,
                configuration.shard_queue_capacities,
                configuration.priority_fairness_burst,
                configuration.completion_queue_capacity),
        executors_({.provider_workers = configuration.provider_workers,
                    .provider_queue_capacity =
                        configuration.provider_queue_capacity,
                    .planner_workers = configuration.planner_workers,
                    .planner_queue_capacity =
                        configuration.planner_queue_capacity}) {}

  [[nodiscard]] RuntimeQueueDepths queue_depths() const {
    return {
        .critical = shards_.queue_size(domain::EventPriority::kCritical),
        .high = shards_.queue_size(domain::EventPriority::kHigh),
        .normal = shards_.queue_size(domain::EventPriority::kNormal),
        .advisory = shards_.queue_size(domain::EventPriority::kAdvisory),
        .completions = shards_.completion_queue_size(),
        .provider_jobs = executors_.provider().queue_size(),
        .planner_jobs = executors_.planner().queue_size(),
        .essential_responses_in_use = responses_.used(),
    };
  }

  [[nodiscard]] RuntimeObservationMetrics observation_metrics() const noexcept {
    return {
        .received_location_events =
            received_location_events_.load(std::memory_order_acquire),
        .coalesced_location_replans =
            coalesced_location_replans_.load(std::memory_order_acquire),
        .dropped_stale_location_events =
            dropped_stale_location_events_.load(std::memory_order_acquire),
        .replans_avoided =
            replans_avoided_.load(std::memory_order_acquire),
    };
  }

  [[nodiscard]] RuntimeExecutionMetrics execution_metrics() const noexcept {
    return {
        .planning_attempts_started =
            planning_attempts_started_.load(std::memory_order_acquire),
        .planning_attempts_completed =
            planning_attempts_completed_.load(std::memory_order_acquire),
        .deadline_misses =
            deadline_misses_.load(std::memory_order_acquire),
        .cancelled_attempts =
            cancelled_attempts_.load(std::memory_order_acquire),
        .supersession_requests =
            supersession_requests_.load(std::memory_order_acquire),
        .provider_failures =
            provider_failures_.load(std::memory_order_acquire),
    };
  }

  void record_terminal_work_status(RuntimePlanningStatus status) noexcept {
    if (status == RuntimePlanningStatus::kDeadlineExceeded) {
      deadline_misses_.fetch_add(1, std::memory_order_relaxed);
    } else if (status == RuntimePlanningStatus::kCancelled) {
      cancelled_attempts_.fetch_add(1, std::memory_order_relaxed);
    }
  }

  void record_provider_failure(RuntimePlanningStatus status) noexcept {
    if (status != RuntimePlanningStatus::kCancelled &&
        status != RuntimePlanningStatus::kInvalidArgument) {
      provider_failures_.fetch_add(1, std::memory_order_relaxed);
    }
  }

  struct PendingPlanning {
    ImmutablePlanningSeed seed;
    RuntimePlanningContext context;
    RuntimeStageTimings timings;
    SteadyTime started_at;
    domain::EventPriority priority{domain::EventPriority::kNormal};
    bool latest_input_is_ordinary_observation{};
  };

  struct ActiveTrip {
    domain::TripState state;
    std::string owner_user_id;
    TripRuntimeVersions versions;
    std::optional<std::uint64_t> stream_binding;
    bool deactivation_pending{};
    bool planning_running{};
    std::shared_ptr<std::stop_source> planning_stop;
    domain::EventPriority planning_priority{
        domain::EventPriority::kNormal};
    domain::TripEventPayload planning_trigger;
    std::optional<PendingPlanning> pending;
  };

  using TripMap = std::map<domain::TripId, ActiveTrip>;

  [[nodiscard]] RuntimeSubmissionStatus try_bootstrap(
      RuntimeBootstrapRequest request, BootstrapCallback callback) {
    if (!accepting_.load(std::memory_order_acquire)) {
      return RuntimeSubmissionStatus::kStopping;
    }
    auto response = responses_.try_acquire();
    if (!response) return RuntimeSubmissionStatus::kResponseCapacityFull;
    const auto trip_id = request.state.trip_id;
    const auto shard_index = shards_.shard_for(trip_id);
    if (!shards_.try_submit(
            trip_id, domain::EventPriority::kCritical,
            [this, request = std::move(request),
             callback = std::move(callback), response = std::move(response),
             shard_index](std::stop_token) mutable {
              auto& trips = trip_maps_[shard_index];
              auto found = trips.find(request.state.trip_id);
              if (!request.state.is_valid() ||
                  request.state.active_proposal.has_value()) {
                callback({RuntimeBootstrapStatus::kInvalidArgument, {},
                          std::nullopt, std::nullopt});
                return;
              }
              const auto observation_is_present =
                  request.state.current_observation.location.has_value() ||
                  request.state.current_observation.observed_at.has_value() ||
                  request.state.current_observation
                      .velocity_meters_per_second.has_value() ||
                  request.state.current_observation.heading_degrees
                      .has_value();
              const auto is_higher_epoch =
                  found == trips.end() ||
                  request.runtime_epoch >
                      found->second.versions.snapshot().runtime_epoch;
              if (is_higher_epoch &&
                  (request.current_observation_sequence != 0 ||
                   observation_is_present)) {
                callback({RuntimeBootstrapStatus::kInvalidArgument, {},
                          std::nullopt, std::nullopt});
                return;
              }
              if (found == trips.end()) {
                auto current =
                    active_trip_count_.load(std::memory_order_relaxed);
                while (current < configuration_.max_active_trips &&
                       !active_trip_count_.compare_exchange_weak(
                           current, current + 1, std::memory_order_acq_rel,
                           std::memory_order_relaxed)) {
                }
                if (current >= configuration_.max_active_trips) {
                  callback({RuntimeBootstrapStatus::kResourceExhausted, {},
                            std::nullopt, std::nullopt});
                  return;
                }

                TripRuntimeVersions versions;
                const auto status = versions.bootstrap(
                    request.runtime_epoch, request.trip_revision,
                    request.finalized_mutation_sequence,
                    request.current_observation_sequence);
                if (!status.accepted()) {
                  active_trip_count_.fetch_sub(1, std::memory_order_release);
                  callback({RuntimeBootstrapStatus::kInvalidArgument, {},
                            std::nullopt, std::nullopt});
                  return;
                }
                auto [inserted, unused] = trips.emplace(
                    request.state.trip_id,
                    ActiveTrip{.state = std::move(request.state),
                               .owner_user_id =
                                   std::move(request.owner_user_id),
                               .versions = std::move(versions),
                               .stream_binding = request.stream_binding,
                               .deactivation_pending = false,
                               .planning_running = false,
                               .planning_stop = nullptr,
                               .planning_priority =
                                   domain::EventPriority::kNormal,
                               .planning_trigger = std::monostate{},
                               .pending = std::nullopt});
                (void)unused;
                callback({RuntimeBootstrapStatus::kAccepted,
                          inserted->second.versions.snapshot(),
                          inserted->second.state.current_plan.plan_id,
                          inserted->second.state.active_proposal});
                return;
              }

              const auto status = found->second.versions.bootstrap(
                  request.runtime_epoch, request.trip_revision,
                  request.finalized_mutation_sequence,
                  request.current_observation_sequence);
              if (status.accepted()) {
                if (found->second.planning_stop != nullptr) {
                  found->second.planning_stop->request_stop();
                }
                found->second.state = std::move(request.state);
                found->second.owner_user_id =
                    std::move(request.owner_user_id);
                found->second.stream_binding = request.stream_binding;
                found->second.deactivation_pending = false;
                found->second.pending.reset();
              } else if (status.status == VersionOperationStatus::kDuplicate &&
                         request.stream_binding.has_value()) {
                found->second.stream_binding = request.stream_binding;
              }
              callback({map_bootstrap_status(status.status),
                        found->second.versions.snapshot(),
                        found->second.state.current_plan.plan_id,
                        found->second.state.active_proposal});
            })) {
      return RuntimeSubmissionStatus::kShardQueueFull;
    }
    return RuntimeSubmissionStatus::kAccepted;
  }

  [[nodiscard]] RuntimeSubmissionStatus try_apply_event(
      RuntimeEventRequest request, EventAcknowledgementCallback callback) {
    if (is_location_update(request.admission.event.payload)) {
      received_location_events_.fetch_add(1, std::memory_order_relaxed);
    }
    if (!accepting_.load(std::memory_order_acquire)) {
      return RuntimeSubmissionStatus::kStopping;
    }
    auto response = responses_.try_acquire();
    if (!response) return RuntimeSubmissionStatus::kResponseCapacityFull;
    const auto priority = request.admission.event.priority_for({});
    const auto submitted_at = std::chrono::steady_clock::now();
    const auto shard_index = shards_.shard_for(request.trip_id);
    if (!shards_.try_submit(
            request.trip_id, priority,
            [this, request = std::move(request),
             callback = std::move(callback), response = std::move(response),
             submitted_at, shard_index](std::stop_token) mutable {
              RuntimeStageTimings timings;
              const auto application_start = std::chrono::steady_clock::now();
              timings.queue_wait_microseconds =
                  elapsed_microseconds(submitted_at, application_start);
              auto& trips = trip_maps_[shard_index];
              auto found = trips.find(request.trip_id);
              if (found == trips.end()) {
                auto result = EventCoordinatorResult{
                    .status = EventCoordinatorStatus::kInactive,
                    .stale_reason = EventCoordinatorStaleReason::kNone,
                    .retryable = true,
                    .planning_input_changed = false,
                    .current_plan_changed = false,
                    .version_snapshot = {},
                    .planning_seed = std::nullopt,
                    .resulting_current_plan_id = std::nullopt};
                callback({std::move(result), timings});
                return;
              }

              auto& trip = found->second;
              if (trip.deactivation_pending) {
                auto result = EventCoordinatorResult{
                    .status = EventCoordinatorStatus::kInactive,
                    .stale_reason = EventCoordinatorStaleReason::kNone,
                    .retryable = true,
                    .planning_input_changed = false,
                    .current_plan_changed = false,
                    .version_snapshot = trip.versions.snapshot(),
                    .planning_seed = std::nullopt,
                    .resulting_current_plan_id = std::nullopt};
                callback({std::move(result), timings});
                return;
              }
              const auto event_priority =
                  request.admission.event.priority_for(
                      trip.state.activities);
              const bool ordinary_observation =
                  is_ordinary_observation(
                      request.admission.event.payload);
              const bool location_update =
                  is_location_update(request.admission.event.payload);
              const bool recommendation_refresh =
                  is_recommendation_refresh(
                      request.admission.event.payload);
              auto admission = coordinate_event_admission(
                  trip.state, trip.versions, request.admission,
                  request.planning->current_time,
                  configuration_.max_advisory_payload_bytes);
              if (location_update &&
                  admission.status == EventCoordinatorStatus::kStale) {
                dropped_stale_location_events_.fetch_add(
                    1, std::memory_order_relaxed);
              }
              const auto application_end = std::chrono::steady_clock::now();
              timings.event_application_microseconds =
                  elapsed_microseconds(application_start, application_end);
              if (recommendation_refresh &&
                  admission.status == EventCoordinatorStatus::kAccepted &&
                  !admission.planning_seed.has_value()) {
                const auto token = trip.versions.capture_planning_work();
                if (!token.has_value()) {
                  admission.status = EventCoordinatorStatus::kInternal;
                } else {
                  admission.planning_seed = ImmutablePlanningSeed{
                      .state = trip.state,
                      .trigger = request.admission.event.payload,
                      .source_versions = trip.versions.snapshot(),
                      .work_token = *token};
                }
              }
              if (ordinary_observation &&
                  admission.planning_seed.has_value() &&
                  !trip.planning_running &&
                  !trip.pending.has_value()) {
                admission.planning_seed.reset();
                replans_avoided_.fetch_add(
                    1, std::memory_order_relaxed);
              }
              if (recommendation_refresh &&
                  admission.planning_seed.has_value() &&
                  (trip.planning_running || trip.pending.has_value())) {
                admission.planning_seed.reset();
                replans_avoided_.fetch_add(
                    1, std::memory_order_relaxed);
              }
              const auto seed = admission.planning_seed;
              const auto response_start = std::chrono::steady_clock::now();
              RuntimeEventAcknowledgement acknowledgement{
                  .admission = std::move(admission), .timings = timings};
              const auto response_end = std::chrono::steady_clock::now();
              acknowledgement.timings.response_assembly_microseconds =
                  elapsed_microseconds(response_start, response_end);
              acknowledgement.timings.total_microseconds =
                  elapsed_microseconds(submitted_at, response_end);
              timings = acknowledgement.timings;
              callback(std::move(acknowledgement));

              if (!seed.has_value() || !request.planning.has_value()) {
                return;
              }
              PendingPlanning pending{.seed = *seed,
                                      .context = *request.planning,
                                      .timings = timings,
                                      .started_at = submitted_at,
                                      .priority = event_priority,
                                      .latest_input_is_ordinary_observation =
                                          ordinary_observation};
              if (!request.planning->is_valid()) {
                deliver_failure(request.trip_id, trip,
                                RuntimePlanningStatus::kInvalidArgument,
                                std::move(pending), false);
                return;
              }
              if (trip.planning_running) {
                preserve_higher_priority_trigger(
                    pending, trip.planning_priority,
                    trip.planning_trigger);
                if (trip.pending.has_value()) {
                  preserve_higher_priority_trigger(
                      pending, trip.pending->priority,
                      trip.pending->seed.trigger);
                }
                if (trip.planning_stop != nullptr) {
                  if (trip.planning_stop->request_stop()) {
                    supersession_requests_.fetch_add(
                        1, std::memory_order_relaxed);
                  }
                }
                if (ordinary_observation) {
                  record_coalesced_location_replan(location_update);
                }
                trip.pending = std::move(pending);
                return;
              }
              if (trip.pending.has_value()) {
                preserve_higher_priority_trigger(
                    pending, trip.pending->priority,
                    trip.pending->seed.trigger);
                if (ordinary_observation) {
                  record_coalesced_location_replan(location_update);
                }
                trip.pending = std::move(pending);
                dispatch_pending(request.trip_id, trip);
                return;
              }
              dispatch_planning(request.trip_id, trip, std::move(pending));
            })) {
      return RuntimeSubmissionStatus::kShardQueueFull;
    }
    return RuntimeSubmissionStatus::kAccepted;
  }

  [[nodiscard]] RuntimeSubmissionStatus try_confirm_finalized(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      std::uint64_t finalized_mutation_sequence, ControlCallback callback) {
    return submit_control(
        trip_id,
        [runtime_epoch, finalized_mutation_sequence](
            ActiveTrip& trip) -> RuntimeControlResult {
          const auto result = trip.versions.confirm_finalized(
              runtime_epoch, finalized_mutation_sequence);
          return map_control_result(result, trip.versions.snapshot());
        },
        std::move(callback));
  }

  [[nodiscard]] RuntimeSubmissionStatus try_snapshot(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      std::uint64_t minimum_finalized_mutation_sequence,
      std::uint64_t minimum_planner_state_version, ControlCallback callback) {
    return submit_control(
        trip_id,
        [runtime_epoch, minimum_finalized_mutation_sequence,
         minimum_planner_state_version](
            ActiveTrip& trip) -> RuntimeControlResult {
          const auto versions = trip.versions.snapshot();
          if (runtime_epoch != versions.runtime_epoch) {
            return {RuntimeControlStatus::kStale, VersionStaleReason::kEpoch,
                    false, versions, std::nullopt, {}};
          }
          if (!trip.versions.snapshot_ready() ||
              versions.finalized_mutation_sequence <
                  minimum_finalized_mutation_sequence ||
              versions.planner_state_version <
                  minimum_planner_state_version) {
            return {RuntimeControlStatus::kSnapshotNotReady,
                    VersionStaleReason::kNone, true, versions, std::nullopt,
                    {}};
          }
          auto state = trip.state;
          state.current_observation = {};
          state.active_proposal.reset();
          return {RuntimeControlStatus::kOk, VersionStaleReason::kNone, false,
                  versions, std::move(state), trip.owner_user_id};
        },
        std::move(callback));
  }

  [[nodiscard]] RuntimeSubmissionStatus try_deactivate(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      bool final_snapshot_required, ControlCallback callback) {
    if (!accepting_.load(std::memory_order_acquire)) {
      return RuntimeSubmissionStatus::kStopping;
    }
    auto response = responses_.try_acquire();
    if (!response) return RuntimeSubmissionStatus::kResponseCapacityFull;
    const auto shard_index = shards_.shard_for(trip_id);
    if (!shards_.try_submit(
            trip_id, domain::EventPriority::kCritical,
            [this, trip_id, runtime_epoch, final_snapshot_required,
             callback = std::move(callback), response = std::move(response),
             shard_index](std::stop_token) mutable {
              auto& trips = trip_maps_[shard_index];
              auto found = trips.find(trip_id);
              if (found == trips.end()) {
                callback({RuntimeControlStatus::kInactive,
                          VersionStaleReason::kNone, true, {}, std::nullopt,
                          {}});
                return;
              }
              auto& trip = found->second;
              const auto versions = trip.versions.snapshot();
              if (runtime_epoch != versions.runtime_epoch) {
                callback({RuntimeControlStatus::kStale,
                          VersionStaleReason::kEpoch, false, versions,
                          std::nullopt, {}});
                return;
              }
              if (final_snapshot_required &&
                  !trip.versions.snapshot_ready()) {
                callback({RuntimeControlStatus::kSnapshotNotReady,
                          VersionStaleReason::kNone, true, versions,
                          std::nullopt, {}});
                return;
              }
              if (final_snapshot_required) {
                trip.deactivation_pending = true;
                if (trip.planning_stop != nullptr) {
                  trip.planning_stop->request_stop();
                }
                trip.pending.reset();
                auto snapshot = trip.state;
                snapshot.current_observation = {};
                snapshot.active_proposal.reset();
                callback({RuntimeControlStatus::kOk,
                          VersionStaleReason::kNone, false, versions,
                          std::move(snapshot), trip.owner_user_id});
                return;
              }
              std::optional<domain::TripState> snapshot;
              auto owner_user_id = trip.owner_user_id;
              if (trip.planning_stop != nullptr) {
                trip.planning_stop->request_stop();
              }
              trips.erase(found);
              active_trip_count_.fetch_sub(1, std::memory_order_release);
              callback({RuntimeControlStatus::kOk,
                        VersionStaleReason::kNone, false, versions,
                        std::move(snapshot), std::move(owner_user_id)});
            })) {
      return RuntimeSubmissionStatus::kShardQueueFull;
    }
    return RuntimeSubmissionStatus::kAccepted;
  }

  [[nodiscard]] RuntimeSubmissionStatus try_abort_deactivation(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      ControlCallback callback) {
    return submit_control(
        trip_id,
        [runtime_epoch](ActiveTrip& trip) {
          const auto versions = trip.versions.snapshot();
          if (runtime_epoch != versions.runtime_epoch) {
            return RuntimeControlResult{
                RuntimeControlStatus::kStale,
                VersionStaleReason::kEpoch,
                false,
                versions,
                std::nullopt,
                {}};
          }
          if (!trip.deactivation_pending) {
            return RuntimeControlResult{
                RuntimeControlStatus::kDuplicate,
                VersionStaleReason::kNone,
                false,
                versions,
                std::nullopt,
                {}};
          }
          trip.deactivation_pending = false;
          return RuntimeControlResult{
              RuntimeControlStatus::kOk,
              VersionStaleReason::kNone,
              false,
              versions,
              std::nullopt,
              {}};
        },
        std::move(callback));
  }

  [[nodiscard]] RuntimeSubmissionStatus try_unbind(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      std::uint64_t stream_binding) {
    if (!accepting_.load(std::memory_order_acquire)) {
      return RuntimeSubmissionStatus::kStopping;
    }
    const auto shard_index = shards_.shard_for(trip_id);
    if (!shards_.try_submit(
            trip_id, domain::EventPriority::kCritical,
            [this, trip_id, runtime_epoch, stream_binding,
             shard_index](std::stop_token) {
              auto& trips = trip_maps_[shard_index];
              const auto found = trips.find(trip_id);
              if (found != trips.end() &&
                  found->second.versions.snapshot().runtime_epoch ==
                      runtime_epoch &&
                  found->second.stream_binding ==
                      std::optional<std::uint64_t>{stream_binding}) {
                found->second.stream_binding.reset();
                if (found->second.planning_stop != nullptr) {
                  found->second.planning_stop->request_stop();
                }
                found->second.pending.reset();
              }
            })) {
      return RuntimeSubmissionStatus::kShardQueueFull;
    }
    return RuntimeSubmissionStatus::kAccepted;
  }

  template <typename Operation>
  [[nodiscard]] RuntimeSubmissionStatus submit_control(
      const domain::TripId& trip_id, Operation operation,
      ControlCallback callback) {
    if (!accepting_.load(std::memory_order_acquire)) {
      return RuntimeSubmissionStatus::kStopping;
    }
    auto response = responses_.try_acquire();
    if (!response) return RuntimeSubmissionStatus::kResponseCapacityFull;
    const auto shard_index = shards_.shard_for(trip_id);
    if (!shards_.try_submit(
            trip_id, domain::EventPriority::kCritical,
            [this, trip_id, operation = std::move(operation),
             callback = std::move(callback), response = std::move(response),
             shard_index](std::stop_token) mutable {
              auto& trips = trip_maps_[shard_index];
              auto found = trips.find(trip_id);
              if (found == trips.end()) {
                callback({RuntimeControlStatus::kInactive,
                          VersionStaleReason::kNone, true, {}, std::nullopt,
                          {}});
                return;
              }
              callback(operation(found->second));
            })) {
      return RuntimeSubmissionStatus::kShardQueueFull;
    }
    return RuntimeSubmissionStatus::kAccepted;
  }

  static void preserve_higher_priority_trigger(
      PendingPlanning& replacement,
      domain::EventPriority retained_priority,
      const domain::TripEventPayload& retained_trigger) {
    if (!has_higher_priority(retained_priority,
                             replacement.priority)) {
      return;
    }
    replacement.priority = retained_priority;
    replacement.seed.trigger = retained_trigger;
  }

  void record_coalesced_location_replan(bool location_update) noexcept {
    if (location_update) {
      coalesced_location_replans_.fetch_add(
          1, std::memory_order_relaxed);
    }
    replans_avoided_.fetch_add(1, std::memory_order_relaxed);
  }

  void dispatch_planning(const domain::TripId& trip_id, ActiveTrip& trip,
                         PendingPlanning pending) {
    auto completion = shards_.try_reserve_completion(trip_id);
    if (!completion.has_value()) {
      deliver_failure(trip_id, trip, RuntimePlanningStatus::kResourceExhausted,
                      std::move(pending), false);
      return;
    }

    trip.planning_running = true;
    trip.planning_priority = pending.priority;
    trip.planning_trigger = pending.seed.trigger;
    trip.planning_stop = std::make_shared<std::stop_source>();
    planning_attempts_started_.fetch_add(1, std::memory_order_relaxed);
    auto stop = trip.planning_stop;
    auto pending_work =
        std::make_shared<PendingPlanning>(std::move(pending));
    auto reserved = std::make_shared<ShardedExecutor::CompletionReservation>(
        std::move(*completion));
    if (!executors_.provider().try_submit(
            [this, trip_id, pending_work, stop,
             reserved](std::stop_token executor_stop) mutable {
              std::stop_callback shutdown_callback(
                  executor_stop, [stop] { stop->request_stop(); });
              pending_work->timings.queue_wait_microseconds =
                  elapsed_microseconds(pending_work->started_at,
                                       std::chrono::steady_clock::now());
              auto matrix =
                  acquire_matrix(pending_work->seed.state,
                                 pending_work->context, provider_,
                                 stop->get_token(), pending_work->timings);
              if (const auto* failure = std::get_if<MatrixFailure>(&matrix)) {
                record_provider_failure(failure->status);
                record_terminal_work_status(failure->status);
                planning_attempts_completed_.fetch_add(
                    1, std::memory_order_relaxed);
                post_completion(
                    trip_id, reserved,
                    [this, trip_id, pending_work,
                     status = failure->status](ActiveTrip& active) mutable {
                      finish_failure(trip_id, active, status,
                                     std::move(*pending_work));
                    });
                return;
              }

              auto immutable_matrix = std::make_shared<domain::TravelTimeMatrix>(
                  std::move(std::get<domain::TravelTimeMatrix>(matrix)));
              if (!executors_.planner().try_submit(
                      [this, trip_id, pending_work, stop,
                       reserved, immutable_matrix](
                          std::stop_token planner_executor_stop) mutable {
                        std::stop_callback shutdown_callback(
                            planner_executor_stop,
                            [stop] { stop->request_stop(); });
                        run_planner(trip_id, std::move(*pending_work), stop,
                                    reserved, immutable_matrix);
                      })) {
                planning_attempts_completed_.fetch_add(
                    1, std::memory_order_relaxed);
                post_completion(
                    trip_id, reserved,
                    [this, trip_id, pending_work](
                        ActiveTrip& active) mutable {
                      finish_failure(
                          trip_id, active,
                          RuntimePlanningStatus::kResourceExhausted,
                          std::move(*pending_work));
                    });
              }
            })) {
      trip.planning_running = false;
      trip.planning_stop.reset();
      planning_attempts_completed_.fetch_add(
          1, std::memory_order_relaxed);
      deliver_failure(trip_id, trip, RuntimePlanningStatus::kResourceExhausted,
                      std::move(*pending_work), false);
    }
  }

  void run_planner(
      const domain::TripId& trip_id, PendingPlanning pending,
      const std::shared_ptr<std::stop_source>& stop,
      const std::shared_ptr<ShardedExecutor::CompletionReservation>& reserved,
      const std::shared_ptr<domain::TravelTimeMatrix>& matrix) {
    const auto planner_start = std::chrono::steady_clock::now();
    const auto input = planner::assemble_beam_search_input(
        pending.seed.state, pending.context.current_time,
        pending.context.planning_horizon_start,
        pending.context.planning_horizon_end, *matrix);
    if (!input.has_value()) {
      planning_attempts_completed_.fetch_add(
          1, std::memory_order_relaxed);
      post_completion(
          trip_id, reserved,
          [this, trip_id, pending = std::move(pending)](
              ActiveTrip& active) mutable {
            finish_failure(trip_id, active,
                           RuntimePlanningStatus::kInvalidArgument,
                           std::move(pending));
          });
      return;
    }
    const auto facts = planner::derive_replan_facts(*input);
    if (!facts.has_value()) {
      planning_attempts_completed_.fetch_add(
          1, std::memory_order_relaxed);
      post_completion(
          trip_id, reserved,
          [this, trip_id, pending = std::move(pending)](
              ActiveTrip& active) mutable {
            finish_failure(trip_id, active, RuntimePlanningStatus::kInternal,
                           std::move(pending));
          });
      return;
    }

    const auto& source_versions = pending.seed.source_versions;
    const planner::ProposalSource source{
        .proposal_id = pending.context.proposal_id,
        .runtime_epoch = source_versions.runtime_epoch,
        .planner_state_version =
            domain::PlannerStateVersion{source_versions.planner_state_version},
        .base_current_plan_id = pending.seed.state.current_plan.plan_id,
        .trip_revision =
            domain::TripRevision{source_versions.trip_revision},
        .accepted_mutation_sequence = domain::MutationSequence{
            source_versions.accepted_mutation_sequence},
        .created_at = pending.context.proposal_created_at,
    };
    const planner::ReplanBudget budget{
        .deadline = pending.context.deadline,
        .max_candidates = pending.context.max_candidates,
        .beam_width = pending.context.beam_width,
        .max_expansions = pending.context.max_expansions,
        .stop_token = stop->get_token(),
    };
    auto attempt = planner::run_replan_attempt(
        *input, pending.seed.state.activities, source, pending.seed.trigger,
        *facts, budget);
    if (attempt.search.deadline_hit) {
      deadline_misses_.fetch_add(1, std::memory_order_relaxed);
    }
    if (attempt.search.cancellation_requested) {
      cancelled_attempts_.fetch_add(1, std::memory_order_relaxed);
    }
    planning_attempts_completed_.fetch_add(
        1, std::memory_order_relaxed);
    const auto planner_end = std::chrono::steady_clock::now();
    pending.timings.planner_microseconds =
        elapsed_microseconds(planner_start, planner_end);

    domain::PlannerStats stats{
        .candidates_evaluated = attempt.search.candidate_count,
        .candidates_pruned = 0,
        .search_depth =
            static_cast<std::uint32_t>(input->remaining_activities.size()),
        .queue_wait_microseconds =
            bounded_u32(pending.timings.queue_wait_microseconds),
        .provider_microseconds =
            bounded_u32(pending.timings.provider_microseconds),
        .planner_microseconds =
            bounded_u32(pending.timings.planner_microseconds),
        .serialization_microseconds = 0,
        .deadline_hit = attempt.search.deadline_hit,
    };
    auto stored = planner::assemble_stored_plan_proposal(
        attempt, pending.seed.state.activities, stats,
        domain::RoutingQuality::kFresh, pending.context.recovery_state);
    const auto status = map_search_outcome(attempt.search.outcome);
    post_completion(
        trip_id, reserved,
        [this, trip_id, pending = std::move(pending), status,
         stored = std::move(stored)](ActiveTrip& active) mutable {
          finish_planning(trip_id, active, status, std::move(stored),
                          std::move(pending));
        });
  }

  template <typename Callback>
  void post_completion(
      const domain::TripId& trip_id,
      const std::shared_ptr<ShardedExecutor::CompletionReservation>& reserved,
      Callback callback) {
    const auto shard_index = shards_.shard_for(trip_id);
    const bool submitted = shards_.submit_completion(
        std::move(*reserved),
        [this, trip_id, callback = std::move(callback),
         shard_index](std::stop_token) mutable {
          auto& trips = trip_maps_[shard_index];
          const auto found = trips.find(trip_id);
          if (found != trips.end()) callback(found->second);
        });
    (void)submitted;
  }

  void finish_planning(const domain::TripId& trip_id, ActiveTrip& trip,
                       RuntimePlanningStatus status,
                       std::optional<domain::StoredPlanProposal> proposal,
                       PendingPlanning pending) {
    trip.planning_running = false;
    trip.planning_stop.reset();
    if (!trip.versions.can_commit_planning_work(
            pending.seed.work_token)) {
      dispatch_pending(trip_id, trip);
      return;
    }
    if (proposal.has_value()) {
      const auto committed =
          commit_planning_result(trip.state, trip.versions,
                                 pending.seed.work_token, *proposal);
      if (committed.status == PlanningCommitStatus::kStale) {
        status = RuntimePlanningStatus::kStale;
        proposal.reset();
      } else if (committed.status == PlanningCommitStatus::kInvalidArgument) {
        status = RuntimePlanningStatus::kInternal;
        proposal.reset();
      }
    } else if (status == RuntimePlanningStatus::kOk) {
      status = RuntimePlanningStatus::kInternal;
    }
    deliver(trip_id, trip, status, std::move(proposal), std::move(pending));
    dispatch_pending(trip_id, trip);
  }

  void finish_failure(const domain::TripId& trip_id, ActiveTrip& trip,
                      RuntimePlanningStatus status, PendingPlanning pending) {
    trip.planning_running = false;
    trip.planning_stop.reset();
    if (!trip.versions.can_commit_planning_work(
            pending.seed.work_token)) {
      dispatch_pending(trip_id, trip);
      return;
    }
    deliver_failure(trip_id, trip, status, std::move(pending),
                    trip.pending.has_value());
    dispatch_pending(trip_id, trip);
  }

  void dispatch_pending(const domain::TripId& trip_id, ActiveTrip& trip) {
    if (!accepting_.load(std::memory_order_acquire) ||
        !trip.pending.has_value()) {
      return;
    }
    if (trip.pending->latest_input_is_ordinary_observation &&
        shards_.queue_size_for_trip(
            trip_id, domain::EventPriority::kNormal) != 0) {
      return;
    }
    auto pending = std::move(*trip.pending);
    trip.pending.reset();
    dispatch_planning(trip_id, trip, std::move(pending));
  }

  void deliver_failure(const domain::TripId& trip_id, ActiveTrip& trip,
                       RuntimePlanningStatus status, PendingPlanning pending,
                       bool replacement_pending) {
    deliver(trip_id, trip, status, std::nullopt, std::move(pending),
            replacement_pending);
  }

  void deliver(const domain::TripId& trip_id, ActiveTrip& trip,
               RuntimePlanningStatus status,
               std::optional<domain::StoredPlanProposal> proposal,
               PendingPlanning pending,
               bool replacement_pending = false) {
    const auto response_start = std::chrono::steady_clock::now();
    RuntimePlanningDelivery delivery{
        .trip_id = trip_id,
        .stream_binding = trip.stream_binding,
        .versions = pending.seed.source_versions,
        .status = status,
        .retryable = retryable(status),
        .coalesced_replacement_pending =
            replacement_pending || trip.pending.has_value(),
        .timings = pending.timings,
        .proposal = std::move(proposal),
    };
    const auto response_end = std::chrono::steady_clock::now();
    delivery.timings.response_assembly_microseconds +=
        elapsed_microseconds(response_start, response_end);
    delivery.timings.total_microseconds =
        elapsed_microseconds(pending.started_at, response_end);
    if (trip.stream_binding.has_value() && proposal_sink_) {
      (void)proposal_sink_(std::move(delivery));
    }
  }

  ConcurrentRuntimeConfiguration configuration_;
  routing::TravelTimeProvider& provider_;
  ProposalSink proposal_sink_;
  PermitPool responses_;
  std::vector<TripMap> trip_maps_;
  std::atomic<std::size_t> active_trip_count_{};
  std::atomic<bool> accepting_{true};
  std::atomic<std::uint64_t> received_location_events_{};
  std::atomic<std::uint64_t> coalesced_location_replans_{};
  std::atomic<std::uint64_t> dropped_stale_location_events_{};
  std::atomic<std::uint64_t> replans_avoided_{};
  std::atomic<std::uint64_t> planning_attempts_started_{};
  std::atomic<std::uint64_t> planning_attempts_completed_{};
  std::atomic<std::uint64_t> deadline_misses_{};
  std::atomic<std::uint64_t> cancelled_attempts_{};
  std::atomic<std::uint64_t> supersession_requests_{};
  std::atomic<std::uint64_t> provider_failures_{};
  // Destruction is reverse declaration order: executor jobs finish while the
  // shard dispatcher is alive, shards join while trip_maps_ is alive, then
  // shard-owned state is released.
  ShardedExecutor shards_;
  ProviderAndPlannerExecutors executors_;

  friend class ConcurrentTripRuntime;
};

ConcurrentTripRuntime::ConcurrentTripRuntime(
    ConcurrentRuntimeConfiguration configuration,
    routing::TravelTimeProvider& travel_time_provider,
    ProposalSink proposal_sink) {
  if (!configuration.is_valid()) {
    throw std::invalid_argument("invalid concurrent runtime configuration");
  }
  impl_ = std::make_unique<Impl>(configuration, travel_time_provider,
                                 std::move(proposal_sink));
}

ConcurrentTripRuntime::~ConcurrentTripRuntime() = default;

RuntimeSubmissionStatus ConcurrentTripRuntime::try_bootstrap(
    RuntimeBootstrapRequest request, BootstrapCallback callback) {
  return impl_->try_bootstrap(std::move(request), std::move(callback));
}

RuntimeSubmissionStatus ConcurrentTripRuntime::try_apply_event(
    RuntimeEventRequest request, EventAcknowledgementCallback callback) {
  return impl_->try_apply_event(std::move(request), std::move(callback));
}

RuntimeSubmissionStatus ConcurrentTripRuntime::try_confirm_finalized(
    const domain::TripId& trip_id, std::uint64_t runtime_epoch,
    std::uint64_t finalized_mutation_sequence, ControlCallback callback) {
  return impl_->try_confirm_finalized(
      trip_id, runtime_epoch, finalized_mutation_sequence,
      std::move(callback));
}

RuntimeSubmissionStatus ConcurrentTripRuntime::try_snapshot(
    const domain::TripId& trip_id, std::uint64_t runtime_epoch,
    std::uint64_t minimum_finalized_mutation_sequence,
    std::uint64_t minimum_planner_state_version, ControlCallback callback) {
  return impl_->try_snapshot(
      trip_id, runtime_epoch, minimum_finalized_mutation_sequence,
      minimum_planner_state_version, std::move(callback));
}

RuntimeSubmissionStatus ConcurrentTripRuntime::try_deactivate(
    const domain::TripId& trip_id, std::uint64_t runtime_epoch,
    bool final_snapshot_required, ControlCallback callback) {
  return impl_->try_deactivate(trip_id, runtime_epoch,
                               final_snapshot_required,
                               std::move(callback));
}

RuntimeSubmissionStatus ConcurrentTripRuntime::try_abort_deactivation(
    const domain::TripId& trip_id, std::uint64_t runtime_epoch,
    ControlCallback callback) {
  return impl_->try_abort_deactivation(
      trip_id, runtime_epoch, std::move(callback));
}

RuntimeSubmissionStatus ConcurrentTripRuntime::try_unbind_stream(
    const domain::TripId& trip_id, std::uint64_t runtime_epoch,
    std::uint64_t stream_binding) {
  return impl_->try_unbind(trip_id, runtime_epoch, stream_binding);
}

void ConcurrentTripRuntime::stop_accepting() noexcept {
  impl_->accepting_.store(false, std::memory_order_release);
}

bool ConcurrentTripRuntime::is_accepting() const noexcept {
  return impl_->accepting_.load(std::memory_order_acquire);
}

std::size_t ConcurrentTripRuntime::active_trip_count() const noexcept {
  return impl_->active_trip_count_.load(std::memory_order_acquire);
}

RuntimeQueueDepths ConcurrentTripRuntime::queue_depths() const {
  return impl_->queue_depths();
}

RuntimeObservationMetrics
ConcurrentTripRuntime::observation_metrics() const noexcept {
  return impl_->observation_metrics();
}

RuntimeExecutionMetrics
ConcurrentTripRuntime::execution_metrics() const noexcept {
  return impl_->execution_metrics();
}

}  // namespace liveroute::runtime
