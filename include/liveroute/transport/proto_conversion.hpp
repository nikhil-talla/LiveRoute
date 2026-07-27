#pragma once

#include <chrono>
#include <cstdint>
#include <optional>
#include <string>

#include "liveroute/domain/plan_proposal.hpp"
#include "liveroute/runtime/concurrent_trip_runtime.hpp"
#include "liveroute/v1/planner.pb.h"

namespace liveroute::transport {

struct ConversionError {
  std::string safe_message;
};

template <typename Value>
struct ConversionResult {
  std::optional<Value> value;
  std::optional<ConversionError> error;

  [[nodiscard]] explicit operator bool() const noexcept {
    return value.has_value();
  }
};

[[nodiscard]] std::optional<domain::TripId> parse_trip_id(
    const std::string& value);
[[nodiscard]] bool is_canonical_uuid(const std::string& value) noexcept;
[[nodiscard]] std::string format_trip_id(const domain::TripId& value);
[[nodiscard]] std::string format_plan_id(const domain::PlanId& value);

[[nodiscard]] ConversionResult<runtime::RuntimeBootstrapRequest>
bootstrap_from_proto(const ::liveroute::v1::PlannerStreamRequest& envelope,
                     std::uint64_t stream_binding);

[[nodiscard]] ConversionResult<runtime::RuntimeEventRequest> event_from_proto(
    const ::liveroute::v1::PlannerStreamRequest& envelope,
    std::chrono::system_clock::time_point system_now,
    std::chrono::steady_clock::time_point steady_now,
    std::chrono::milliseconds default_attempt_timeout,
    std::size_t max_candidates, std::size_t beam_width,
    std::size_t max_expansions);

void proposal_to_proto(const domain::StoredPlanProposal& source,
                       ::liveroute::v1::ReplanResult& destination);

[[nodiscard]] ConversionResult<::liveroute::v1::SnapshotBlob>
snapshot_to_proto(const domain::TripState& state,
                  const runtime::TripRuntimeVersionSnapshot& versions,
                  const std::string& owner_user_id);

[[nodiscard]] ::liveroute::v1::StatusCode status_to_proto(
    runtime::RuntimePlanningStatus status) noexcept;
[[nodiscard]] ::liveroute::v1::StatusCode status_to_proto(
    runtime::RuntimeControlStatus status) noexcept;
[[nodiscard]] ::liveroute::v1::StaleReason stale_reason_to_proto(
    runtime::VersionStaleReason reason) noexcept;
[[nodiscard]] ::liveroute::v1::StaleReason stale_reason_to_proto(
    runtime::EventCoordinatorStaleReason reason) noexcept;

void set_response_versions(
    const runtime::TripRuntimeVersionSnapshot& source,
    ::liveroute::v1::PlannerStreamResponse& destination);

}  // namespace liveroute::transport
