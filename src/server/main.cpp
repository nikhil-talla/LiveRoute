#include <iostream>
#include <cstdlib>
#include <string>
#include <string_view>

namespace {

constexpr std::string_view kServiceName = "liveroute-planner";

}  // namespace

#ifdef LIVEROUTE_ENABLE_GRPC

#include <chrono>
#include <memory>
#include <span>
#include <stop_token>

#include <grpcpp/server.h>
#include <grpcpp/server_builder.h>
#include <grpcpp/health_check_service_interface.h>

#include "liveroute/routing/travel_time_provider.hpp"
#ifdef LIVEROUTE_ENABLE_OSRM
#include "liveroute/routing/osrm_travel_time_provider.hpp"
#endif
#include "liveroute/transport/grpc_planner_service.hpp"

namespace {

class UnavailableTravelTimeProvider final
    : public liveroute::routing::TravelTimeProvider {
 public:
  liveroute::routing::TravelTimeLookupResult get_matrix(
      std::span<const liveroute::domain::Location>,
      liveroute::domain::TravelMode,
      std::chrono::system_clock::time_point, liveroute::domain::Deadline,
      std::stop_token) override {
    return liveroute::routing::TravelTimeLookupResult{
        liveroute::routing::TravelTimeProviderError::
            kProviderUnavailable};
  }
};

[[nodiscard]] std::unique_ptr<liveroute::routing::TravelTimeProvider>
make_travel_time_provider() {
#ifdef LIVEROUTE_ENABLE_OSRM
  const auto endpoint = [](const char* name, const char* fallback) {
    const auto* value = std::getenv(name);
    return std::string{value == nullptr ? fallback : value};
  };
  return std::make_unique<liveroute::routing::OsrmTravelTimeProvider>(
      liveroute::routing::OsrmTravelTimeProviderConfig{
          .dataset_version = endpoint(
              "LIVEROUTE_OSRM_DATASET_VERSION",
              "geofabrik-rhode-island-260701-osrm-v26.5.0-mld-v1"),
          .car_endpoint = endpoint("LIVEROUTE_OSRM_CAR_ENDPOINT",
                                   "http://osrm-car:5000"),
          .foot_endpoint = endpoint("LIVEROUTE_OSRM_FOOT_ENDPOINT",
                                    "http://osrm-foot:5000"),
          .max_locations = 65,
          .max_matrix_cells = 4225,
          .max_encoded_request_bytes = 8192,
          .max_response_bytes = 1048576,
          .per_profile_concurrency = 2,
          .connect_timeout = std::chrono::milliseconds{100},
          .request_timeout = std::chrono::milliseconds{750},
          .route_cache = liveroute::routing::RouteMatrixCacheConfig{},
      });
#else
  return std::make_unique<UnavailableTravelTimeProvider>();
#endif
}

[[nodiscard]] int serve(std::string address) {
  using liveroute::runtime::ConcurrentTripRuntime;
  using liveroute::transport::GrpcPlannerService;
  using liveroute::transport::PlannerResponseRouter;

  auto provider = make_travel_time_provider();
  PlannerResponseRouter router;
  ConcurrentTripRuntime runtime(
      {.shard_count = 2,
       .max_active_trips = 128,
       .shard_queue_capacities = {64, 64, 128, 32},
       .completion_queue_capacity = 128,
       .priority_fairness_burst = 16,
       .provider_workers = 2,
       .provider_queue_capacity = 32,
       .planner_workers = 2,
       .planner_queue_capacity = 32,
       .essential_response_capacity = 128,
       .max_advisory_payload_bytes = 262144},
      *provider,
      [&router](liveroute::runtime::RuntimePlanningDelivery delivery) {
        return router.publish(std::move(delivery));
      });
  GrpcPlannerService service(
      {.cpp_instance_id = "liveroute-cpp-local-v1",
       .outbound_queue_capacity = 128,
       .max_message_bytes = 4194304,
       .max_snapshot_bytes = 2097152,
       .max_active_trips = 128,
       .default_attempt_timeout = std::chrono::milliseconds{1000},
       .max_candidates = 4096,
       .beam_width = 32,
       .max_expansions = 16384},
      runtime, router);

  ::grpc::EnableDefaultHealthCheckService(true);
  ::grpc::ServerBuilder builder;
  builder.SetMaxReceiveMessageSize(4194304);
  builder.SetMaxSendMessageSize(4194304);
  builder.AddListeningPort(address, ::grpc::InsecureServerCredentials());
  builder.RegisterService(&service);
  auto server = builder.BuildAndStart();
  if (!server) {
    std::cerr << "failed to start " << kServiceName << " on "
              << address << '\n';
    return 1;
  }
  if (auto* health = server->GetHealthCheckService(); health != nullptr) {
    health->SetServingStatus("liveroute.v1.LiveRoutePlanner", true);
    health->SetServingStatus("liveroute.v1.LiveRoutePlanner/osrm-car", true);
    health->SetServingStatus("liveroute.v1.LiveRoutePlanner/osrm-foot", true);
  }
  std::cout << kServiceName << " listening on " << address << '\n';
  server->Wait();
  runtime.stop_accepting();
  return 0;
}

}  // namespace

#endif

int main(int argc, char* argv[]) {
  if (argc == 2 && std::string_view{argv[1]} == "--self-check") {
    std::cout << kServiceName << " build and transport are healthy\n";
    return 0;
  }

#ifdef LIVEROUTE_ENABLE_GRPC
  std::string address = "127.0.0.1:50051";
  if (argc == 2) {
    constexpr std::string_view kPrefix = "--listen=";
    const std::string_view argument{argv[1]};
    if (!argument.starts_with(kPrefix) ||
        argument.size() == kPrefix.size()) {
      std::cerr << "usage: " << argv[0]
                << " [--self-check|--listen=ADDRESS]\n";
      return 2;
    }
    address = argument.substr(kPrefix.size());
  } else if (argc != 1) {
    std::cerr << "usage: " << argv[0]
              << " [--self-check|--listen=ADDRESS]\n";
    return 2;
  }
  return serve(std::move(address));
#else
  std::cerr << kServiceName
            << " was built without the pinned gRPC transport; use "
               "--self-check or configure LIVEROUTE_ENABLE_GRPC=ON.\n";
  return 1;
#endif
}
