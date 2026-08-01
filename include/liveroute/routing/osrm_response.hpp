#pragma once

#include <cstddef>
#include <string_view>

#include "liveroute/routing/travel_time_provider.hpp"

namespace liveroute::routing {

// Converts one complete, byte-limited OSRM Table response into the internal
// matrix/error contract. HTTP status, transport, cancellation, and byte-limit
// precedence are handled by the HTTP adapter before this function is called.
[[nodiscard]] TravelTimeLookupResult parse_osrm_table_response(
    std::string_view response, std::size_t expected_location_count);

// True only when the complete JSON body contains one of the pinned OSRM error
// codes covered by the public mapping table. It is used to give that mapping
// precedence over generic HTTP 4xx handling.
[[nodiscard]] bool is_recognized_osrm_error_response(
    std::string_view response);

}  // namespace liveroute::routing
