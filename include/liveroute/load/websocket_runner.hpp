#pragma once

#include <cstddef>
#include <cstdint>
#include <iosfwd>
#include <string>
#include <vector>

#include "liveroute/load/workload.hpp"

namespace liveroute::load {

[[nodiscard]] bool websocket_transport_available() noexcept;

struct WebSocketLoadResult {
  bool connected{};
  bool authenticated{};
  bool trips_created{};
  bool subscribed{};
  bool transport_ok{};
  std::size_t submitted{};
  std::size_t acknowledged{};
  std::size_t protocol_errors{};
  std::size_t plan_notifications{};
  std::vector<std::uint64_t> acknowledgement_latencies_us;
  std::uint64_t elapsed_microseconds{};
  std::string transport_message;

  [[nodiscard]] bool completed() const noexcept;
};

[[nodiscard]] WebSocketLoadResult run_websocket_workload(
    const std::string& target, const std::string& token_file,
    const WorkloadConfiguration& configuration);

void write_websocket_load_report(
    std::ostream& output, const std::string& target,
    const WorkloadConfiguration& configuration,
    const WebSocketLoadResult& result);

}  // namespace liveroute::load
