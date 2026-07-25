#pragma once

#include <chrono>
#include <cstddef>
#include <optional>
#include <stop_token>
#include <vector>

#include "liveroute/domain/activity.hpp"
#include "liveroute/domain/current_plan.hpp"
#include "liveroute/domain/travel_time_matrix.hpp"

namespace liveroute::planner {

struct PlanningActivity {
  domain::Activity activity;
  std::size_t original_trip_ordinal{};
  domain::CurrentPlanSegment current_plan_segment;

  [[nodiscard]] bool is_valid() const noexcept {
    return activity.is_valid() &&
           activity.activity_id == current_plan_segment.activity_id &&
           current_plan_segment.is_valid();
  }
};

struct ReplanBudget {
  domain::Deadline deadline;
  std::size_t max_candidates{};
  std::size_t beam_width{};
  std::size_t max_expansions{};
  std::stop_token stop_token;

  [[nodiscard]] bool is_valid() const noexcept {
    return max_candidates != 0 && beam_width != 0 && max_expansions != 0;
  }
};

// Matrix index zero is the current location; remaining activity i is matrix
// index i + 1. All values are already normalized UTC/in-memory constraints.
struct BeamSearchInput {
  domain::UnixTimeMilliseconds current_time;
  domain::UnixTimeMilliseconds planning_horizon_start;
  domain::UnixTimeMilliseconds planning_horizon_end;
  std::vector<domain::CurrentPlanSegment> preserved_prefix;
  std::vector<PlanningActivity> remaining_activities;
  const domain::TravelTimeMatrix* travel_time_matrix{};

  [[nodiscard]] bool is_valid() const noexcept;
};

}  // namespace liveroute::planner
