#include <chrono>
#include <cstdint>
#include <iostream>
#include <string>

#include "liveroute/v1/planner.pb.h"

int main() {
  ::liveroute::v1::PlannerStreamRequest request;
  request.set_request_id("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa");
  request.set_trip_id("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb");
  request.set_runtime_epoch(1);
  request.set_observation_sequence(1);
  request.set_expires_at_unix_ms(1);
  auto* event = request.mutable_apply_event();
  event->set_event_id("cccccccc-cccc-cccc-cccc-cccccccccccc");
  event->mutable_location_updated()
      ->mutable_location()
      ->set_latitude(40.0);
  event->mutable_location_updated()
      ->mutable_location()
      ->set_longitude(-74.0);

  constexpr std::uint64_t kIterations = 100000;
  std::string bytes;
  ::liveroute::v1::PlannerStreamRequest decoded;
  const auto request_start = std::chrono::steady_clock::now();
  for (std::uint64_t index = 0; index < kIterations; ++index) {
    bytes.clear();
    if (!request.SerializeToString(&bytes) ||
        !decoded.ParseFromString(bytes)) {
      return 1;
    }
  }
  const auto request_elapsed =
      std::chrono::duration_cast<std::chrono::nanoseconds>(
          std::chrono::steady_clock::now() - request_start);
  const auto request_bytes = bytes.size();

  ::liveroute::v1::PlannerStreamResponse response;
  response.set_request_id(request.request_id());
  response.set_trip_id(request.trip_id());
  response.set_runtime_epoch(1);
  response.set_accepted_observation_sequence(1);
  response.set_planner_state_version(1);
  response.mutable_event_acknowledged()->set_status(
      ::liveroute::v1::STATUS_CODE_OK);
  response.mutable_event_acknowledged()->set_disposition(
      ::liveroute::v1::EVENT_DISPOSITION_ACCEPTED);
  ::liveroute::v1::PlannerStreamResponse decoded_response;
  const auto response_start = std::chrono::steady_clock::now();
  for (std::uint64_t index = 0; index < kIterations; ++index) {
    bytes.clear();
    if (!response.SerializeToString(&bytes) ||
        !decoded_response.ParseFromString(bytes)) {
      return 1;
    }
  }
  const auto response_elapsed =
      std::chrono::duration_cast<std::chrono::nanoseconds>(
          std::chrono::steady_clock::now() - response_start);
  std::cout << "iterations=" << kIterations
            << " request_roundtrip_elapsed_ns="
            << request_elapsed.count()
            << " request_bytes=" << request_bytes
            << " response_roundtrip_elapsed_ns="
            << response_elapsed.count()
            << " response_bytes=" << bytes.size() << '\n';
  return 0;
}
