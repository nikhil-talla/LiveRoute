#pragma once

#include <cstddef>
#include <cstdint>
#include <iosfwd>
#include <string>
#include <vector>

#include "liveroute/load/workload.hpp"

namespace liveroute::load {

struct GrpcLoadResult {
  bool stream_opened{};
  bool bootstrapped{};
  bool primed{};
  bool transport_ok{};
  std::size_t submitted{};
  std::size_t acknowledged{};
  std::size_t scheduled_replans{};
  std::size_t replan_results{};
  std::size_t protocol_errors{};
  std::size_t superseded_replan_correlations{};
  std::vector<std::uint64_t> acknowledgement_statuses;
  std::vector<std::uint64_t> replan_statuses;
  std::vector<std::uint64_t> error_statuses;
  std::vector<std::uint64_t> acknowledgement_latencies_us;
  std::vector<std::uint64_t> replan_latencies_us;
  std::uint64_t elapsed_microseconds{};
  std::string transport_message;

  [[nodiscard]] bool completed() const noexcept;
};

[[nodiscard]] GrpcLoadResult run_grpc_workload(
    const std::string& target,
    const WorkloadConfiguration& configuration);

void write_grpc_load_report(
    std::ostream& output, const std::string& target,
    const WorkloadConfiguration& configuration,
    const GrpcLoadResult& result);

}  // namespace liveroute::load
