#pragma once

#include <cstdint>

namespace liveroute::runtime {

// These values are mutated only by a trip's owner shard. They deliberately
// contain no transport or persistence types; adapters translate their result
// into the public protocol status and stale reason.
enum class VersionOperationStatus : std::uint8_t {
  kAccepted,
  kDuplicate,
  kStale,
  kInvalidArgument,
  kInactive,
};

enum class VersionStaleReason : std::uint8_t {
  kNone,
  kEpoch,
  kMutationSequence,
  kObservationSequence,
  kTripRevision,
};

struct VersionOperationResult {
  VersionOperationStatus status;
  VersionStaleReason stale_reason{VersionStaleReason::kNone};

  [[nodiscard]] constexpr bool accepted() const noexcept {
    return status == VersionOperationStatus::kAccepted;
  }
};

struct TripRuntimeVersionSnapshot {
  std::uint64_t runtime_epoch{};
  std::uint64_t trip_revision{};
  std::uint64_t planner_state_version{};
  std::uint64_t planning_generation{};
  std::uint64_t accepted_mutation_sequence{};
  std::uint64_t finalized_mutation_sequence{};
  std::uint64_t accepted_observation_sequence{};
};

// Owns the protocol-independent sequencing portion of one active trip. A
// successful bootstrap contains a fully finalized durable base, so accepted
// and finalized mutation watermarks begin equal.
class TripRuntimeVersions {
 public:
  [[nodiscard]] VersionOperationResult bootstrap(
      std::uint64_t runtime_epoch, std::uint64_t trip_revision,
      std::uint64_t finalized_mutation_sequence,
      std::uint64_t current_observation_sequence);

  [[nodiscard]] VersionOperationResult accept_durable(
      std::uint64_t runtime_epoch, std::uint64_t mutation_sequence,
      std::uint64_t expected_trip_revision, bool advances_trip_revision);

  [[nodiscard]] VersionOperationResult accept_observation(
      std::uint64_t runtime_epoch, std::uint64_t observation_sequence);

  [[nodiscard]] VersionOperationResult confirm_finalized(
      std::uint64_t runtime_epoch, std::uint64_t finalized_mutation_sequence);

  [[nodiscard]] bool snapshot_ready() const noexcept;
  [[nodiscard]] bool is_active() const noexcept { return active_; }
  [[nodiscard]] const TripRuntimeVersionSnapshot& snapshot() const noexcept {
    return snapshot_;
  }

 private:
  [[nodiscard]] VersionOperationResult validate_epoch(
      std::uint64_t runtime_epoch) const noexcept;

  bool active_{};
  TripRuntimeVersionSnapshot snapshot_{};
};

}  // namespace liveroute::runtime
