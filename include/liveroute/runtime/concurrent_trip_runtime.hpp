#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>

#include "liveroute/domain/plan_proposal.hpp"
#include "liveroute/domain/trip_state.hpp"
#include "liveroute/planner/beam_search.hpp"
#include "liveroute/routing/travel_time_provider.hpp"
#include "liveroute/runtime/event_coordinator.hpp"
#include "liveroute/runtime/priority_lanes.hpp"

namespace liveroute::runtime {

struct ConcurrentRuntimeConfiguration {
  std::size_t shard_count{};
  std::size_t max_active_trips{};
  PriorityLaneCapacities shard_queue_capacities;
  std::size_t completion_queue_capacity{};
  std::size_t priority_fairness_burst{};
  std::size_t provider_workers{};
  std::size_t provider_queue_capacity{};
  std::size_t planner_workers{};
  std::size_t planner_queue_capacity{};
  std::size_t essential_response_capacity{};
  std::size_t max_advisory_payload_bytes{};

  [[nodiscard]] bool is_valid() const noexcept;
};

struct RuntimeBootstrapRequest {
  domain::TripState state;
  std::string owner_user_id;
  std::uint64_t runtime_epoch{};
  std::uint64_t trip_revision{};
  std::uint64_t finalized_mutation_sequence{};
  std::uint64_t current_observation_sequence{};
  std::optional<std::uint64_t> stream_binding;
};

struct RuntimePlanningContext {
  domain::UnixTimeMilliseconds current_time{0};
  domain::UnixTimeMilliseconds planning_horizon_start{0};
  domain::UnixTimeMilliseconds planning_horizon_end{0};
  domain::ProposalId proposal_id;
  domain::UnixTimeMilliseconds proposal_created_at{0};
  domain::Deadline deadline;
  std::size_t max_candidates{};
  std::size_t beam_width{};
  std::size_t max_expansions{};
  domain::RecoveryState recovery_state{domain::RecoveryState::kCurrent};

  [[nodiscard]] bool is_valid() const noexcept;
};

struct RuntimeEventRequest {
  domain::TripId trip_id;
  EventAdmissionRequest admission;
  std::optional<RuntimePlanningContext> planning;
};

enum class RuntimeBootstrapStatus : std::uint8_t {
  kAccepted,
  kDuplicate,
  kStale,
  kInvalidArgument,
  kResourceExhausted,
};

enum class RuntimeSubmissionStatus : std::uint8_t {
  kAccepted,
  kResponseCapacityFull,
  kShardQueueFull,
  kStopping,
};

enum class RuntimePlanningStatus : std::uint8_t {
  kOk,
  kNoNewProposal,
  kStale,
  kInvalidArgument,
  kResourceExhausted,
  kCancelled,
  kDeadlineExceeded,
  kProviderUnavailable,
  kMatrixTooLarge,
  kInfeasible,
  kInternal,
};

enum class RuntimeControlStatus : std::uint8_t {
  kOk,
  kDuplicate,
  kStale,
  kInvalidArgument,
  kInactive,
  kSnapshotNotReady,
};

struct RuntimeStageTimings {
  std::uint64_t queue_wait_microseconds{};
  std::uint64_t event_application_microseconds{};
  std::uint64_t provider_microseconds{};
  std::uint64_t matrix_conversion_microseconds{};
  std::uint64_t planner_microseconds{};
  std::uint64_t response_assembly_microseconds{};
  std::uint64_t total_microseconds{};
};

struct RuntimeQueueDepths {
  std::size_t critical{};
  std::size_t high{};
  std::size_t normal{};
  std::size_t advisory{};
  std::size_t completions{};
  std::size_t provider_jobs{};
  std::size_t planner_jobs{};
  std::size_t essential_responses_in_use{};

  friend bool operator==(const RuntimeQueueDepths&,
                         const RuntimeQueueDepths&) = default;
};

struct RuntimeObservationMetrics {
  std::uint64_t received_location_events{};
  std::uint64_t coalesced_location_replans{};
  std::uint64_t dropped_stale_location_events{};
  std::uint64_t replans_avoided{};

  friend bool operator==(const RuntimeObservationMetrics&,
                         const RuntimeObservationMetrics&) = default;
};

struct RuntimeExecutionMetrics {
  std::uint64_t planning_attempts_started{};
  std::uint64_t planning_attempts_completed{};
  std::uint64_t deadline_misses{};
  std::uint64_t cancelled_attempts{};
  std::uint64_t supersession_requests{};
  std::uint64_t provider_failures{};

  friend bool operator==(const RuntimeExecutionMetrics&,
                         const RuntimeExecutionMetrics&) = default;
};

struct RuntimeBootstrapResult {
  RuntimeBootstrapStatus status;
  TripRuntimeVersionSnapshot versions;
  std::optional<domain::PlanId> current_plan_id;
  std::optional<domain::StoredPlanProposal> retained_proposal;
};

struct RuntimeEventAcknowledgement {
  EventCoordinatorResult admission;
  RuntimeStageTimings timings;
};

struct RuntimePlanningDelivery {
  domain::TripId trip_id;
  std::optional<std::uint64_t> stream_binding;
  TripRuntimeVersionSnapshot versions;
  RuntimePlanningStatus status;
  bool retryable{};
  bool coalesced_replacement_pending{};
  RuntimeStageTimings timings;
  std::optional<domain::StoredPlanProposal> proposal;
};

struct RuntimeControlResult {
  RuntimeControlStatus status;
  VersionStaleReason stale_reason{VersionStaleReason::kNone};
  bool retryable{};
  TripRuntimeVersionSnapshot versions;
  std::optional<domain::TripState> snapshot_state;
  std::string owner_user_id;
};

// Callbacks are transport adapters: they must perform only a non-blocking
// bounded enqueue. Returning false from the proposal sink leaves the latest
// committed proposal retained in TripState for reconnect/bootstrap.
using BootstrapCallback = std::function<void(RuntimeBootstrapResult)>;
using EventAcknowledgementCallback =
    std::function<void(RuntimeEventAcknowledgement)>;
using ProposalSink = std::function<bool(RuntimePlanningDelivery)>;
using ControlCallback = std::function<void(RuntimeControlResult)>;

class ConcurrentTripRuntime {
 public:
  ConcurrentTripRuntime(ConcurrentRuntimeConfiguration configuration,
                        routing::TravelTimeProvider& travel_time_provider,
                        ProposalSink proposal_sink);
  ~ConcurrentTripRuntime();

  ConcurrentTripRuntime(const ConcurrentTripRuntime&) = delete;
  ConcurrentTripRuntime& operator=(const ConcurrentTripRuntime&) = delete;

  [[nodiscard]] RuntimeSubmissionStatus try_bootstrap(
      RuntimeBootstrapRequest request, BootstrapCallback callback);
  [[nodiscard]] RuntimeSubmissionStatus try_apply_event(
      RuntimeEventRequest request, EventAcknowledgementCallback callback);
  [[nodiscard]] RuntimeSubmissionStatus try_confirm_finalized(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      std::uint64_t finalized_mutation_sequence, ControlCallback callback);
  [[nodiscard]] RuntimeSubmissionStatus try_snapshot(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      std::uint64_t minimum_finalized_mutation_sequence,
      std::uint64_t minimum_planner_state_version, ControlCallback callback);
  [[nodiscard]] RuntimeSubmissionStatus try_deactivate(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      bool final_snapshot_required, ControlCallback callback);
  [[nodiscard]] RuntimeSubmissionStatus try_abort_deactivation(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      ControlCallback callback);

  // Removes only the matching transport binding and cooperatively cancels
  // replaceable work. Trip state and retained committed proposals remain
  // shard-owned and active.
  [[nodiscard]] RuntimeSubmissionStatus try_unbind_stream(
      const domain::TripId& trip_id, std::uint64_t runtime_epoch,
      std::uint64_t stream_binding);

  void stop_accepting() noexcept;
  [[nodiscard]] bool is_accepting() const noexcept;
  [[nodiscard]] std::size_t active_trip_count() const noexcept;
  [[nodiscard]] RuntimeQueueDepths queue_depths() const;
  [[nodiscard]] RuntimeObservationMetrics observation_metrics() const noexcept;
  [[nodiscard]] RuntimeExecutionMetrics execution_metrics() const noexcept;

 private:
  class Impl;
  std::unique_ptr<Impl> impl_;
};

}  // namespace liveroute::runtime
