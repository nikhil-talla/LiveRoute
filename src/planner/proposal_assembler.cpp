#include "liveroute/planner/proposal_assembler.hpp"

#include <algorithm>
#include <cstddef>
#include <optional>
#include <utility>
#include <vector>

#include "liveroute/planner/candidate_itinerary.hpp"

namespace liveroute::planner {

namespace {

[[nodiscard]] const domain::Activity* activity_for_id(
    std::span<const domain::Activity> activities,
    const domain::ActivityId& activity_id) noexcept {
  for (const auto& activity : activities) {
    if (activity.activity_id == activity_id) return &activity;
  }
  return nullptr;
}

[[nodiscard]] std::optional<std::size_t> suffix_index_for_id(
    const BeamSearchInput& input,
    const domain::ActivityId& activity_id) noexcept {
  for (std::size_t index = 0; index < input.remaining_activities.size();
       ++index) {
    if (input.remaining_activities[index].activity.activity_id == activity_id) {
      return index;
    }
  }
  return std::nullopt;
}

[[nodiscard]] bool contains_id(
    std::span<const domain::ActivityId> ids,
    const domain::ActivityId& activity_id) noexcept {
  return std::find(ids.begin(), ids.end(), activity_id) != ids.end();
}

[[nodiscard]] std::vector<domain::ActivityId> common_scheduled_baseline(
    const BeamSearchInput& input,
    std::span<const domain::ActivityId> candidate_scheduled) {
  std::vector<domain::ActivityId> result;
  result.reserve(candidate_scheduled.size());
  for (const auto& activity : input.remaining_activities) {
    if (activity.current_plan_segment.state ==
            domain::PlanEntryState::kScheduled &&
        contains_id(candidate_scheduled, activity.activity.activity_id)) {
      result.push_back(activity.activity.activity_id);
    }
  }
  return result;
}

[[nodiscard]] std::vector<domain::ActivityId> common_scheduled_candidate(
    const BeamSearchInput& input,
    std::span<const domain::ActivityId> candidate_scheduled) {
  std::vector<domain::ActivityId> result;
  result.reserve(candidate_scheduled.size());
  for (const auto& activity_id : candidate_scheduled) {
    const auto suffix_index = suffix_index_for_id(input, activity_id);
    if (suffix_index &&
        input.remaining_activities[*suffix_index].current_plan_segment.state ==
            domain::PlanEntryState::kScheduled) {
      result.push_back(activity_id);
    }
  }
  return result;
}

[[nodiscard]] bool common_position_changed(
    const domain::ActivityId& activity_id,
    std::span<const domain::ActivityId> baseline,
    std::span<const domain::ActivityId> candidate) noexcept {
  const auto baseline_position =
      std::find(baseline.begin(), baseline.end(), activity_id);
  const auto candidate_position =
      std::find(candidate.begin(), candidate.end(), activity_id);
  return baseline_position != baseline.end() &&
         candidate_position != candidate.end() &&
         std::distance(baseline.begin(), baseline_position) !=
             std::distance(candidate.begin(), candidate_position);
}

[[nodiscard]] bool revised_segment_changed(
    const domain::CurrentPlanSegment& baseline,
    const domain::CurrentPlanSegment& candidate,
    std::span<const domain::ActivityId> common_baseline,
    std::span<const domain::ActivityId> common_candidate) noexcept {
  if (baseline.state != candidate.state) return true;
  if (baseline.state == domain::PlanEntryState::kOmitted) return false;
  return baseline.scheduled_start != candidate.scheduled_start ||
         baseline.scheduled_end != candidate.scheduled_end ||
         common_position_changed(candidate.activity_id, common_baseline,
                                 common_candidate);
}

[[nodiscard]] domain::ProposalSegment make_prefix_segment(
    const domain::CurrentPlanSegment& segment,
    const domain::Activity& activity) {
  return {
      .activity_id = segment.activity_id,
      .location = activity.location,
      .time_zone_name = activity.time_zone_name,
      .scheduled_start = segment.scheduled_start,
      .scheduled_end = segment.scheduled_end,
      .inbound_route = std::nullopt,
      .disposition =
          segment.state == domain::PlanEntryState::kOmitted
              ? domain::SegmentDisposition::kSkipped
              : domain::SegmentDisposition::kPreserved,
      .reasons = {},
  };
}

}  // namespace

std::optional<AssembledProposal> assemble_plan_proposal(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    std::span<const domain::Activity> activities,
    const ProposalSource& source, const domain::TripEventPayload& trigger,
    const ReplanFacts& facts) {
  const auto itinerary = reconstruct_candidate_itinerary(input, decisions);
  if (!itinerary ||
      activities.size() !=
          itinerary->preserved_prefix.size() +
              itinerary->revised_suffix.size()) {
    return std::nullopt;
  }

  domain::PlanProposal proposal{
      .proposal_id = source.proposal_id,
      .source_runtime_epoch = source.runtime_epoch,
      .source_planner_state_version = source.planner_state_version,
      .base_current_plan_id = source.base_current_plan_id,
      .source_trip_revision = source.trip_revision,
      .source_accepted_mutation_sequence =
          source.accepted_mutation_sequence,
      .preserved_prefix = {},
      .revised_suffix = {},
      .created_at = source.created_at,
  };
  proposal.preserved_prefix.reserve(itinerary->preserved_prefix.size());
  for (const auto& segment : itinerary->preserved_prefix) {
    const auto* activity = activity_for_id(activities, segment.activity_id);
    if (activity == nullptr) return std::nullopt;
    proposal.preserved_prefix.push_back(
        make_prefix_segment(segment, *activity));
  }

  std::vector<domain::ActivityId> candidate_scheduled;
  candidate_scheduled.reserve(itinerary->revised_suffix.size());
  for (const auto& segment : itinerary->revised_suffix) {
    if (segment.state == domain::PlanEntryState::kScheduled) {
      candidate_scheduled.push_back(segment.activity_id);
    }
  }
  const auto common_baseline =
      common_scheduled_baseline(input, candidate_scheduled);
  const auto common_candidate =
      common_scheduled_candidate(input, candidate_scheduled);

  bool changes_current_plan = false;
  std::size_t prior_matrix_index = 0;
  proposal.revised_suffix.reserve(itinerary->revised_suffix.size());
  for (const auto& segment : itinerary->revised_suffix) {
    const auto suffix_index = suffix_index_for_id(input, segment.activity_id);
    const auto* activity = activity_for_id(activities, segment.activity_id);
    if (!suffix_index || activity == nullptr) return std::nullopt;
    const auto& baseline =
        input.remaining_activities[*suffix_index].current_plan_segment;
    const bool changed =
        revised_segment_changed(baseline, segment, common_baseline,
                                common_candidate);
    changes_current_plan = changes_current_plan || changed;

    std::optional<domain::RouteEstimate> inbound_route;
    domain::SegmentDisposition disposition =
        domain::SegmentDisposition::kSkipped;
    if (segment.state == domain::PlanEntryState::kScheduled) {
      const auto matrix_index = *suffix_index + 1;
      inbound_route =
          input.travel_time_matrix->at(prior_matrix_index, matrix_index);
      prior_matrix_index = matrix_index;
      disposition =
          baseline.state == domain::PlanEntryState::kOmitted
              ? domain::SegmentDisposition::kAdded
              : changed ? domain::SegmentDisposition::kMoved
                        : domain::SegmentDisposition::kPreserved;
    }

    proposal.revised_suffix.push_back(
        {.activity_id = segment.activity_id,
         .location = activity->location,
         .time_zone_name = activity->time_zone_name,
         .scheduled_start = segment.scheduled_start,
         .scheduled_end = segment.scheduled_end,
         .inbound_route = std::move(inbound_route),
         .disposition = disposition,
         .reasons = derive_segment_reasons(trigger, facts, changed)});
  }

  if (!proposal.is_valid_for(activities)) return std::nullopt;
  return AssembledProposal{.proposal = std::move(proposal),
                           .changes_current_plan = changes_current_plan};
}

}  // namespace liveroute::planner
