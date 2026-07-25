#pragma once

#include <algorithm>
#include <compare>
#include <cstddef>
#include <cstdint>
#include <tuple>
#include <vector>

namespace liveroute::planner {

struct ScheduledPlanKeyEntry {
  std::size_t original_ordinal{};
  std::int64_t start_unix_ms{};
  std::int64_t end_unix_ms{};

  constexpr auto operator<=>(const ScheduledPlanKeyEntry&) const = default;
};

struct CanonicalPlanKey {
  std::vector<std::size_t> scheduled_ordinals_in_order;
  std::vector<ScheduledPlanKeyEntry> scheduled_entries;
  std::vector<std::size_t> skipped_ordinals;

  [[nodiscard]] bool is_valid() const noexcept {
    if (scheduled_ordinals_in_order.size() != scheduled_entries.size() ||
        !std::is_sorted(skipped_ordinals.begin(), skipped_ordinals.end()) ||
        std::adjacent_find(skipped_ordinals.begin(), skipped_ordinals.end()) !=
            skipped_ordinals.end()) {
      return false;
    }
    for (std::size_t index = 0; index < scheduled_entries.size(); ++index) {
      const auto& entry = scheduled_entries[index];
      if (scheduled_ordinals_in_order[index] != entry.original_ordinal ||
          entry.start_unix_ms >= entry.end_unix_ms ||
          std::find(skipped_ordinals.begin(), skipped_ordinals.end(),
                    entry.original_ordinal) != skipped_ordinals.end() ||
          std::find(scheduled_ordinals_in_order.begin(),
                    scheduled_ordinals_in_order.begin() +
                        static_cast<std::ptrdiff_t>(index),
                    entry.original_ordinal) !=
              scheduled_ordinals_in_order.begin() +
                  static_cast<std::ptrdiff_t>(index)) {
        return false;
      }
    }
    return true;
  }

  constexpr auto operator<=>(const CanonicalPlanKey&) const = default;
};

struct CandidateScore {
  // One count for each distinct remaining suffix priority rank, in ascending
  // rank order. Lower rank is more important.
  std::vector<std::uint32_t> skips_by_priority;
  std::int64_t scheduled_utility{};
  std::int64_t total_lateness_ms{};
  std::int64_t total_preferred_shortfall_ms{};
  std::int64_t total_travel_ms{};
  std::int64_t changed_activity_count{};
  std::int64_t total_start_shift_ms{};
  std::int64_t final_scheduled_end_unix_ms{};
  CanonicalPlanKey canonical_plan_key;

  [[nodiscard]] bool is_valid() const noexcept {
    return total_lateness_ms >= 0 && total_preferred_shortfall_ms >= 0 &&
           total_travel_ms >= 0 && changed_activity_count >= 0 &&
           total_start_shift_ms >= 0 && canonical_plan_key.is_valid();
  }
};

struct ExpansionDecision {
  std::size_t activity_ordinal{};
  std::uint8_t decision{};  // 0 scheduled, 1 skipped
  std::int64_t start_unix_ms{};
  std::int64_t end_unix_ms{};

  constexpr auto operator<=>(const ExpansionDecision&) const = default;
};

// Returns true only when left must be preferred under
// liveroute-v1-lexicographic-1's complete-candidate tuple.
[[nodiscard]] inline bool is_better_complete(const CandidateScore& left,
                                              const CandidateScore& right) {
  if (left.skips_by_priority != right.skips_by_priority) {
    return std::lexicographical_compare(
        left.skips_by_priority.begin(), left.skips_by_priority.end(),
        right.skips_by_priority.begin(), right.skips_by_priority.end());
  }
  if (left.scheduled_utility != right.scheduled_utility) {
    return left.scheduled_utility > right.scheduled_utility;
  }
  return std::tie(left.total_lateness_ms, left.total_preferred_shortfall_ms,
                  left.total_travel_ms, left.changed_activity_count,
                  left.total_start_shift_ms, left.final_scheduled_end_unix_ms,
                  left.canonical_plan_key) <
         std::tie(right.total_lateness_ms, right.total_preferred_shortfall_ms,
                  right.total_travel_ms, right.changed_activity_count,
                  right.total_start_shift_ms, right.final_scheduled_end_unix_ms,
                  right.canonical_plan_key);
}

// Partial scores already include optimistic undecided utility and zero future
// costs. The exact decision key resolves projected-score ties for beam
// retention without depending on insertion or thread order.
[[nodiscard]] inline bool is_better_partial(
    const CandidateScore& left, const std::vector<ExpansionDecision>& left_decisions,
    const CandidateScore& right, const std::vector<ExpansionDecision>& right_decisions) {
  if (is_better_complete(left, right)) return true;
  if (is_better_complete(right, left)) return false;
  return std::lexicographical_compare(left_decisions.begin(), left_decisions.end(),
                                      right_decisions.begin(), right_decisions.end());
}

}  // namespace liveroute::planner
