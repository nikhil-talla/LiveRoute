#include "liveroute/load/grpc_runner.hpp"

#include <charconv>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <iostream>
#include <limits>
#include <optional>
#include <string>
#include <string_view>

#include "liveroute/v1/planner.pb.h"

namespace {

using namespace std::chrono_literals;
using liveroute::load::WorkloadConfiguration;
using liveroute::load::WorkloadProfile;

[[nodiscard]] std::optional<std::size_t> parse_size(
    std::string_view value) {
  std::size_t result = 0;
  const auto parsed =
      std::from_chars(value.data(), value.data() + value.size(), result);
  if (parsed.ec != std::errc{} ||
      parsed.ptr != value.data() + value.size()) {
    return std::nullopt;
  }
  return result;
}

[[nodiscard]] bool parse_arguments(
    int argc, char** argv, std::string* target,
    WorkloadConfiguration* configuration, bool* self_check) {
  for (int index = 1; index < argc; ++index) {
    const std::string_view argument{argv[index]};
    if (argument == "--self-check") {
      *self_check = true;
      continue;
    }
    if (argument == "--help" || index + 1 >= argc) return false;
    const std::string_view value{argv[++index]};
    if (argument == "--target") {
      *target = value;
      continue;
    }
    if (argument == "--profile") {
      const auto parsed =
          liveroute::load::parse_workload_profile(value);
      if (!parsed) return false;
      configuration->profile = *parsed;
      continue;
    }
    const auto parsed = parse_size(value);
    if (!parsed) return false;
    if (argument == "--seed") configuration->seed = *parsed;
    else if (argument == "--trips") {
      configuration->active_trips = *parsed;
    } else if (argument == "--events") {
      configuration->event_count = *parsed;
    } else if (argument == "--events-per-second") {
      configuration->events_per_second = *parsed;
    } else if (argument == "--burst-size") {
      configuration->location_burst_size = *parsed;
    } else if (argument == "--suffix-size") {
      configuration->suffix_size = *parsed;
    } else if (argument == "--reservation-percent") {
      if (*parsed > std::numeric_limits<std::uint32_t>::max()) {
        return false;
      }
      configuration->reservation_percent =
          static_cast<std::uint32_t>(*parsed);
    } else if (argument == "--deadline-ms") {
      configuration->deadline = std::chrono::milliseconds{*parsed};
    } else {
      return false;
    }
  }
  if (*self_check) {
    configuration->profile = WorkloadProfile::kManyTrips;
    configuration->seed = 1;
    configuration->active_trips = 4;
    configuration->event_count = 32;
    configuration->events_per_second = 100000;
    configuration->location_burst_size = 4;
    configuration->suffix_size = 4;
    configuration->deadline = 5s;
  }
  return !target->empty() && configuration->is_valid();
}

void usage() {
  std::cerr
      << "usage: liveroute_grpc_loadgen --target HOST:PORT"
         " [--profile NAME] [--seed N] [--trips N] [--events N]"
         " [--events-per-second N] [--burst-size N]"
         " [--suffix-size N] [--reservation-percent N]"
         " [--deadline-ms N] [--self-check]\n";
}

}  // namespace

int main(int argc, char** argv) {
  std::string target;
  WorkloadConfiguration configuration;
  bool self_check = false;
  if (!parse_arguments(
          argc, argv, &target, &configuration, &self_check)) {
    usage();
    return 2;
  }
  const auto result =
      liveroute::load::run_grpc_workload(target, configuration);
  liveroute::load::write_grpc_load_report(
      std::cout, target, configuration, result);
  if (self_check &&
      (result.acknowledged != configuration.event_count ||
       result.protocol_errors != 0 ||
       result.acknowledgement_statuses[
           ::liveroute::v1::STATUS_CODE_OK] !=
           configuration.event_count ||
       result.replan_results == 0)) {
    return 3;
  }
  return result.completed() ? 0 : 3;
}
