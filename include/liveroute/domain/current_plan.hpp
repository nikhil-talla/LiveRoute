#pragma once

#include <cstdint>
#include <optional>
#include <span>
#include <vector>

#include "liveroute/domain/types.hpp"

namespace liveroute::domain {

enum class PlanOrigin : std::uint8_t {
  kUserAuthored,
  kAcceptedEngineProposal,
};

enum class PlanEntryState : std::uint8_t {
  kScheduled,
  kOmitted,
};

struct CurrentPlanSegment {
  ActivityId activity_id;
  PlanEntryState state;
  std::optional<UnixTimeMilliseconds> scheduled_start;
  std::optional<UnixTimeMilliseconds> scheduled_end;

  [[nodiscard]] bool is_valid() const noexcept;
};

// The user-authoritative plan has no route/provider/planner fields. Its
// structural validation deliberately does not assess feasibility.
struct CurrentPlan {
  PlanId plan_id;
  std::uint64_t plan_revision{};
  PlanOrigin origin;
  std::vector<CurrentPlanSegment> segments;
  UnixTimeMilliseconds created_at{0};
  std::optional<ProposalId> source_proposal_id;

  [[nodiscard]] bool is_valid_for(
      std::span<const ActivityId> activity_ids) const;
};

}  // namespace liveroute::domain
