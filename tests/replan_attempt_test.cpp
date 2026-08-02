#include "liveroute/planner/replan_attempt.hpp"

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <vector>

namespace {

using namespace liveroute::domain;
using namespace liveroute::planner;

template <typename Id>
Id id(std::uint8_t marker) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = static_cast<std::byte>(marker);
  return Id{bytes};
}

Activity activity() {
  return {
      .activity_id = id<ActivityId>(1),
      .place_id = PlaceId{"place"},
      .display_name = "Place",
      .location = Location{40, -74},
      .time_zone_name = "America/New_York",
      .inbound_travel_mode = TravelMode::kWalking,
      .activity_class = ActivityClass::kFlexible,
      .activity_state = ActivityState::kPlanned,
      .priority_rank = 0,
      .utility_score = 1,
      .timing = ActivityTiming{
          .open_windows = {{UnixTimeMilliseconds{0},
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
      .found_closed_at = std::nullopt,
  };
}

ReplanBudget budget(std::size_t max_expansions = 100,
                    std::size_t max_candidates = 100) {
  return {.deadline = std::chrono::steady_clock::now() + std::chrono::minutes{1},
          .max_candidates = max_candidates,
          .beam_width = 8,
          .max_expansions = max_expansions,
          .stop_token = {}};
}

ProposalSource source(const CurrentPlan& plan) {
  return {.proposal_id = id<ProposalId>(2),
          .runtime_epoch = 1,
          .planner_state_version = PlannerStateVersion{3},
          .base_current_plan_id = plan.plan_id,
          .trip_revision = TripRevision{1},
          .accepted_mutation_sequence = MutationSequence{1},
          .created_at = UnixTimeMilliseconds{10}};
}

}  // namespace

int main() {
  const auto place = activity();
  const CurrentPlanSegment baseline{
      .activity_id = place.activity_id,
      .state = PlanEntryState::kOmitted,
      .scheduled_start = std::nullopt,
      .scheduled_end = std::nullopt};
  const CurrentPlan plan{
      .plan_id = id<PlanId>(3),
      .plan_revision = 1,
      .origin = PlanOrigin::kUserAuthored,
      .segments = {baseline},
      .created_at = UnixTimeMilliseconds{0},
      .source_proposal_id = std::nullopt};
  const TravelTimeMatrix matrix{
      2,
      {{std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true},
       {std::chrono::seconds{0}, 0, true}}};
  const BeamSearchInput input{
      .current_time = UnixTimeMilliseconds{0},
      .planning_horizon_start = UnixTimeMilliseconds{0},
      .planning_horizon_end = UnixTimeMilliseconds{10000},
      .preserved_prefix = {},
      .remaining_activities = {{.activity = place,
                                .original_trip_ordinal = 0,
                                .current_plan_segment = baseline}},
      .travel_time_matrix = &matrix};
  const TripEventPayload trigger =
      ActivityDelayed{place.activity_id, 5};

  const auto result = run_replan_attempt(
      input, std::span<const Activity>{&place, 1}, source(plan), trigger,
      ReplanFacts{}, budget());
  if (result.search.outcome != BeamSearchOutcome::kComplete ||
      !result.proposal.has_value() ||
      !result.proposal->changes_current_plan ||
      result.proposal->proposal.revised_suffix.size() != 1 ||
      result.proposal->proposal.revised_suffix.front().disposition !=
          SegmentDisposition::kAdded ||
      result.metadata.notification != NotificationType::kPlanChangeSuggested ||
      result.metadata.reasons !=
          std::vector<PlanReasonCode>{PlanReasonCode::kActivityDelay}) {
    return 1;
  }

  const PlannerStats stats{.candidates_evaluated = result.search.candidate_count,
                           .candidates_pruned = 0,
                           .search_depth = 1,
                           .queue_wait_microseconds = 2,
                           .provider_microseconds = 3,
                           .planner_microseconds = 4,
                           .serialization_microseconds = 5,
                           .deadline_hit = false};
  const auto stored = assemble_stored_plan_proposal(
      result, std::span<const Activity>{&place, 1}, stats,
      RoutingQuality::kFresh, RecoveryState::kCurrent);
  if (!stored || !stored->is_valid_for(std::span<const Activity>{&place, 1}) ||
      stored->quality.plan_quality != PlanQuality::kComplete ||
      stored->quality.routing_quality != RoutingQuality::kFresh ||
      stored->stats.planner_microseconds != 4) {
    return 1;
  }

  PlannerScratch reusable_scratch;
  const auto reused = run_replan_attempt(
      input, std::span<const Activity>{&place, 1}, source(plan), trigger,
      ReplanFacts{}, budget(), reusable_scratch);
  if (reused.search.outcome != result.search.outcome ||
      !reused.proposal.has_value() ||
      reused.proposal->proposal.revised_suffix.size() !=
          result.proposal->proposal.revised_suffix.size() ||
      reused.proposal->changes_current_plan !=
          result.proposal->changes_current_plan) {
    return 1;
  }

  const auto limited = run_replan_attempt(
      input, std::span<const Activity>{&place, 1}, source(plan), trigger,
      ReplanFacts{}, budget(100, 1));
  if (limited.search.outcome != BeamSearchOutcome::kBestSoFar ||
      !limited.proposal.has_value()) {
    return 1;
  }
  const auto best_stored = assemble_stored_plan_proposal(
      limited, std::span<const Activity>{&place, 1}, stats,
      RoutingQuality::kStaleCache, RecoveryState::kNotAdvancing);
  if (!best_stored ||
      best_stored->quality.plan_quality != PlanQuality::kBestSoFar ||
      best_stored->quality.routing_quality != RoutingQuality::kStaleCache ||
      best_stored->quality.recovery_state != RecoveryState::kNotAdvancing) {
    return 1;
  }

  return 0;
}
