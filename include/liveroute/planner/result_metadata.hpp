#pragma once

#include <cstdint>
#include <optional>
#include <vector>

#include "liveroute/domain/plan_proposal.hpp"
#include "liveroute/domain/trip_event.hpp"
#include "liveroute/planner/beam_search.hpp"

namespace liveroute::planner {

struct ReplanFacts {
  bool late_departure{};
  bool reservation_at_risk{};
  std::optional<std::int64_t> next_event_slack_ms;
};

struct ResultMetadata {
  domain::NotificationType notification;
  std::vector<domain::PlanReasonCode> reasons;
};

[[nodiscard]] std::optional<ReplanFacts> derive_replan_facts(
    const BeamSearchInput& input);

[[nodiscard]] std::vector<domain::PlanReasonCode> derive_causal_reasons(
    const domain::TripEventPayload& trigger, const ReplanFacts& facts);

[[nodiscard]] std::vector<domain::PlanReasonCode> derive_segment_reasons(
    const domain::TripEventPayload& trigger, const ReplanFacts& facts,
    bool segment_changed);

[[nodiscard]] ResultMetadata derive_result_metadata(
    const domain::TripEventPayload& trigger, const ReplanFacts& facts,
    BeamSearchOutcome outcome, bool proposal_changes_current_plan);

}  // namespace liveroute::planner
