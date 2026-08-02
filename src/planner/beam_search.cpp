#include "liveroute/planner/beam_search.hpp"

#include <algorithm>
#include <array>
#include <chrono>
#include <cstdint>
#include <limits>
#include <numeric>
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

std::vector<CandidateAlternative> generate_candidate_alternatives_impl(
    const BeamSearchInput& input, const PlanningActivity& planning_activity,
    UnixTimeMilliseconds arrival, bool validate_input);

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
  const auto max_duration_ms = seconds_to_milliseconds(timing.max_duration_seconds);
  if (!duration_ms || !min_duration_ms || !max_duration_ms ||
      *duration_ms < *min_duration_ms || *duration_ms > *max_duration_ms) {
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

void add_generated_interval(const BeamSearchInput& input,
                            const PlanningActivity& planning_activity,
                            std::int64_t arrival, const TimeWindow& window,
                            std::int64_t start,
                            std::vector<CandidateAlternative>* alternatives) {
  const auto& timing = planning_activity.activity.timing;
  if (!has_positive_legal_room(input, planning_activity, window, start)) return;

  std::optional<std::int64_t> duration_ms;
  if (planning_activity.current_plan_segment.state ==
      domain::PlanEntryState::kScheduled) {
    duration_ms = checked_subtract(
        planning_activity.current_plan_segment.scheduled_end->value(),
        planning_activity.current_plan_segment.scheduled_start->value());
  } else {
    const auto duration_seconds =
        std::max<std::uint32_t>(1, timing.preferred_duration_seconds);
    duration_ms = checked_milliseconds(duration_seconds);
  }
  const auto end = duration_ms ? checked_add(start, *duration_ms) : std::nullopt;
  if (!end || !is_locally_hard_feasible(input, planning_activity, arrival, start,
                                         *end)) {
    return;
  }
  alternatives->push_back({.kind = CandidateAlternativeKind::kScheduled,
                           .activity_ordinal = planning_activity.original_trip_ordinal,
                           .start = UnixTimeMilliseconds{start},
                           .end = UnixTimeMilliseconds{*end},
                           .is_exact_current_plan = false});
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

[[nodiscard]] std::optional<std::size_t> activity_index_for_ordinal(
    const PlannerActivityColumns& columns, std::size_t ordinal) noexcept;

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

[[nodiscard]] std::optional<std::pair<std::size_t, std::int64_t>>
last_scheduled_state(const BeamSearchInput& input,
                     std::span<const ExpansionDecision> decisions,
                     const PlannerActivityColumns* columns = nullptr) noexcept {
  for (auto decision = decisions.rbegin(); decision != decisions.rend();
       ++decision) {
    if (decision->decision != 0) continue;
    const auto index = columns != nullptr
                           ? activity_index_for_ordinal(*columns,
                                                       decision->activity_ordinal)
                           : activity_index_for_ordinal(input,
                                                       decision->activity_ordinal);
    if (!index) return std::nullopt;
    return std::pair<std::size_t, std::int64_t>{*index,
                                                decision->end_unix_ms};
  }
  return std::pair<std::size_t, std::int64_t>{
      std::numeric_limits<std::size_t>::max(),
      input.suffix_start_time().value()};
}

[[nodiscard]] std::optional<UnixTimeMilliseconds> actual_arrival(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    std::size_t destination_activity_index,
    const PlannerActivityColumns* columns = nullptr) noexcept {
  const auto last = last_scheduled_state(input, decisions, columns);
  if (!last) return std::nullopt;
  const auto origin_matrix_index =
      last->first == std::numeric_limits<std::size_t>::max()
          ? std::size_t{0}
          : last->first + 1;
  const auto destination_matrix_index =
      columns != nullptr
          ? columns->matrix_location_indices[destination_activity_index]
          : destination_activity_index + 1;
  const auto& route = input.travel_time_matrix->at(origin_matrix_index,
                                                   destination_matrix_index);
  const auto travel_ms = route_duration_milliseconds(route);
  const auto arrival =
      travel_ms ? checked_add(last->second, *travel_ms) : std::nullopt;
  return arrival ? std::optional<UnixTimeMilliseconds>{
                       UnixTimeMilliseconds{*arrival}}
                 : std::nullopt;
}

[[nodiscard]] bool passes_protected_activity_lower_bound(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    std::vector<std::uint8_t>* reusable_decided = nullptr,
    bool validate_input = true) {
  std::vector<std::uint8_t> local_decided;
  auto& decided = reusable_decided != nullptr ? *reusable_decided : local_decided;
  decided.resize(input.remaining_activities.size());
  std::fill(decided.begin(), decided.end(), std::uint8_t{0});
  for (const auto& decision : decisions) {
    const auto index =
        activity_index_for_ordinal(input, decision.activity_ordinal);
    if (!index) return false;
    decided[*index] = 1;
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
    const auto alternatives = generate_candidate_alternatives_impl(
        input, activity, optimistic_arrival, validate_input);
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

void reconstruct_decisions(const PlannerScratch& scratch,
                           const BeamScratchCandidate& candidate,
                           std::vector<ExpansionDecision>* decisions) {
  decisions->resize(candidate.depth);
  auto path_index = candidate.path_index;
  for (std::size_t position = candidate.depth; position > 0; --position) {
    const auto& path = scratch.path_nodes.at(*path_index);
    (*decisions)[position - 1] = path.decision;
    path_index = path.parent_path_index;
  }
}

[[nodiscard]] BeamSearchResult interrupted_result(
    BeamSearchOutcome outcome,
    const std::optional<BeamScratchCandidate>& best,
    const PlannerScratch& scratch,
    std::size_t expansion_count, std::size_t candidate_count,
    bool search_was_truncated) {
  if (best.has_value()) {
    std::vector<ExpansionDecision> decisions;
    reconstruct_decisions(scratch, *best, &decisions);
    return {
        .outcome = outcome == BeamSearchOutcome::kComplete
                       ? BeamSearchOutcome::kComplete
                       : BeamSearchOutcome::kBestSoFar,
        .best_decisions = std::move(decisions),
        .best_score = best->score,
        .expansion_count = expansion_count,
        .candidate_count = candidate_count,
        .search_was_truncated = search_was_truncated,
        .deadline_hit = outcome == BeamSearchOutcome::kDeadlineExceeded,
        .cancellation_requested =
            outcome == BeamSearchOutcome::kCancelled,
    };
  }
  return {
      .outcome = outcome,
      .best_decisions = std::nullopt,
      .best_score = std::nullopt,
      .expansion_count = expansion_count,
      .candidate_count = candidate_count,
      .search_was_truncated = search_was_truncated,
      .deadline_hit = outcome == BeamSearchOutcome::kDeadlineExceeded,
      .cancellation_requested =
          outcome == BeamSearchOutcome::kCancelled,
  };
}

constexpr std::uint16_t kBaselineScheduled = 1U << 0U;
constexpr std::uint16_t kMandatory = 1U << 1U;
constexpr std::uint16_t kMovable = 1U << 2U;
constexpr std::uint16_t kSkippable = 1U << 3U;
constexpr std::uint16_t kReservationPresent = 1U << 4U;
constexpr std::uint16_t kMandatoryDeadlinePresent = 1U << 5U;
constexpr std::uint16_t kFoundClosed = 1U << 6U;

[[nodiscard]] bool prepare_activity_columns(
    const BeamSearchInput& input, PlannerActivityColumns* columns) {
  columns->reset();
  const auto count = input.remaining_activities.size();
  columns->activity_ids.reserve(count);
  columns->original_trip_ordinals.reserve(count);
  columns->matrix_location_indices.reserve(count);
  columns->priority_ranks.reserve(count);
  columns->utility_scores.reserve(count);
  columns->minimum_duration_ms.reserve(count);
  columns->scheduled_duration_ms.reserve(count);
  columns->preferred_duration_ms.reserve(count);
  columns->maximum_duration_ms.reserve(count);
  columns->earliest_open_ms.reserve(count);
  columns->latest_close_ms.reserve(count);
  columns->baseline_start_ms.reserve(count);
  columns->baseline_end_ms.reserve(count);
  columns->reservation_start_ms.reserve(count);
  columns->reservation_latest_start_ms.reserve(count);
  columns->mandatory_deadline_ms.reserve(count);
  columns->flags.reserve(count);
  columns->window_offsets.reserve(count + 1);
  columns->sorted_ordinals.reserve(count);
  columns->sorted_activity_indices.reserve(count);
  columns->window_offsets.push_back(0);

  for (std::size_t index = 0; index < count; ++index) {
    const auto& planning_activity = input.remaining_activities[index];
    const auto& activity = planning_activity.activity;
    const auto& timing = activity.timing;
    const auto& baseline = planning_activity.current_plan_segment;
    const auto minimum_duration = checked_milliseconds(timing.min_duration_seconds);
    const auto preferred_duration =
        checked_milliseconds(timing.preferred_duration_seconds);
    const auto maximum_duration = checked_milliseconds(timing.max_duration_seconds);
    if (!minimum_duration || !preferred_duration || !maximum_duration) {
      return false;
    }

    std::optional<std::int64_t> scheduled_duration;
    if (baseline.state == domain::PlanEntryState::kScheduled) {
      scheduled_duration = checked_subtract(
          baseline.scheduled_end->value(), baseline.scheduled_start->value());
    } else {
      scheduled_duration = checked_milliseconds(
          std::max<std::uint32_t>(1, timing.preferred_duration_seconds));
    }
    if (!scheduled_duration) return false;

    std::optional<std::int64_t> reservation_latest_start;
    if (timing.reservation_start.has_value()) {
      const auto grace_ms = checked_milliseconds(timing.reservation_grace_seconds);
      reservation_latest_start =
          grace_ms ? checked_add(timing.reservation_start->value(), *grace_ms)
                   : std::nullopt;
      if (!reservation_latest_start) return false;
    }

    std::uint16_t flags = 0;
    if (baseline.state == domain::PlanEntryState::kScheduled) {
      flags |= kBaselineScheduled;
    }
    if (timing.mandatory) flags |= kMandatory;
    if (timing.can_move) flags |= kMovable;
    if (timing.can_skip) flags |= kSkippable;
    if (timing.reservation_start.has_value()) flags |= kReservationPresent;
    if (timing.mandatory_deadline.has_value()) {
      flags |= kMandatoryDeadlinePresent;
    }
    if (activity.found_closed_at.has_value()) flags |= kFoundClosed;

    columns->activity_ids.push_back(activity.activity_id);
    columns->original_trip_ordinals.push_back(planning_activity.original_trip_ordinal);
    columns->matrix_location_indices.push_back(index + 1);
    columns->priority_ranks.push_back(activity.priority_rank);
    columns->utility_scores.push_back(activity.utility_score);
    columns->minimum_duration_ms.push_back(*minimum_duration);
    columns->scheduled_duration_ms.push_back(*scheduled_duration);
    columns->preferred_duration_ms.push_back(*preferred_duration);
    columns->maximum_duration_ms.push_back(*maximum_duration);
    columns->baseline_start_ms.push_back(
        baseline.scheduled_start.has_value() ? baseline.scheduled_start->value() : 0);
    columns->baseline_end_ms.push_back(
        baseline.scheduled_end.has_value() ? baseline.scheduled_end->value() : 0);
    columns->reservation_start_ms.push_back(
        timing.reservation_start.has_value() ? timing.reservation_start->value() : 0);
    columns->reservation_latest_start_ms.push_back(
        reservation_latest_start.value_or(0));
    columns->mandatory_deadline_ms.push_back(
        timing.mandatory_deadline.has_value() ? timing.mandatory_deadline->value() : 0);
    columns->flags.push_back(flags);

    for (const auto& window : timing.open_windows) {
      columns->window_opens_ms.push_back(window.opens_at.value());
      columns->window_closes_ms.push_back(window.closes_at.value());
    }
    columns->window_offsets.push_back(columns->window_opens_ms.size());
    columns->sorted_ordinals.push_back(planning_activity.original_trip_ordinal);
    columns->sorted_activity_indices.push_back(static_cast<std::uint8_t>(index));
  }

  std::iota(columns->sorted_activity_indices.begin(),
            columns->sorted_activity_indices.end(), std::uint8_t{0});
  std::sort(columns->sorted_activity_indices.begin(),
            columns->sorted_activity_indices.end(), [&columns](std::uint8_t left,
                                                               std::uint8_t right) {
    return columns->original_trip_ordinals[left] <
           columns->original_trip_ordinals[right];
  });
  for (std::size_t position = 0; position < count; ++position) {
    columns->sorted_ordinals[position] =
        columns->original_trip_ordinals[columns->sorted_activity_indices[position]];
  }
  return true;
}

[[nodiscard]] std::optional<std::size_t> activity_index_for_ordinal(
    const PlannerActivityColumns& columns, std::size_t ordinal) noexcept {
  const auto position = std::lower_bound(columns.sorted_ordinals.begin(),
                                         columns.sorted_ordinals.end(), ordinal);
  if (position == columns.sorted_ordinals.end() || *position != ordinal) {
    return std::nullopt;
  }
  return static_cast<std::size_t>(columns.sorted_activity_indices[
      static_cast<std::size_t>(position - columns.sorted_ordinals.begin())]);
}

}  // namespace

bool BeamSearchInput::is_valid() const noexcept {
  if (planning_horizon_start >= planning_horizon_end ||
      current_time < planning_horizon_start || current_time > planning_horizon_end ||
      preserved_prefix.size() > 64 ||
      remaining_activities.size() > 64 - preserved_prefix.size() ||
      travel_time_matrix == nullptr ||
      travel_time_matrix->location_count() != remaining_activities.size() + 1) {
    return false;
  }
  std::array<std::array<std::byte, 16>, 64> activity_ids{};
  std::size_t activity_id_count = 0;
  std::optional<UnixTimeMilliseconds> prior_preserved_end;
  for (const auto& segment : preserved_prefix) {
    if (!segment.is_valid()) return false;
    activity_ids[activity_id_count++] = segment.activity_id.value();
    if (segment.state == domain::PlanEntryState::kScheduled) {
      if (prior_preserved_end.has_value() &&
          *segment.scheduled_start < *prior_preserved_end) {
        return false;
      }
      prior_preserved_end = segment.scheduled_end;
    }
  }

  std::array<std::size_t, 64> ordinals{};
  std::size_t ordinal_count = 0;
  for (const auto& activity : remaining_activities) {
    if (!activity.is_valid()) return false;
    activity_ids[activity_id_count++] = activity.activity.activity_id.value();
    ordinals[ordinal_count++] = activity.original_trip_ordinal;
  }
  std::sort(activity_ids.begin(), activity_ids.begin() + activity_id_count);
  if (std::adjacent_find(activity_ids.begin(),
                         activity_ids.begin() + activity_id_count) !=
      activity_ids.begin() + activity_id_count) {
    return false;
  }
  std::sort(ordinals.begin(), ordinals.begin() + ordinal_count);
  return std::adjacent_find(ordinals.begin(), ordinals.begin() + ordinal_count) ==
         ordinals.begin() + ordinal_count;
}

UnixTimeMilliseconds BeamSearchInput::suffix_start_time() const noexcept {
  auto start = current_time;
  for (const auto& segment : preserved_prefix) {
    if (segment.state == domain::PlanEntryState::kScheduled &&
        segment.scheduled_end.has_value() && *segment.scheduled_end > start) {
      start = *segment.scheduled_end;
    }
  }
  return start;
}

namespace {

std::vector<CandidateAlternative> generate_candidate_alternatives_impl(
    const BeamSearchInput& input, const PlanningActivity& planning_activity,
    UnixTimeMilliseconds arrival, bool validate_input) {
  std::vector<CandidateAlternative> alternatives;
  if ((validate_input && !input.is_valid()) || !planning_activity.is_valid()) {
    return alternatives;
  }

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
        add_generated_interval(input, planning_activity, arrival.value(), window,
                               earliest_start, &alternatives);
      }

      if (activity.activity_class != domain::ActivityClass::kFixed &&
          has_current_interval) {
        const auto current_start = segment.scheduled_start->value();
        if (current_start != earliest_start && current_start >= arrival.value() &&
            current_start >= window.opens_at.value() &&
            current_start < window.closes_at.value() &&
            has_positive_legal_room(input, planning_activity, window, current_start)) {
          add_generated_interval(input, planning_activity, arrival.value(), window,
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

}  // namespace

std::vector<CandidateAlternative> generate_candidate_alternatives(
    const BeamSearchInput& input, const PlanningActivity& planning_activity,
    UnixTimeMilliseconds arrival) {
  return generate_candidate_alternatives_impl(
      input, planning_activity, arrival, true);
}

template <bool UseColumns, bool ValidateInput>
std::optional<CandidateScore> score_candidate_impl(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions, PlannerScoreScratch& scratch,
    const PlannerActivityColumns* columns) {
  if constexpr (ValidateInput) {
    if (!input.is_valid()) return std::nullopt;
  }
  if (decisions.size() > input.remaining_activities.size()) {
    return std::nullopt;
  }
  if constexpr (UseColumns) {
    if (columns == nullptr ||
        columns->original_trip_ordinals.size() !=
            input.remaining_activities.size()) {
      return std::nullopt;
    }
  }

  const auto activity_count = input.remaining_activities.size();
  scratch.prepare(activity_count, decisions.size());
  auto& decided = scratch.decided;
  auto& changed = scratch.changed;
  auto& common_scheduled = scratch.common_scheduled;
  auto& candidate_common_order = scratch.candidate_common_order;
  auto& priority_ranks = scratch.priority_ranks;
  priority_ranks.reserve(activity_count);
  std::int64_t optimistic_utility = 0;
  for (std::size_t index = 0; index < activity_count; ++index) {
    const auto priority_rank = [&] {
      if constexpr (UseColumns) return columns->priority_ranks[index];
      return input.remaining_activities[index].activity.priority_rank;
    }();
    const auto utility_score = [&] {
      if constexpr (UseColumns) return columns->utility_scores[index];
      return input.remaining_activities[index].activity.utility_score;
    }();
    priority_ranks.push_back(priority_rank);
    if (!checked_accumulate(utility_score,
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
      .final_scheduled_end_unix_ms = input.suffix_start_time().value(),
      .canonical_plan_key = {},
  };
  score.canonical_plan_key.scheduled_ordinals_in_order.reserve(decisions.size());
  score.canonical_plan_key.scheduled_entries.reserve(decisions.size());
  score.canonical_plan_key.skipped_ordinals.reserve(decisions.size());

  std::optional<std::size_t> prior_scheduled_activity_index;
  std::int64_t prior_scheduled_end = input.suffix_start_time().value();

  const auto activity_index = [&](std::size_t ordinal)
      -> std::optional<std::size_t> {
    if constexpr (UseColumns) {
      return activity_index_for_ordinal(*columns, ordinal);
    }
    return activity_index_for_ordinal(input, ordinal);
  };
  const auto is_baseline_scheduled = [&](std::size_t index) {
    if constexpr (UseColumns) return (columns->flags[index] & kBaselineScheduled) != 0;
    return input.remaining_activities[index].current_plan_segment.state ==
           domain::PlanEntryState::kScheduled;
  };
  const auto is_mandatory = [&](std::size_t index) {
    if constexpr (UseColumns) return (columns->flags[index] & kMandatory) != 0;
    return input.remaining_activities[index].activity.timing.mandatory;
  };
  const auto is_skippable = [&](std::size_t index) {
    if constexpr (UseColumns) return (columns->flags[index] & kSkippable) != 0;
    return input.remaining_activities[index].activity.timing.can_skip;
  };
  const auto preferred_duration = [&](std::size_t index) {
    if constexpr (UseColumns) return columns->preferred_duration_ms[index];
    return checked_milliseconds(input.remaining_activities[index]
                                    .activity.timing.preferred_duration_seconds)
        .value_or(0);
  };
  const auto original_ordinal = [&](std::size_t index) {
    if constexpr (UseColumns) return columns->original_trip_ordinals[index];
    return input.remaining_activities[index].original_trip_ordinal;
  };
  const auto baseline_start = [&](std::size_t index) {
    if constexpr (UseColumns) return columns->baseline_start_ms[index];
    return input.remaining_activities[index].current_plan_segment.scheduled_start
        ->value();
  };
  const auto baseline_end = [&](std::size_t index) {
    if constexpr (UseColumns) return columns->baseline_end_ms[index];
    return input.remaining_activities[index].current_plan_segment.scheduled_end
        ->value();
  };
  const auto has_reservation = [&](std::size_t index) {
    if constexpr (UseColumns) return (columns->flags[index] & kReservationPresent) != 0;
    return input.remaining_activities[index].activity.timing.reservation_start
        .has_value();
  };
  const auto reservation_start = [&](std::size_t index) {
    if constexpr (UseColumns) return columns->reservation_start_ms[index];
    return input.remaining_activities[index].activity.timing.reservation_start
        ->value();
  };

  for (const auto& decision : decisions) {
    const auto resolved_activity_index = activity_index(decision.activity_ordinal);
    if (!resolved_activity_index || decided[*resolved_activity_index] ||
        decision.decision > 1) {
      return std::nullopt;
    }
    const auto index = *resolved_activity_index;
    decided[index] = true;

    const auto& planning_activity = input.remaining_activities[index];
    const auto& activity = planning_activity.activity;

    if (decision.decision == 1) {
      if (decision.start_unix_ms != 0 || decision.end_unix_ms != 0 ||
          is_mandatory(index) || !is_skippable(index)) {
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
      const auto utility_score = [&] {
        if constexpr (UseColumns) return columns->utility_scores[index];
        return activity.utility_score;
      }();
      const auto reduced_utility =
          checked_subtract(score.scheduled_utility, utility_score);
      if (!reduced_utility) return std::nullopt;
      score.scheduled_utility = *reduced_utility;
      score.canonical_plan_key.skipped_ordinals.push_back(
          original_ordinal(index));
      if (is_baseline_scheduled(index)) {
        changed[index] = true;
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
    const std::size_t destination_index =
        UseColumns ? columns->matrix_location_indices[index] : index + 1;
    const auto& route =
        input.travel_time_matrix->at(origin_index, destination_index);
    const auto travel_ms = route_duration_milliseconds(route);
    const auto arrival =
        travel_ms ? checked_add(prior_scheduled_end, *travel_ms) : std::nullopt;
    if (!travel_ms || !arrival ||
        !contains_scheduled_alternative(
            generate_candidate_alternatives_impl(
                input, planning_activity,
                UnixTimeMilliseconds{*arrival}, ValidateInput),
            decision)) {
      return std::nullopt;
    }
    if (!checked_accumulate(*travel_ms, &score.total_travel_ms)) {
      return std::nullopt;
    }

    const auto duration =
        checked_subtract(decision.end_unix_ms, decision.start_unix_ms);
    const auto preferred_duration_ms = preferred_duration(index);
    if (!duration || preferred_duration_ms <= 0) return std::nullopt;
    if (*duration < preferred_duration_ms &&
        !checked_accumulate(preferred_duration_ms - *duration,
                            &score.total_preferred_shortfall_ms)) {
      return std::nullopt;
    }

    std::optional<std::int64_t> lateness_anchor;
    if (has_reservation(index)) {
      lateness_anchor = reservation_start(index);
    } else if (is_baseline_scheduled(index)) {
      lateness_anchor = baseline_start(index);
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

    if (!is_baseline_scheduled(index)) {
      changed[index] = true;
    } else {
      common_scheduled[index] = true;
      candidate_common_order.push_back(index);
      if (baseline_start(index) != decision.start_unix_ms ||
          baseline_end(index) != decision.end_unix_ms) {
        changed[index] = true;
      }
      const auto shift = checked_absolute_difference(
          decision.start_unix_ms, baseline_start(index));
      if (!shift ||
          !checked_accumulate(*shift, &score.total_start_shift_ms)) {
        return std::nullopt;
      }
    }

    score.canonical_plan_key.scheduled_ordinals_in_order.push_back(
        original_ordinal(index));
    score.canonical_plan_key.scheduled_entries.push_back(
        {.original_ordinal = original_ordinal(index),
         .start_unix_ms = decision.start_unix_ms,
         .end_unix_ms = decision.end_unix_ms});
    score.final_scheduled_end_unix_ms = decision.end_unix_ms;
    prior_scheduled_activity_index = index;
    prior_scheduled_end = decision.end_unix_ms;
  }

  if (decisions.size() == activity_count &&
      std::find(decided.begin(), decided.end(), false) != decided.end()) {
    return std::nullopt;
  }

  auto& baseline_common_order = scratch.baseline_common_order;
  baseline_common_order.reserve(candidate_common_order.size());
  for (std::size_t index = 0; index < activity_count; ++index) {
    if (common_scheduled[index]) baseline_common_order.push_back(index);
  }
  if (baseline_common_order.size() != candidate_common_order.size()) {
    return std::nullopt;
  }
  auto& baseline_common_positions = scratch.baseline_common_positions;
  auto& candidate_common_positions = scratch.candidate_common_positions;
  std::fill(baseline_common_positions.begin(), baseline_common_positions.end(), 0);
  std::fill(candidate_common_positions.begin(), candidate_common_positions.end(), 0);
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

std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions) {
  PlannerScoreScratch scratch;
  return score_candidate_impl<false, true>(input, decisions, scratch, nullptr);
}

std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    PlannerScoreScratch& scratch) {
  return score_candidate_impl<false, true>(input, decisions, scratch, nullptr);
}

std::optional<CandidateScore> score_candidate(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    PlannerScoreScratch& scratch, const PlannerActivityColumns& columns) {
  return score_candidate_impl<true, true>(input, decisions, scratch, &columns);
}

namespace {

std::optional<CandidateScore> score_validated_candidate(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions,
    PlannerScoreScratch& scratch,
    const PlannerActivityColumns* columns) {
  return columns == nullptr
             ? score_candidate_impl<false, false>(input, decisions, scratch,
                                                  nullptr)
             : score_candidate_impl<true, false>(input, decisions, scratch,
                                                 columns);
}

}  // namespace

BeamSearchResult run_beam_search(const BeamSearchInput& input,
                                 const ReplanBudget& budget) {
  PlannerScratch scratch;
  return run_beam_search(input, budget, scratch);
}

BeamSearchResult run_beam_search(const BeamSearchInput& input,
                                 const ReplanBudget& budget,
                                 PlannerScratch& scratch) {
  scratch.reset();
  if (!input.is_valid() || !budget.is_valid()) {
    return {.outcome = BeamSearchOutcome::kInvalidInput,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt};
  }
  if (scratch.use_soa && !prepare_activity_columns(input, &scratch.activity_columns)) {
    return {.outcome = BeamSearchOutcome::kInvalidInput,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt};
  }
  if (budget.stop_token.stop_requested()) {
    return {.outcome = BeamSearchOutcome::kCancelled,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt,
            .cancellation_requested = true};
  }
  if (std::chrono::steady_clock::now() >= budget.deadline) {
    return {.outcome = BeamSearchOutcome::kDeadlineExceeded,
            .best_decisions = std::nullopt,
            .best_score = std::nullopt,
            .deadline_hit = true};
  }

  const bool use_validated_score =
      (scratch.tail_optimization_mask & kTailValidatedInput) != 0;
  const auto* columns =
      scratch.use_soa ? &scratch.activity_columns : nullptr;
  const auto initial_score = use_validated_score
                                 ? score_validated_candidate(input, {},
                                                             scratch.score,
                                                             columns)
                                 : scratch.use_soa
                                       ? score_candidate(input, {}, scratch.score,
                                                         scratch.activity_columns)
                                       : score_candidate(input, {}, scratch.score);
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

  scratch.beam.push_back(
      {.path_index = std::nullopt, .depth = 0, .score = *initial_score});
  std::optional<BeamScratchCandidate> best_complete;
  std::size_t expansion_count = 0;
  std::size_t candidate_count = 0;
  bool search_was_truncated = false;

  scratch.activity_order.resize(input.remaining_activities.size());
  for (std::size_t index = 0; index < scratch.activity_order.size(); ++index) {
    scratch.activity_order[index] = index;
  }
  std::sort(scratch.activity_order.begin(), scratch.activity_order.end(),
            [&input](std::size_t left, std::size_t right) {
              return input.remaining_activities[left].original_trip_ordinal <
                     input.remaining_activities[right].original_trip_ordinal;
            });
  scratch.decided.resize(input.remaining_activities.size());

  for (std::size_t depth = 0;
       depth < input.remaining_activities.size(); ++depth) {
    scratch.children.clear();
    for (const auto& parent : scratch.beam) {
      reconstruct_decisions(scratch, parent, &scratch.working_decisions);
      std::fill(scratch.decided.begin(), scratch.decided.end(),
                std::uint8_t{0});
      for (const auto& decision : scratch.working_decisions) {
        const auto index = scratch.use_soa
                               ? activity_index_for_ordinal(
                                     scratch.activity_columns,
                                     decision.activity_ordinal)
                               : activity_index_for_ordinal(
                                     input, decision.activity_ordinal);
        if (!index) {
          return interrupted_result(
              BeamSearchOutcome::kInvalidInput, best_complete, scratch,
              expansion_count, candidate_count, search_was_truncated);
        }
        scratch.decided[*index] = 1;
      }

      for (const auto activity_index : scratch.activity_order) {
        if (scratch.decided[activity_index] != 0) continue;
        if (budget.stop_token.stop_requested()) {
          return interrupted_result(
              BeamSearchOutcome::kCancelled, best_complete, scratch,
              expansion_count, candidate_count, search_was_truncated);
        }
        if (std::chrono::steady_clock::now() >= budget.deadline) {
          return interrupted_result(
              BeamSearchOutcome::kDeadlineExceeded, best_complete, scratch,
              expansion_count, candidate_count, search_was_truncated);
        }
        if (expansion_count >= budget.max_expansions) {
          return interrupted_result(
              BeamSearchOutcome::kSearchLimited, best_complete, scratch,
              expansion_count, candidate_count, search_was_truncated);
        }
        ++expansion_count;

        const auto arrival = actual_arrival(
            input, scratch.working_decisions, activity_index,
            scratch.use_soa ? &scratch.activity_columns : nullptr);
        const auto alternatives = generate_candidate_alternatives_impl(
            input, input.remaining_activities[activity_index],
            arrival.value_or(input.planning_horizon_end),
            !use_validated_score);
        for (const auto& alternative : alternatives) {
          scratch.path_nodes.push_back(
              {.parent_path_index = parent.path_index,
               .decision = expansion_decision(alternative)});
          const auto child_path_index = scratch.path_nodes.size() - 1;
          scratch.working_decisions.push_back(
              scratch.path_nodes.back().decision);
          const auto score = use_validated_score
                                 ? score_validated_candidate(
                                       input, scratch.working_decisions,
                                       scratch.score, columns)
                                 : scratch.use_soa
                                       ? score_candidate(
                                             input, scratch.working_decisions,
                                             scratch.score,
                                             scratch.activity_columns)
                                       : score_candidate(
                                             input, scratch.working_decisions,
                                             scratch.score);
          if (!score ||
              !passes_protected_activity_lower_bound(
                  input, scratch.working_decisions,
                  (scratch.tail_optimization_mask & kTailLowerBoundScratch) != 0
                      ? &scratch.protected_decided
                      : nullptr,
                  !use_validated_score)) {
            scratch.working_decisions.pop_back();
            scratch.path_nodes.pop_back();
            continue;
          }
          if (candidate_count >= budget.max_candidates) {
            scratch.working_decisions.pop_back();
            scratch.path_nodes.pop_back();
            return interrupted_result(
                BeamSearchOutcome::kSearchLimited, best_complete, scratch,
                expansion_count, candidate_count, search_was_truncated);
          }
          ++candidate_count;
          BeamScratchCandidate child{
              .path_index = child_path_index,
              .depth = parent.depth + 1,
              .score = *score};
          if (child.depth ==
                  input.remaining_activities.size() &&
              (!best_complete ||
               is_better_complete(child.score, best_complete->score))) {
            best_complete = child;
          }
          scratch.children.push_back(std::move(child));
          scratch.working_decisions.pop_back();
        }
      }
    }

    if (scratch.children.empty()) {
      if (best_complete) {
        return interrupted_result(
            BeamSearchOutcome::kComplete, best_complete, scratch,
            expansion_count, candidate_count, search_was_truncated);
      }
      return interrupted_result(
          search_was_truncated ? BeamSearchOutcome::kSearchLimited
                               : BeamSearchOutcome::kExhaustiveInfeasible,
          std::nullopt, scratch, expansion_count, candidate_count,
          search_was_truncated);
    }

    const auto compare_children =
        [&scratch](const BeamScratchCandidate& left,
                   const BeamScratchCandidate& right) {
          reconstruct_decisions(scratch, left, &scratch.comparison_left);
          reconstruct_decisions(scratch, right, &scratch.comparison_right);
          return is_better_partial(
              left.score, scratch.comparison_left, right.score,
              scratch.comparison_right);
        };
    if (scratch.children.size() > budget.beam_width) {
      search_was_truncated = true;
      if ((scratch.tail_optimization_mask & kTailPartialBeamSelection) != 0) {
        std::partial_sort(
            scratch.children.begin(),
            scratch.children.begin() +
                static_cast<std::ptrdiff_t>(budget.beam_width),
            scratch.children.end(), compare_children);
      } else {
        std::sort(scratch.children.begin(), scratch.children.end(),
                  compare_children);
      }
      scratch.children.resize(budget.beam_width);
    } else {
      std::sort(scratch.children.begin(), scratch.children.end(),
                compare_children);
    }
    scratch.beam.swap(scratch.children);
  }

  if (!best_complete) {
    return interrupted_result(
        search_was_truncated ? BeamSearchOutcome::kSearchLimited
                             : BeamSearchOutcome::kExhaustiveInfeasible,
        std::nullopt, scratch, expansion_count, candidate_count,
        search_was_truncated);
  }
  std::vector<ExpansionDecision> best_decisions;
  reconstruct_decisions(scratch, *best_complete, &best_decisions);
  return {
      .outcome = BeamSearchOutcome::kComplete,
      .best_decisions = std::move(best_decisions),
      .best_score = best_complete->score,
      .expansion_count = expansion_count,
      .candidate_count = candidate_count,
      .search_was_truncated = search_was_truncated,
      .deadline_hit = false,
      .cancellation_requested = false,
  };
}

}  // namespace liveroute::planner
