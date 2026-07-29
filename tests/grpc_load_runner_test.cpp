#include <chrono>
#include <cstddef>
#include <span>
#include <stop_token>
#include <string>

#include <grpcpp/server.h>
#include <grpcpp/server_builder.h>

#include "liveroute/load/grpc_runner.hpp"
#include "liveroute/routing/travel_time_provider.hpp"
#include "liveroute/transport/grpc_planner_service.hpp"

namespace {

using namespace std::chrono_literals;

class TestProvider final
    : public liveroute::routing::TravelTimeProvider {
 public:
  liveroute::routing::TravelTimeLookupResult get_matrix(
      std::span<const liveroute::domain::Location> locations,
      liveroute::domain::TravelMode,
      std::chrono::system_clock::time_point,
      liveroute::domain::Deadline,
      std::stop_token stop_token) override {
    if (stop_token.stop_requested()) {
      return liveroute::routing::TravelTimeLookupResult{
          liveroute::routing::TravelTimeProviderError::kCancelled};
    }
    std::vector<liveroute::domain::RouteEstimate> estimates(
        locations.size() * locations.size(),
        {.duration = std::chrono::seconds{0},
         .distance_meters = 0,
         .reachable = true});
    return liveroute::routing::TravelTimeLookupResult{
        liveroute::domain::TravelTimeMatrix{
            locations.size(), std::move(estimates)}};
  }
};

}  // namespace

int main() {
  TestProvider provider;
  liveroute::transport::PlannerResponseRouter router;
  liveroute::runtime::ConcurrentTripRuntime runtime(
      {.shard_count = 2,
       .max_active_trips = 4,
       .shard_queue_capacities = {32, 32, 32, 32},
       .completion_queue_capacity = 32,
       .priority_fairness_burst = 4,
       .provider_workers = 2,
       .provider_queue_capacity = 32,
       .planner_workers = 2,
       .planner_queue_capacity = 32,
       .essential_response_capacity = 64,
       .max_advisory_payload_bytes = 1024},
      provider,
      [&router](liveroute::runtime::RuntimePlanningDelivery delivery) {
        return router.publish(std::move(delivery));
      });
  liveroute::transport::GrpcPlannerService service(
      {.cpp_instance_id = "grpc-load-test",
       .outbound_queue_capacity = 64,
       .max_message_bytes = 4194304,
       .max_snapshot_bytes = 2097152,
       .max_active_trips = 4,
       .default_attempt_timeout = 2s,
       .max_candidates = 128,
       .beam_width = 8,
       .max_expansions = 256},
      runtime, router);

  int port = 0;
  ::grpc::ServerBuilder builder;
  builder.AddListeningPort(
      "127.0.0.1:0", ::grpc::InsecureServerCredentials(), &port);
  builder.RegisterService(&service);
  auto server = builder.BuildAndStart();
  if (!server || port == 0) return 1;

  liveroute::load::WorkloadConfiguration configuration{
      .profile = liveroute::load::WorkloadProfile::kManyTrips,
      .seed = 1,
      .active_trips = 4,
      .event_count = 4,
      .events_per_second = 100000,
      .location_burst_size = 4,
      .suffix_size = 4,
      .reservation_percent = 75,
      .deadline = 5s,
      .shard_count = 2,
      .worker_count = 2,
  };
  const auto result = liveroute::load::run_grpc_workload(
      "127.0.0.1:" + std::to_string(port), configuration);

  server->Shutdown();
  runtime.stop_accepting();
  if (!result.completed() ||
      result.submitted != configuration.event_count ||
      result.acknowledged != configuration.event_count ||
      result.scheduled_replans != configuration.event_count ||
      result.replan_results != configuration.event_count ||
      result.acknowledgement_latencies_us.size() !=
          configuration.event_count ||
      result.replan_latencies_us.size() !=
          configuration.event_count) {
    return 1;
  }
  return 0;
}
