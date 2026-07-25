#include "liveroute/planner/beam_search.hpp"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <limits>
#include <optional>
#include <span>
#include <utility>
#include <vector>

#include "liveroute/planner/checked_int.hpp"

namespace liveroute::planner {

namespace {

using domain::ActivityTiming;
using domain::TimeWindow;
using domain::UnixTimeMilliseconds;

[[nodiscard]] std::optional<std::int64_t> seconds_to_milliseconds(
    std::uint32_t seconds) noexcept {
  return checked_milliseconds(seconds);
}

[[nodiscard]] bool is_start_in_reservation_range(
    const ActivityTiming& timing, std::int64_t start) noexcept {
  if (!timing.reservation_start.has_value()) return true;
  const auto grace_ms = seconds_to_milliseconds(timing.reservation_grace_seconds);
  const auto reservation_end =
      grace_ms ? checked_add(timing.reservation_start->value(), *grace_ms)
               : std::nullopt;
  return reservation_end.has_value() && start >= timing.reservation_start->value() &&
         start <= *reservation_end;
}

[[nodiscard]] const TimeWindow* containing_window(const ActivityTiming& timing,
                                                   std::int64_t start) noexcept {
  for (const auto& window : timing.open_windows) {
    if (start >= window.opens_at.value() && start < window.closes_at.value()) {
      return &window;
    }
  }
  return nullptr;
}

[[nodiscard]] bool is_locally_hard_feasible(
    const BeamSearchInput& input, const PlanningActivity& planning_activity,
    std::int64_t arrival, std::int64_t start, std::int64_t end) noexcept {
  const auto& activity = planning_activity.activity;
  const auto& timing = activity.timing;
  if (activity.found_closed_at.has_value() || start >= end || start < arrival ||
      start < input.planning_horizon_start.value() ||
      end > input.planning_horizon_end.value() ||
      !is_start_in_reservation_range(timing, start) ||
      (timing.mandatory_deadline.has_value() &&
       end > timing.mandatory_deadline->value())) {
    return false;
  }

  const auto duration_ms = checked_subtract(end, start);
  const auto min_duration_ms = seconds_to_milliseconds(timing.min_duration_seconds);
  const auto preferred_duration_ms =
      seconds_to_milliseconds(timing.preferred_duration_seconds);
  const auto max_duration_ms = seconds_to_milliseconds(timing.max_duration_seconds);
  if (!duration_ms || !min_duration_ms || !preferred_duration_ms ||
      !max_duration_ms || *duration_ms < *min_duration_ms ||
      *duration_ms > *max_duration_ms ||
      (!timing.can_shorten && *duration_ms < *preferred_duration_ms)) {
    return false;
  }

  const auto* window = containing_window(timing, start);
  return window != nullptr && end <= window->closes_at.value();
}

[[nodiscard]] bool has_positive_legal_room(
    const BeamSearchInput& input, const PlanningActivity& planning_activity,
    const TimeWindow& window, std::int64_t start) noexcept {
  if (!is_start_in_reservation_range(planning_activity.activity.timing, start) ||
      start < input.planning_horizon_start.value() ||
      start >= input.planning_horizon_end.value()) {
    return false;
  }
  std::int64_t latest_end =
      std::min(window.closes_at.value(), input.planning_horizon_end.value());
  if (planning_activity.activity.timing.mandatory_deadline.has_value()) {
    latest_end = std::min(latest_end,
                          planning_activity.activity.timing.mandatory_deadline->value());
  }
  return start < latest_end;
}

void add_generated_durations(const BeamSearchInput& input,
                             const PlanningActivity& planning_activity,
                             std::int64_t arrival, const TimeWindow& window,
                             std::int64_t start,
                             std::vector<CandidateAlternative>* alternatives) {
  const auto& timing = planning_activity.activity.timing;
  if (!has_positive_legal_room(input, planning_activity, window, start)) return;

  std::int64_t latest_end =
      std::min(window.closes_at.value(), input.planning_horizon_end.value());
  if (timing.mandatory_deadline.has_value()) {
    latest_end = std::min(latest_end, timing.mandatory_deadline->value());
  }
  const auto remaining_ms = checked_subtract(latest_end, start);
  const auto max_duration_ms = seconds_to_milliseconds(timing.max_duration_seconds);
  if (!remaining_ms || !max_duration_ms) return;

  const auto remaining_seconds = *remaining_ms / 1000;
  const auto finish_cap_seconds =
      std::min(remaining_seconds, *max_duration_ms / 1000);
  const std::int64_t minimum_positive_seconds =
      std::max<std::int64_t>(1, timing.min_duration_seconds);
  if (finish_cap_seconds < minimum_positive_seconds) return;

  std::vector<std::int64_t> durations_seconds;
  if (!timing.can_shorten) {
    if (timing.preferred_duration_seconds > 0 &&
        timing.preferred_duration_seconds <= finish_cap_seconds) {
      durations_seconds.push_back(timing.preferred_duration_seconds);
    }
  } else {
    const auto preferred_or_cap = std::min<std::int64_t>(
        std::max<std::int64_t>(1, timing.preferred_duration_seconds),
        finish_cap_seconds);
    if (preferred_or_cap >= minimum_positive_seconds) {
      durations_seconds.push_back(preferred_or_cap);
    }
    if (minimum_positive_seconds != preferred_or_cap) {
      durations_seconds.push_back(minimum_positive_seconds);
    }
  }

  for (const auto duration_seconds : durations_seconds) {
    const auto duration_ms = checked_milliseconds(
        static_cast<std::uint32_t>(duration_seconds));
    const auto end = duration_ms ? checked_add(start, *duration_ms) : std::nullopt;
    if (!end || !is_locally_hard_feasible(input, planning_activity, arrival, start,
                                           *end)) {
      continue;
    }
    alternatives->push_back({.kind = CandidateAlternativeKind::kScheduled,
                             .activity_ordinal = planning_activity.original_trip_ordinal,
                             .start = UnixTimeMilliseconds{start},
                             .end = UnixTimeMilliseconds{*end},
                             .is_exact_current_plan = false});
  }
}

void add_exact_current_plan(const BeamSearchInput& input,
                            const PlanningActivity& planning_activity,
                            std::int64_t arrival,
                            std::vector<CandidateAlternative>* alternatives) {
  const auto& segment = planning_activity.current_plan_segment;
  if (segment.state != domain::PlanEntryState::kScheduled) return;
  const auto start = segment.scheduled_start->value();
  const auto end = segment.scheduled_end->value();
  if (!is_locally_hard_feasible(input, planning_activity, arrival, start, end)) return;
  alternatives->push_back({.kind = CandidateAlternativeKind::kScheduled,
                           .activity_ordinal = planning_activity.original_trip_ordinal,
                           .start = UnixTimeMilliseconds{start},
                           .end = UnixTimeMilliseconds{end},
                           .is_exact_current_plan = true});
}

[[nodiscard]] std::optional<std::int64_t> route_duration_milliseconds(
    const domain::RouteEstimate& estimate) noexcept {
  const auto seconds = estimate.duration.count();
  constexpr auto kMillisecondsPerSecond = std::int64_t{1000};
  if (!estimate.reachable || seconds < 0 ||
      seconds > std::numeric_limits<std::int64_t>::max() /
                    kMillisecondsPerSecond) {
    return std::nullopt;
  }
  return seconds * kMillisecondsPerSecond;
}

[[nodiscard]] bool checked_accumulate(std::int64_t value,
                                      std::int64_t* total) noexcept {
  const auto result = checked_add(*total, value);
  if (!result) return false;
  *total = *result;
  return true;
}

[[nodiscard]] std::optional<std::size_t> activity_index_for_ordinal(
    const BeamSearchInput& input, std::size_t ordinal) noexcept {
  for (std::size_t index = 0; index < input.remaining_activities.size(); ++index) {
    if (input.remaining_activities[index].original_trip_ordinal == ordinal) {
      return index;
    }
  }
  return std::nullopt;
}

[[nodiscard]] bool contains_scheduled_alternative(
    std::span<const CandidateAlternative> alternatives,
    const ExpansionDecision& decision) noexcept {
  return std::any_of(
      alternatives.begin(), alternatives.end(),
      [&decision](const CandidateAlternative& alternative) {
        return alternative.kind == CandidateAlternativeKind::kScheduled &&
               alternative.activity_ordinal == decision.activity_ordinal &&
               alternative.start.value() == decision.start_unix_ms &&
               alternative.end.value() == decision.end_unix_ms;
      });
}

struct BeamNode {
  std::vector<ExpansionDecision> decisions;
  CandidateScore score;
};

[[nodiscard]] std::optional<std::pair<std::size_t, std::int64_t>>
last_scheduled_state(const BeamSearchInput& input,
                     std::span<const ExpansionDecision> decisions) noexcept {
  for (auto decision = decisions.rbegin(); decision != decisions.rend();
       ++decision) {
    if (decision->decision != 0) continue;
    const auto index =
        activity_index_for_ordinal(input, decision->activity_ordinal);
    if (!index) return std::nullopt;
    return std::pair<std::size_t, std::int64_t>{*index,
                                                decision->end_unix_ms};
  }
  return std::pair<std::size_t, std::int64_t>{
      std::numeric_limits<std::size_t>::max(), input.current_time.value()};
}

[[nodiscard]] std::optional<UnixTimeMilliseconds> actual_arrival(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    std::size_t destination_activity_index) noexcept {
  const auto last = last_scheduled_state(input, decisions);
  if (!last) return std::nullopt;
  const auto origin_matrix_index =
      last->first == std::numeric_limits<std::size_t>::max()
          ? std::size_t{0}
          : last->first + 1;
  const auto& route = input.travel_time_matrix->at(
      origin_matrix_index, destination_activity_index + 1);
  const auto travel_ms = route_duration_milliseconds(route);
  const auto arrival =
      travel_ms ? checked_add(last->second, *travel_ms) : std::nullopt;
  return arrival ? std::optional<UnixTimeMilliseconds>{
                       UnixTimeMilliseconds{*arrival}}
                 : std::nullopt;
}

[[nodiscard]] bool passes_protected_activity_lower_bound(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions) {
  std::vector<bool> decided(input.remaining_activities.size(), false);
  for (const auto& decision : decisions) {
    const auto index =
        activity_index_for_ordinal(input, decision.activity_ordinal);
    if (!index) return false;
    decided[*index] = true;
  }

  const auto last = last_scheduled_state(input, decisions);
  if (!last) return false;
  const auto optimistic_arrival = UnixTimeMilliseconds{last->second};
  for (std::size_t index = 0; index < input.remaining_activities.size();
       ++index) {
    if (decided[index]) continue;
    const auto& activity = input.remaining_activities[index];
    if (!activity.activity.timing.mandatory &&
        activity.activity.timing.can_skip) {
      continue;
    }
    const auto alternatives =
        generate_candidate_alternatives(input, activity, optimistic_arrival);
    if (std::none_of(
            alternatives.begin(), alternatives.end(),
            [](const CandidateAlternative& alternative) {
              return alternative.kind ==
                     CandidateAlternativeKind::kScheduled;
            })) {
      return false;
    }
  }
  return true;
}

[[nodiscard]] ExpansionDecision expansion_decision(
    const CandidateAlternative& alternative) noexcept {
  return {
      .activity_ordinal = alternative.activity_ordinal,
      .decision = static_cast<std::uint8_t>(
          alternative.kind == CandidateAlternativeKind::kScheduled ? 0 : 1),
      .start_unix_ms = alternative.start.value(),
      .end_unix_ms = alternative.end.value(),
  };
}

[[nodiscard]] BeamSearchResult interrupted_result(
    BeamSearchOutcome outcome, const std::optional<BeamNode>& best,
    std::size_t expansion_count, std::size_t candidate_count,
    bool search_was_truncated) {
  if (best.has_value()) {
    return {
        .outcome = BeamSearchOutcome::kBestSoFar,
        .best_decisions = best->decisions,
        .best_score = best->score,
        .expansion_count = expansion_count,
        .candidate_count = candidate_count,
        .search_was_truncated = search_was_truncated,
    };
  }
  return {
      .outcome = outcome,
      .best_decisions = std::nullopt,
      .best_score = std::nullopt,
      .expansion_count = expansion_count,
      .candidate_count = candidate_count,
      .search_was_truncated = search_was_truncated,
  };
}

}  // namespace

bool BeamSearchInput::is_valid() const noexcept {
  if (planning_horizon_start >= planning_horizon_end ||
      current_time < planning_horizon_start || current_time > planning_horizon_end ||
      remaining_activities.size() > 64 || travel_time_matrix == nullptr ||
      travel_time_matrix->location_count() != remaining_activities.size() + 1) {
    return false;
  }
  std::vector<std::size_t> ordinals;
  ordinals.reserve(remaining_activities.size());
  for (const auto& activity : remaining_activities) {
    if (!activity.is_valid()) return false;
    ordinals.push_back(activity.original_trip_ordinal);
  }
  std::sort(ordinals.begin(), ordinals.end());
  return std::adjacent_find(ordinals.begin(), ordinals.end()) == ordinals.end();
}

std::vector<CandidateAlternative> generate_candidate_alternatives(
    const BeamSearchInput& input, const PlanningActivity& planning_activity,
    UnixTimeMilliseconds arrival) {
  std::vector<CandidateAlternative> alternatives;
  if (!input.is_valid() || !planning_activity.is_valid()) return alternatives;

  const auto& activity = planning_activity.activity;
  const auto& timing = activity.timing;
  const auto& segment = planning_activity.current_plan_segment;
  const bool has_current_interval =
      segment.state == domain::PlanEntryState::kScheduled;
  const bool must_use_current_interval =
      !timing.can_move || (activity.activity_class == domain::ActivityClass::kFixed &&
                           has_current_interval);

  if (must_use_current_interval) {
    add_exact_current_plan(input, planning_activity, arrival.value(), &alternatives);
  } else if (activity.activity_class != domain::ActivityClass::kFixed ||
             timing.reservation_start.has_value()) {
    for (const auto& window : timing.open_windows) {
      std::int64_t earliest_start = std::max(arrival.value(), window.opens_at.value());
      if (timing.reservation_start.has_value()) {
        earliest_start = std::max(earliest_start, timing.reservation_start->value());
      }
      if (has_positive_legal_room(input, planning_activity, window, earliest_start)) {
        add_generated_durations(input, planning_activity, arrival.value(), window,
                                earliest_start, &alternatives);
      }

      if (activity.activity_class != domain::ActivityClass::kFixed &&
          has_current_interval) {
        const auto current_start = segment.scheduled_start->value();
        if (current_start != earliest_start && current_start >= arrival.value() &&
            current_start >= window.opens_at.value() &&
            current_start < window.closes_at.value() &&
            has_positive_legal_room(input, planning_activity, window, current_start)) {
          add_generated_durations(input, planning_activity, arrival.value(), window,
                                  current_start, &alternatives);
        }
      }
    }
    if (activity.activity_class != domain::ActivityClass::kFixed && has_current_interval) {
      add_exact_current_plan(input, planning_activity, arrival.value(), &alternatives);
    }
  }

  std::sort(alternatives.begin(), alternatives.end(),
            [](const CandidateAlternative& left, const CandidateAlternative& right) {
              if (left.start != right.start) return left.start < right.start;
              if (left.is_exact_current_plan != right.is_exact_current_plan) {
                return left.is_exact_current_plan;
              }
              return left.end > right.end;
            });
  alternatives.erase(
      std::unique(alternatives.begin(), alternatives.end(),
                  [](const CandidateAlternative& left,
                     const CandidateAlternative& right) {
                    return left.kind == right.kind &&
                           left.activity_ordinal == right.activity_ordinal &&
                           left.start == right.start && left.end == right.end;
                  }),
      alternatives.end());

  if (!timing.mandatory && timing.can_skip) {
    alternatives.push_back({.kind = CandidateAlternativeKind::kSkipped,
                            .activity_ordinal = planning_activity.original_trip_ordinal,
                            .start = UnixTimeMilliseconds{0},
                            .end = UnixTimeMilliseconds{0},
                            .is_exact_current_plan = false});
  }
  return alternatives;
}

std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions) {
  if (!input.is_valid() || decisions.size() > input.remaining_activities.size()) {
    return std::nullopt;
  }

  const auto activity_count = input.remaining_activities.size();
  std::vector<bool> decided(activity_count, false);
  std::vector<bool> changed(activity_count, false);
  std::vector<bool> common_scheduled(activity_count, false);
  std::vector<std::size_t> candidate_common_order;
  candidate_common_order.reserve(decisions.size());

  std::vector<std::int32_t> priority_ranks;
  priority_ranks.reserve(activity_count);
  std::int64_t optimistic_utility = 0;
  for (const auto& planning_activity : input.remaining_activities) {
    priority_ranks.push_back(planning_activity.activity.priority_rank);
    if (!checked_accumulate(planning_activity.activity.utility_score,
                            &optimistic_utility)) {
      return std::nullopt;
    }
  }
  std::sort(priority_ranks.begin(), priority_ranks.end());
  priority_ranks.erase(
      std::unique(priority_ranks.begin(), priority_ranks.end()),
      priority_ranks.end());

  CandidateScore score{
      .skips_by_priority =
          std::vector<std::uint32_t>(priority_ranks.size(), 0),
      .scheduled_utility = optimistic_utility,
      .total_lateness_ms = 0,
      .total_preferred_shortfall_ms = 0,
      .total_travel_ms = 0,
      .changed_activity_count = 0,
      .total_start_shift_ms = 0,
      .final_scheduled_end_unix_ms = input.current_time.value(),
      .canonical_plan_key = {},
  };
  score.canonical_plan_key.scheduled_ordinals_in_order.reserve(decisions.size());
  score.canonical_plan_key.scheduled_entries.reserve(decisions.size());
  score.canonical_plan_key.skipped_ordinals.reserve(decisions.size());

  std::optional<std::size_t> prior_scheduled_activity_index;
  std::int64_t prior_scheduled_end = input.current_time.value();

  for (const auto& decision : decisions) {
    const auto activity_index =
        activity_index_for_ordinal(input, decision.activity_ordinal);
    if (!activity_index || decided[*activity_index] || decision.decision > 1) {
      return std::nullopt;
    }
    decided[*activity_index] = true;

    const auto& planning_activity = input.remaining_activities[*activity_index];
    const auto& activity = planning_activity.activity;
    const auto& baseline = planning_activity.current_plan_segment;

    if (decision.decision == 1) {
      if (decision.start_unix_ms != 0 || decision.end_unix_ms != 0 ||
          activity.timing.mandatory || !activity.timing.can_skip) {
        return std::nullopt;
      }
      const auto rank_position = std::lower_bound(
          priority_ranks.begin(), priority_ranks.end(), activity.priority_rank);
      if (rank_position == priority_ranks.end() ||
          *rank_position != activity.priority_rank) {
        return std::nullopt;
      }
      auto& skip_count = score.skips_by_priority[static_cast<std::size_t>(
          std::distance(priority_ranks.begin(), rank_position))];
      if (skip_count == std::numeric_limits<std::uint32_t>::max()) {
        return std::nullopt;
      }
      ++skip_count;
      const auto reduced_utility =
          checked_subtract(score.scheduled_utility, activity.utility_score);
      if (!reduced_utility) return std::nullopt;
      score.scheduled_utility = *reduced_utility;
      score.canonical_plan_key.skipped_ordinals.push_back(
          planning_activity.original_trip_ordinal);
      if (baseline.state == domain::PlanEntryState::kScheduled) {
        changed[*activity_index] = true;
      }
      continue;
    }

    if (decision.start_unix_ms >= decision.end_unix_ms) {
      return std::nullopt;
    }
    const std::size_t origin_index =
        prior_scheduled_activity_index.has_value()
            ? *prior_scheduled_activity_index + 1
            : 0;
    const std::size_t destination_index = *activity_index + 1;
    const auto& route =
        input.travel_time_matrix->at(origin_index, destination_index);
    const auto travel_ms = route_duration_milliseconds(route);
    const auto arrival =
        travel_ms ? checked_add(prior_scheduled_end, *travel_ms) : std::nullopt;
    if (!travel_ms || !arrival ||
        !contains_scheduled_alternative(
            generate_candidate_alternatives(
                input, planning_activity,
                UnixTimeMilliseconds{*arrival}),
            decision)) {
      return std::nullopt;
    }
    if (!checked_accumulate(*travel_ms, &score.total_travel_ms)) {
      return std::nullopt;
    }

    const auto duration =
        checked_subtract(decision.end_unix_ms, decision.start_unix_ms);
    const auto preferred_duration =
        checked_milliseconds(activity.timing.preferred_duration_seconds);
    if (!duration || !preferred_duration) return std::nullopt;
    if (*duration < *preferred_duration &&
        !checked_accumulate(*preferred_duration - *duration,
                            &score.total_preferred_shortfall_ms)) {
      return std::nullopt;
    }

    std::optional<std::int64_t> lateness_anchor;
    if (activity.timing.reservation_start.has_value()) {
      lateness_anchor = activity.timing.reservation_start->value();
    } else if (baseline.state == domain::PlanEntryState::kScheduled) {
      lateness_anchor = baseline.scheduled_start->value();
    }
    if (lateness_anchor.has_value() &&
        decision.start_unix_ms > *lateness_anchor) {
      const auto lateness =
          checked_subtract(decision.start_unix_ms, *lateness_anchor);
      if (!lateness ||
          !checked_accumulate(*lateness, &score.total_lateness_ms)) {
        return std::nullopt;
      }
    }

    if (baseline.state == domain::PlanEntryState::kOmitted) {
      changed[*activity_index] = true;
    } else {
      common_scheduled[*activity_index] = true;
      candidate_common_order.push_back(*activity_index);
      if (baseline.scheduled_start->value() != decision.start_unix_ms ||
          baseline.scheduled_end->value() != decision.end_unix_ms) {
        changed[*activity_index] = true;
      }
      const auto shift = checked_absolute_difference(
          decision.start_unix_ms, baseline.scheduled_start->value());
      if (!shift ||
          !checked_accumulate(*shift, &score.total_start_shift_ms)) {
        return std::nullopt;
      }
    }

    score.canonical_plan_key.scheduled_ordinals_in_order.push_back(
        planning_activity.original_trip_ordinal);
    score.canonical_plan_key.scheduled_entries.push_back(
        {.original_ordinal = planning_activity.original_trip_ordinal,
         .start_unix_ms = decision.start_unix_ms,
         .end_unix_ms = decision.end_unix_ms});
    score.final_scheduled_end_unix_ms = decision.end_unix_ms;
    prior_scheduled_activity_index = *activity_index;
    prior_scheduled_end = decision.end_unix_ms;
  }

  if (decisions.size() == activity_count &&
      std::find(decided.begin(), decided.end(), false) != decided.end()) {
    return std::nullopt;
  }

  std::vector<std::size_t> baseline_common_order;
  baseline_common_order.reserve(candidate_common_order.size());
  for (std::size_t index = 0; index < activity_count; ++index) {
    if (common_scheduled[index]) baseline_common_order.push_back(index);
  }
  if (baseline_common_order.size() != candidate_common_order.size()) {
    return std::nullopt;
  }
  std::vector<std::size_t> baseline_common_positions(activity_count, 0);
  std::vector<std::size_t> candidate_common_positions(activity_count, 0);
  for (std::size_t position = 0; position < baseline_common_order.size();
       ++position) {
    baseline_common_positions[baseline_common_order[position]] = position;
    candidate_common_positions[candidate_common_order[position]] = position;
  }
  for (std::size_t index = 0; index < activity_count; ++index) {
    if (common_scheduled[index] &&
        baseline_common_positions[index] !=
            candidate_common_positions[index]) {
      changed[index] = true;
    }
    if (changed[index] &&
        !checked_accumulate(1, &score.changed_activity_count)) {
      return std::nullopt;
    }
  }

  std::sort(score.canonical_plan_key.skipped_ordinals.begin(),
            score.canonical_plan_key.skipped_ordinals.end());
  return score.is_valid() ? std::optional<CandidateScore>{std::move(score)}
                          : std::nullopt;
}

BeamSearchResult run_beam_search(const BeamSearchInput& input,
                                 const ReplanBudget& budget) {
  if (!input.is_valid() || !budget.is_valid()) {
    return {.outcome = BeamSearchOutcome::kInvalidInput,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt};
  }
  if (budget.stop_token.stop_requested()) {
    return {.outcome = BeamSearchOutcome::kCancelled,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt};
  }
  if (std::chrono::steady_clock::now() >= budget.deadline) {
    return {.outcome = BeamSearchOutcome::kDeadlineExceeded,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt};
  }

  const auto initial_score = score_candidate(input, {});
  if (!initial_score) {
    return {.outcome = BeamSearchOutcome::kInvalidInput,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt};
  }
  if (input.remaining_activities.empty()) {
    return {
        .outcome = BeamSearchOutcome::kComplete,
        .best_decisions = std::vector<ExpansionDecision>{},
        .best_score = *initial_score,
    };
  }

  std::vector<BeamNode> beam{
      BeamNode{.decisions = {}, .score = *initial_score}};
  std::optional<BeamNode> best_complete;
  std::size_t expansion_count = 0;
  std::size_t candidate_count = 0;
  bool search_was_truncated = false;

  std::vector<std::size_t> activity_order(
      input.remaining_activities.size());
  for (std::size_t index = 0; index < activity_order.size(); ++index) {
    activity_order[index] = index;
  }
  std::sort(activity_order.begin(), activity_order.end(),
            [&input](std::size_t left, std::size_t right) {
              return input.remaining_activities[left].original_trip_ordinal <
                     input.remaining_activities[right].original_trip_ordinal;
            });

  for (std::size_t depth = 0;
       depth < input.remaining_activities.size(); ++depth) {
    std::vector<BeamNode> children;
    for (const auto& parent : beam) {
      std::vector<bool> decided(input.remaining_activities.size(), false);
      for (const auto& decision : parent.decisions) {
        const auto index =
            activity_index_for_ordinal(input, decision.activity_ordinal);
        if (!index) {
          return interrupted_result(
              BeamSearchOutcome::kInvalidInput, best_complete,
              expansion_count, candidate_count, search_was_truncated);
        }
        decided[*index] = true;
      }

      for (const auto activity_index : activity_order) {
        if (decided[activity_index]) continue;
        if (budget.stop_token.stop_requested()) {
          return interrupted_result(
              BeamSearchOutcome::kCancelled, best_complete, expansion_count,
              candidate_count, search_was_truncated);
        }
        if (std::chrono::steady_clock::now() >= budget.deadline) {
          return interrupted_result(
              BeamSearchOutcome::kDeadlineExceeded, best_complete,
              expansion_count, candidate_count, search_was_truncated);
        }
        if (expansion_count >= budget.max_expansions) {
          return interrupted_result(
              BeamSearchOutcome::kSearchLimited, best_complete,
              expansion_count, candidate_count, search_was_truncated);
        }
        ++expansion_count;

        const auto arrival =
            actual_arrival(input, parent.decisions, activity_index);
        const auto alternatives = generate_candidate_alternatives(
            input, input.remaining_activities[activity_index],
            arrival.value_or(input.planning_horizon_end));
        for (const auto& alternative : alternatives) {
          auto decisions = parent.decisions;
          decisions.push_back(expansion_decision(alternative));
          const auto score = score_candidate(input, decisions);
          if (!score ||
              !passes_protected_activity_lower_bound(input, decisions)) {
            continue;
          }
          if (candidate_count >= budget.max_candidates) {
            return interrupted_result(
                BeamSearchOutcome::kSearchLimited, best_complete,
                expansion_count, candidate_count, search_was_truncated);
          }
          ++candidate_count;
          BeamNode child{.decisions = std::move(decisions), .score = *score};
          if (child.decisions.size() ==
                  input.remaining_activities.size() &&
              (!best_complete ||
               is_better_complete(child.score, best_complete->score))) {
            best_complete = child;
          }
          children.push_back(std::move(child));
        }
      }
    }

    if (children.empty()) {
      if (best_complete) {
        return interrupted_result(
            BeamSearchOutcome::kComplete, best_complete, expansion_count,
            candidate_count, search_was_truncated);
      }
      return interrupted_result(
          search_was_truncated ? BeamSearchOutcome::kSearchLimited
                               : BeamSearchOutcome::kExhaustiveInfeasible,
          std::nullopt, expansion_count, candidate_count,
          search_was_truncated);
    }

    std::sort(
        children.begin(), children.end(),
        [](const BeamNode& left, const BeamNode& right) {
          return is_better_partial(left.score, left.decisions, right.score,
                                   right.decisions);
        });
    if (children.size() > budget.beam_width) {
      search_was_truncated = true;
      children.resize(budget.beam_width);
    }
    beam = std::move(children);
  }

  if (!best_complete) {
    return interrupted_result(
        search_was_truncated ? BeamSearchOutcome::kSearchLimited
                             : BeamSearchOutcome::kExhaustiveInfeasible,
        std::nullopt, expansion_count, candidate_count,
        search_was_truncated);
  }
  return {
      .outcome = BeamSearchOutcome::kComplete,
      .best_decisions = best_complete->decisions,
      .best_score = best_complete->score,
      .expansion_count = expansion_count,
      .candidate_count = candidate_count,
      .search_was_truncated = search_was_truncated,
  };
}

}  // namespace liveroute::planner
