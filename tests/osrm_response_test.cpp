#include <chrono>
#include <iostream>
#include <limits>
#include <string_view>

#include "liveroute/routing/osrm_response.hpp"

namespace {

using liveroute::routing::TravelTimeProviderError;

bool error_is(std::string_view body, TravelTimeProviderError expected) {
  const auto result = liveroute::routing::parse_osrm_table_response(body, 2);
  return !result.has_matrix() && result.error() == expected;
}

bool check_ok_and_rounding() {
  const auto result = liveroute::routing::parse_osrm_table_response(
      R"({"code":"Ok","durations":[[0,1.01],[null,0]],"distances":[[0,10.01],[null,0]],"sources":[],"destinations":[]})",
      2);
  return result.has_matrix() && result.matrix().location_count() == 2 &&
         result.matrix().at(0, 1).duration == std::chrono::seconds{2} &&
         result.matrix().at(0, 1).distance_meters == 11 &&
         result.matrix().at(0, 1).reachable &&
         !result.matrix().at(1, 0).reachable;
}

bool check_no_table() {
  const auto result = liveroute::routing::parse_osrm_table_response(
      R"({"code":"NoTable","message":"no route"})", 2);
  return result.has_matrix() && result.matrix().at(0, 0).reachable &&
         result.matrix().at(1, 1).reachable &&
         !result.matrix().at(0, 1).reachable &&
         !result.matrix().at(1, 0).reachable;
}

bool check_codes() {
  return liveroute::routing::is_recognized_osrm_error_response(
             R"({ "code" : "NoSegment", "message":"x" })") &&
         !liveroute::routing::is_recognized_osrm_error_response(
             R"({"code":"FutureCode"})") &&
         error_is(R"({"code":"NoSegment"})",
                  TravelTimeProviderError::kProviderUnavailable) &&
         error_is(R"({"code":"TooBig"})",
                  TravelTimeProviderError::kMatrixTooLarge) &&
         error_is(R"({"code":"InvalidQuery"})",
                  TravelTimeProviderError::kInternal) &&
         error_is(R"({"code":"NotImplemented"})",
                  TravelTimeProviderError::kInternal) &&
         error_is(R"({"code":"FutureCode"})",
                  TravelTimeProviderError::kInternal);
}

bool check_malformed_matrices() {
  constexpr auto unavailable = TravelTimeProviderError::kProviderUnavailable;
  return error_is("not-json", unavailable) &&
         error_is(R"({"code":"Ok","durations":[[0,1]],"distances":[[0,1]]})",
                  unavailable) &&
         error_is(R"({"code":"Ok","durations":[[0,null],[1,0]],"distances":[[0,1],[1,0]]})",
                  unavailable) &&
         error_is(R"({"code":"Ok","durations":[[0,-1],[1,0]],"distances":[[0,1],[1,0]]})",
                  unavailable) &&
         error_is(R"({"code":"Ok","durations":[[0,1],[1,0]],"distances":[[0,4294967295.1],[1,0]]})",
                  unavailable) &&
         error_is(R"({"code":"Ok","code":"NoTable"})", unavailable);
}

}  // namespace

int main() {
  if (!check_ok_and_rounding() || !check_no_table() || !check_codes() ||
      !check_malformed_matrices()) {
    std::cerr << "OSRM response test failed\n";
    return 1;
  }
  return 0;
}
