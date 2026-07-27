#pragma once

#include <optional>

#include "liveroute/domain/trip_state.hpp"
#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/planner/beam_search.hpp"

namespace liveroute::planner {

// Builds the transport-independent planner input from one immutable,
// shard-owned state snapshot and its already-normalized route matrix.
[[nodiscard]] std::optional<BeamSearchInput> assemble_beam_search_input(
    const domain::TripState& state,
    domain::UnixTimeMilliseconds current_time,
    domain::UnixTimeMilliseconds planning_horizon_start,
    domain::UnixTimeMilliseconds planning_horizon_end,
    const domain::TravelTimeMatrix& travel_time_matrix);

}  // namespace liveroute::planner
