#include "liveroute/planner/candidate_itinerary.hpp"

#include <algorithm>
#include <cstddef>
#include <optional>
#include <utility>
#include <vector>

namespace liveroute::planner {

namespace {

[[nodiscard]] std::optional<std::size_t> activity_index_for_id(
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

[[nodiscard]] std::optional<std::size_t> activity_index_for_ordinal(
    const BeamSearchInput& input, std::size_t ordinal) noexcept {
  for (std::size_t index = 0; index < input.remaining_activities.size();
       ++index) {
    if (input.remaining_activities[index].original_trip_ordinal == ordinal) {
      return index;
    }
  }
  return std::nullopt;
}

[[nodiscard]] bool same_segment(
    const domain::CurrentPlanSegment& left,
    const domain::CurrentPlanSegment& right) noexcept {
  return left.activity_id == right.activity_id && left.state == right.state &&
         left.scheduled_start == right.scheduled_start &&
         left.scheduled_end == right.scheduled_end;
}

}  // namespace

bool CandidateItinerary::is_valid_for(const BeamSearchInput& input) const {
  if (!input.is_valid() || preserved_prefix.size() != input.preserved_prefix.size() ||
      revised_suffix.size() != input.remaining_activities.size()) {
    return false;
  }
  for (std::size_t index = 0; index < preserved_prefix.size(); ++index) {
    if (!same_segment(preserved_prefix[index], input.preserved_prefix[index])) {
      return false;
    }
  }

  std::vector<ExpansionDecision> decisions;
  decisions.reserve(revised_suffix.size());
  std::optional<std::size_t> prior_skipped_ordinal;
  bool reached_skipped_entries = false;
  for (const auto& segment : revised_suffix) {
    if (!segment.is_valid()) return false;
    const auto activity_index = activity_index_for_id(input, segment.activity_id);
    if (!activity_index) return false;
    const auto ordinal =
        input.remaining_activities[*activity_index].original_trip_ordinal;
    if (segment.state == domain::PlanEntryState::kOmitted) {
      reached_skipped_entries = true;
      if (prior_skipped_ordinal.has_value() &&
          ordinal <= *prior_skipped_ordinal) {
        return false;
      }
      prior_skipped_ordinal = ordinal;
      decisions.push_back({.activity_ordinal = ordinal,
                           .decision = 1,
                           .start_unix_ms = 0,
                           .end_unix_ms = 0});
      continue;
    }
    if (reached_skipped_entries) return false;
    decisions.push_back(
        {.activity_ordinal = ordinal,
         .decision = 0,
         .start_unix_ms = segment.scheduled_start->value(),
         .end_unix_ms = segment.scheduled_end->value()});
  }
  return score_candidate(input, decisions).has_value();
}

std::optional<CandidateItinerary> reconstruct_candidate_itinerary(
    const BeamSearchInput& input,
    std::span<const ExpansionDecision> decisions) {
  if (!input.is_valid() ||
      decisions.size() != input.remaining_activities.size() ||
      !score_candidate(input, decisions).has_value()) {
    return std::nullopt;
  }

  CandidateItinerary itinerary{.preserved_prefix = input.preserved_prefix,
                               .revised_suffix = {}};
  itinerary.revised_suffix.reserve(decisions.size());
  std::vector<std::pair<std::size_t, domain::CurrentPlanSegment>> skipped;
  skipped.reserve(decisions.size());
  for (const auto& decision : decisions) {
    const auto activity_index =
        activity_index_for_ordinal(input, decision.activity_ordinal);
    if (!activity_index) return std::nullopt;
    const auto activity_id =
        input.remaining_activities[*activity_index].activity.activity_id;
    if (decision.decision == 0) {
      itinerary.revised_suffix.push_back(
          {.activity_id = activity_id,
           .state = domain::PlanEntryState::kScheduled,
           .scheduled_start =
               domain::UnixTimeMilliseconds{decision.start_unix_ms},
           .scheduled_end =
               domain::UnixTimeMilliseconds{decision.end_unix_ms}});
    } else {
      skipped.push_back(
          {decision.activity_ordinal,
           {.activity_id = activity_id,
            .state = domain::PlanEntryState::kOmitted,
            .scheduled_start = std::nullopt,
            .scheduled_end = std::nullopt}});
    }
  }
  std::sort(skipped.begin(), skipped.end(),
            [](const auto& left, const auto& right) {
              return left.first < right.first;
            });
  for (auto& [ordinal, segment] : skipped) {
    static_cast<void>(ordinal);
    itinerary.revised_suffix.push_back(std::move(segment));
  }
  return itinerary.is_valid_for(input)
             ? std::optional<CandidateItinerary>{std::move(itinerary)}
             : std::nullopt;
}

}  // namespace liveroute::planner
