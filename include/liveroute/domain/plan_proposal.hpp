#pragma once

#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <vector>

#include "liveroute/domain/activity.hpp"
#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/domain/types.hpp"

namespace liveroute::domain {

enum class SegmentDisposition : std::uint8_t {
  kPreserved = 1,
  kMoved = 2,
  kShortened = 3,
  kSkipped = 4,
  kAdded = 5,
};

enum class NotificationType : std::uint8_t {
  kNone = 1,
  kLowSlackWarning = 2,
  kCriticalLateness = 3,
  kPlanChangeSuggested = 4,
  kInfeasibleSchedule = 5,
};

enum class PlanReasonCode : std::uint8_t {
  kLateDeparture = 1,
  kActivityDelay = 2,
  kRouteDeviation = 3,
  kHoursChanged = 4,
  kPlaceClosed = 5,
  kReservationAtRisk = 6,
  kTravelDelay = 7,
  kUserEdit = 8,
  kDeadlineBudget = 9,
  kNoFeasiblePlan = 10,
};

enum class PlanQuality : std::uint8_t {
  kComplete = 1,
  kBestSoFar = 2,
  kNoNewProposal = 3,
};

enum class RoutingQuality : std::uint8_t {
  kFresh = 1,
  kStaleCache = 2,
  kUnavailable = 3,
};

enum class RecoveryState : std::uint8_t {
  kCurrent = 1,
  kNotAdvancing = 2,
};

struct ProposalSegment {
  ActivityId activity_id;
  Location location;
  std::string time_zone_name;
  std::optional<UnixTimeMilliseconds> scheduled_start;
  std::optional<UnixTimeMilliseconds> scheduled_end;
  std::optional<RouteEstimate> inbound_route;
  SegmentDisposition disposition;
  std::vector<PlanReasonCode> reasons;

  [[nodiscard]] bool is_valid_for(const Activity& activity) const noexcept;
};

struct PlanProposal {
  ProposalId proposal_id;
  std::uint64_t source_runtime_epoch{};
  PlannerStateVersion source_planner_state_version{0};
  PlanId base_current_plan_id;
  TripRevision source_trip_revision{0};
  MutationSequence source_accepted_mutation_sequence{0};
  std::vector<ProposalSegment> preserved_prefix;
  std::vector<ProposalSegment> revised_suffix;
  UnixTimeMilliseconds created_at{0};

  [[nodiscard]] bool is_valid_for(
      std::span<const Activity> activities) const;
};

struct PlannerStats {
  std::uint64_t candidates_evaluated{};
  std::uint64_t candidates_pruned{};
  std::uint32_t search_depth{};
  std::uint32_t queue_wait_microseconds{};
  std::uint32_t provider_microseconds{};
  std::uint32_t planner_microseconds{};
  std::uint32_t serialization_microseconds{};
  bool deadline_hit{};

  [[nodiscard]] bool is_valid() const noexcept {
    return search_depth <= 64;
  }
};

struct ResultQuality {
  PlanQuality plan_quality;
  RoutingQuality routing_quality;
  RecoveryState recovery_state;

  [[nodiscard]] bool is_valid() const noexcept;
};

struct StoredPlanProposal {
  PlanProposal proposal;
  NotificationType notification;
  std::vector<PlanReasonCode> reasons;
  PlannerStats stats;
  ResultQuality quality;

  [[nodiscard]] bool is_valid_for(
      std::span<const Activity> activities) const;
};

}  // namespace liveroute::domain
