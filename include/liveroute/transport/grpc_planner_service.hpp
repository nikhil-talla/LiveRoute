#pragma once

#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <mutex>
#include <string>
#include <unordered_map>
#include <vector>

#include <grpcpp/support/server_callback.h>

#include "liveroute/runtime/concurrent_trip_runtime.hpp"
#include "liveroute/v1/planner.grpc.pb.h"

namespace liveroute::transport {

struct GrpcPlannerConfiguration {
  std::string cpp_instance_id;
  std::size_t outbound_queue_capacity{};
  std::uint32_t max_message_bytes{};
  std::uint32_t max_snapshot_bytes{};
  std::uint32_t max_active_trips{};
  std::chrono::milliseconds default_attempt_timeout{};
  std::size_t max_candidates{};
  std::size_t beam_width{};
  std::size_t max_expansions{};

  [[nodiscard]] bool is_valid() const noexcept;
};

class PlannerResponseSink {
 public:
  virtual ~PlannerResponseSink() = default;
  [[nodiscard]] virtual bool publish(
      runtime::RuntimePlanningDelivery delivery) = 0;
};

class PlannerResponseRouter {
 public:
  void bind(std::uint64_t stream_binding,
            std::weak_ptr<PlannerResponseSink> sink);
  void unbind(std::uint64_t stream_binding);
  [[nodiscard]] bool publish(runtime::RuntimePlanningDelivery delivery);

 private:
  std::mutex mutex_;
  std::unordered_map<std::uint64_t, std::weak_ptr<PlannerResponseSink>>
      bindings_;
};

[[nodiscard]] std::vector<std::string> required_v1_capabilities();

class GrpcPlannerService final
    : public ::liveroute::v1::LiveRoutePlanner::CallbackService {
 public:
  GrpcPlannerService(GrpcPlannerConfiguration configuration,
                     runtime::ConcurrentTripRuntime& runtime,
                     PlannerResponseRouter& response_router);

  ::grpc::ServerBidiReactor<::liveroute::v1::PlannerStreamRequest,
                            ::liveroute::v1::PlannerStreamResponse>*
  PlanTrips(::grpc::CallbackServerContext* context) override;

 private:
  GrpcPlannerConfiguration configuration_;
  runtime::ConcurrentTripRuntime& runtime_;
  PlannerResponseRouter& response_router_;
  std::atomic<std::uint64_t> next_stream_binding_{1};
};

}  // namespace liveroute::transport
