#include "liveroute/runtime/event_coordinator.hpp"

#include <optional>
#include <utility>

namespace liveroute::runtime {

namespace {

[[nodiscard]] bool is_durable(domain::TripEventClass event_class) noexcept {
  return event_class == domain::TripEventClass::kDurable ||
         event_class == domain::TripEventClass::kCanonicalFirstDurableMirror ||
         event_class == domain::TripEventClass::kDurableCompareAndSwap;
}

[[nodiscard]] bool is_canonical_mirror(
    domain::TripEventClass event_class) noexcept {
  return event_class ==
         domain::TripEventClass::kCanonicalFirstDurableMirror;
}

[[nodiscard]] bool advances_trip_revision(
    const domain::TripEvent& event) noexcept {
  if (const auto* decision =
          std::get_if<domain::PlanDecisionEvent>(&event.payload)) {
    return decision->decision == domain::PlanDecision::kAccept;
  }
  return is_durable(*event.event_class());
}

[[nodiscard]] EventCoordinatorStaleReason map_stale_reason(
    VersionStaleReason reason) noexcept {
  switch (reason) {
    case VersionStaleReason::kNone:
      return EventCoordinatorStaleReason::kNone;
    case VersionStaleReason::kEpoch:
      return EventCoordinatorStaleReason::kEpoch;
    case VersionStaleReason::kMutationSequence:
      return EventCoordinatorStaleReason::kMutationSequence;
    case VersionStaleReason::kObservationSequence:
      return EventCoordinatorStaleReason::kObservationSequence;
    case VersionStaleReason::kTripRevision:
      return EventCoordinatorStaleReason::kTripRevision;
    case VersionStaleReason::kPlannerStateVersion:
      return EventCoordinatorStaleReason::kPlannerStateVersion;
  }
  return EventCoordinatorStaleReason::kNone;
}

[[nodiscard]] EventCoordinatorResult make_result(
    EventCoordinatorStatus status,
    const TripRuntimeVersionSnapshot& version_snapshot,
    EventCoordinatorStaleReason stale_reason =
        EventCoordinatorStaleReason::kNone,
    bool retryable = false) {
  return {.status = status,
          .stale_reason = stale_reason,
          .retryable = retryable,
          .planning_input_changed = false,
          .current_plan_changed = false,
          .version_snapshot = version_snapshot,
          .planning_seed = std::nullopt};
}

[[nodiscard]] EventCoordinatorResult map_version_result(
    VersionOperationResult result,
    const TripRuntimeVersionSnapshot& version_snapshot) noexcept {
  switch (result.status) {
    case VersionOperationStatus::kAccepted:
      return make_result(EventCoordinatorStatus::kAccepted, version_snapshot);
    case VersionOperationStatus::kDuplicate:
      return make_result(EventCoordinatorStatus::kDuplicate, version_snapshot);
    case VersionOperationStatus::kStale:
      return make_result(EventCoordinatorStatus::kStale,
                         version_snapshot,
                         map_stale_reason(result.stale_reason));
    case VersionOperationStatus::kInvalidArgument:
      return make_result(EventCoordinatorStatus::kInvalidArgument,
                         version_snapshot);
    case VersionOperationStatus::kInactive:
      return make_result(EventCoordinatorStatus::kInactive,
                         version_snapshot,
                         EventCoordinatorStaleReason::kNone, true);
  }
  return make_result(EventCoordinatorStatus::kInternal, version_snapshot);
}

}  // namespace

EventCoordinatorResult coordinate_event_admission(
    domain::TripState& state, TripRuntimeVersions& versions,
    const EventAdmissionRequest& request,
    std::size_t max_advisory_payload_bytes) {
  const auto event_class = request.event.event_class();
  if (!event_class.has_value()) {
    return make_result(EventCoordinatorStatus::kInvalidArgument,
                       versions.snapshot());
  }
  const bool durable = is_durable(*event_class);
  if ((durable &&
       (request.mutation_sequence == 0 ||
        request.observation_sequence != 0 ||
        request.expected_trip_revision == 0)) ||
      (!durable &&
       (request.observation_sequence == 0 ||
        request.mutation_sequence != 0 ||
        request.expected_trip_revision != 0))) {
    return make_result(EventCoordinatorStatus::kInvalidArgument,
                       versions.snapshot());
  }

  const auto preview =
      durable
          ? versions.preview_durable(
                request.runtime_epoch, request.mutation_sequence,
                request.expected_trip_revision,
                advances_trip_revision(request.event),
                request.expected_planner_state_version,
                *event_class != domain::TripEventClass::kAdvisory)
          : versions.preview_observation(
                request.runtime_epoch, request.observation_sequence,
                request.expected_planner_state_version,
                *event_class != domain::TripEventClass::kAdvisory);
  if (!preview.accepted()) {
    return map_version_result(preview, versions.snapshot());
  }

  auto candidate_state = state;
  const auto applied = domain::apply_trip_event(
      candidate_state, request.event, max_advisory_payload_bytes);
  if (!applied.accepted()) {
    if (durable && !is_canonical_mirror(*event_class)) {
      const auto resolved = versions.resolve_terminal_durable(
          request.runtime_epoch, request.mutation_sequence,
          request.expected_trip_revision,
          request.expected_planner_state_version);
      if (!resolved.accepted()) {
        return make_result(EventCoordinatorStatus::kInternal,
                           versions.snapshot());
      }
    }
    return make_result(
        applied.status == domain::TripStateApplyStatus::kStaleProposal
            ? EventCoordinatorStatus::kStale
            : EventCoordinatorStatus::kInvalidArgument,
        versions.snapshot(),
        applied.status == domain::TripStateApplyStatus::kStaleProposal
            ? EventCoordinatorStaleReason::kPlanProposal
            : EventCoordinatorStaleReason::kNone);
  }

  const auto committed =
      durable
          ? versions.accept_durable(
                request.runtime_epoch, request.mutation_sequence,
                request.expected_trip_revision,
                advances_trip_revision(request.event),
                request.expected_planner_state_version,
                applied.planning_input_changed)
          : versions.accept_observation(
                request.runtime_epoch, request.observation_sequence,
                request.expected_planner_state_version,
                applied.planning_input_changed);
  if (!committed.accepted()) {
    return make_result(EventCoordinatorStatus::kInternal,
                       versions.snapshot());
  }
  state = std::move(candidate_state);

  EventCoordinatorResult result{
      .status = EventCoordinatorStatus::kAccepted,
      .stale_reason = EventCoordinatorStaleReason::kNone,
      .retryable = false,
      .planning_input_changed = applied.planning_input_changed,
      .current_plan_changed = applied.current_plan_changed,
      .version_snapshot = versions.snapshot(),
      .planning_seed = std::nullopt,
  };
  if (applied.planning_input_changed) {
    const auto token = versions.capture_planning_work();
    if (!token.has_value()) {
      return make_result(EventCoordinatorStatus::kInternal,
                         versions.snapshot());
    }
    result.planning_seed =
        ImmutablePlanningSeed{.state = state,
                              .trigger = request.event.payload,
                              .source_versions = versions.snapshot(),
                              .work_token = *token};
  }
  return result;
}

}  // namespace liveroute::runtime
