#pragma once

#include <cstddef>
#include <variant>
#include <vector>
#include <string_view>

#include "liveroute/routing/travel_time_provider.hpp"

namespace liveroute::routing {

struct OsrmTableGrid {
  std::size_t row_count{};
  std::size_t column_count{};
  std::vector<domain::RouteEstimate> estimates;
  bool no_table{};

  [[nodiscard]] const domain::RouteEstimate& at(std::size_t row,
                                                 std::size_t column) const;
};

using OsrmTableGridResult =
    std::variant<OsrmTableGrid, TravelTimeProviderError>;

// Converts a complete, byte-limited OSRM Table response into a rectangular
// source-by-destination grid. The square adapter below is retained for the
// existing provider contract and delegates to this parser.
[[nodiscard]] OsrmTableGridResult parse_osrm_table_response_grid(
    std::string_view response, std::size_t expected_row_count,
    std::size_t expected_column_count);

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
