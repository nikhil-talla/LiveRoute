#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <memory>
#include <mutex>
#include <span>
#include <stop_token>
#include <string>

#include <grpcpp/create_channel.h>
#include <grpcpp/security/credentials.h>
#include <grpcpp/server.h>
#include <grpcpp/server_builder.h>

#include "liveroute/routing/travel_time_provider.hpp"
#include "liveroute/transport/grpc_planner_service.hpp"
#include "liveroute/v1/planner.grpc.pb.h"

namespace {

using namespace std::chrono_literals;
using liveroute::domain::Deadline;
using liveroute::domain::Location;
using liveroute::domain::TravelMode;
using liveroute::routing::TravelTimeLookupResult;
using liveroute::routing::TravelTimeProvider;
using liveroute::runtime::ConcurrentTripRuntime;
using liveroute::runtime::RuntimePlanningDelivery;
using liveroute::transport::GrpcPlannerService;
using liveroute::transport::PlannerResponseRouter;

constexpr std::string_view kTripId =
    "11111111-1111-1111-1111-111111111111";
constexpr std::string_view kOwnerId =
    "22222222-2222-2222-2222-222222222222";
constexpr std::string_view kActivityId =
    "33333333-3333-3333-3333-333333333333";
constexpr std::string_view kPlanId =
    "44444444-4444-4444-4444-444444444444";
constexpr std::string_view kEventId =
    "55555555-5555-5555-5555-555555555555";

[[nodiscard]] std::int64_t now_ms() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

class ToggleProvider final : public TravelTimeProvider {
 public:
  TravelTimeLookupResult get_matrix(
      std::span<const Location> locations, TravelMode,
      std::chrono::system_clock::time_point, Deadline,
      std::stop_token stop_token) override {
    std::unique_lock lock(mutex_);
    started_ = true;
    condition_.notify_all();
    if (block_) {
      std::stop_callback cancellation(
          stop_token, [this] { condition_.notify_all(); });
      condition_.wait(lock, [&] { return stop_token.stop_requested(); });
      cancelled_ = stop_token.stop_requested();
      condition_.notify_all();
      return TravelTimeLookupResult{
          liveroute::routing::TravelTimeProviderError::kCancelled};
    }
    std::vector<liveroute::domain::RouteEstimate> estimates(
        locations.size() * locations.size(),
        {.duration = std::chrono::seconds{0},
         .distance_meters = 0,
         .reachable = true});
    return TravelTimeLookupResult{
        liveroute::domain::TravelTimeMatrix{
            locations.size(), std::move(estimates)}};
  }

  void block() {
    std::scoped_lock lock(mutex_);
    block_ = true;
    started_ = false;
    cancelled_ = false;
  }

  [[nodiscard]] bool wait_started() {
    std::unique_lock lock(mutex_);
    return condition_.wait_for(lock, 2s, [&] { return started_; });
  }

  [[nodiscard]] bool wait_cancelled() {
    std::unique_lock lock(mutex_);
    return condition_.wait_for(lock, 2s, [&] { return cancelled_; });
  }

 private:
  std::mutex mutex_;
  std::condition_variable condition_;
  bool block_{};
  bool started_{};
  bool cancelled_{};
};

void fill_open(::liveroute::v1::PlannerStreamRequest& request,
               std::string request_id) {
  request.set_request_id(std::move(request_id));
  auto* open = request.mutable_open_stream();
  open->set_backend_instance_id("phase8-test");
  open->set_protocol_version("liveroute.v1");
  for (const auto& capability :
       liveroute::transport::required_v1_capabilities()) {
    open->add_capabilities(capability);
  }
}

void fill_bootstrap(::liveroute::v1::PlannerStreamRequest& request,
                    std::string request_id,
                    std::uint64_t observation_sequence) {
  const auto now = now_ms();
  request.set_request_id(std::move(request_id));
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(7);
  request.set_expires_at_unix_ms(now + 5000);
  auto* bootstrap = request.mutable_bootstrap_trip();
  bootstrap->set_finalized_mutation_sequence(1);
  bootstrap->set_trip_revision(1);
  bootstrap->set_current_observation_sequence(observation_sequence);
  if (observation_sequence != 0) {
    auto* observation = bootstrap->mutable_current_observation();
    observation->mutable_location()->set_latitude(40.0);
    observation->mutable_location()->set_longitude(-74.0);
    observation->set_observed_at_unix_ms(now);
  }
  auto* trip = bootstrap->mutable_full_trip();
  trip->set_trip_id(kTripId);
  trip->set_owner_user_id(kOwnerId);
  trip->set_default_time_zone_name("America/New_York");
  trip->set_current_plan_id(kPlanId);
  auto* activity = trip->add_activities();
  activity->set_activity_id(kActivityId);
  activity->set_place_id("place-1");
  activity->set_display_name("Test activity");
  activity->mutable_location()->set_latitude(40.01);
  activity->mutable_location()->set_longitude(-74.01);
  activity->set_time_zone_name("America/New_York");
  activity->set_inbound_travel_mode(
      ::liveroute::v1::TRAVEL_MODE_WALKING);
  activity->set_activity_class(
      ::liveroute::v1::ACTIVITY_CLASS_FLEXIBLE);
  activity->set_activity_state(
      ::liveroute::v1::ACTIVITY_STATE_PLANNED);
  activity->set_priority_rank(1);
  activity->set_utility_score(10);
  auto* timing = activity->mutable_timing();
  auto* window = timing->add_open_windows();
  window->set_opens_at_unix_ms(now - 60000);
  window->set_closes_at_unix_ms(now + 3600000);
  timing->set_min_duration_seconds(60);
  timing->set_preferred_duration_seconds(60);
  timing->set_max_duration_seconds(60);
  timing->set_can_move(true);
  timing->set_can_skip(true);

  auto* plan = bootstrap->mutable_current_plan();
  plan->set_plan_id(kPlanId);
  plan->set_plan_revision(1);
  plan->set_origin(
      ::liveroute::v1::PLAN_ORIGIN_USER_AUTHORED);
  plan->set_created_at_unix_ms(now);
  auto* segment = plan->add_segments();
  segment->set_activity_id(kActivityId);
  segment->set_state(
      ::liveroute::v1::PLAN_ENTRY_STATE_SCHEDULED);
  segment->set_scheduled_start_unix_ms(now + 600000);
  segment->set_scheduled_end_unix_ms(now + 660000);
}

void fill_location_event(
    ::liveroute::v1::PlannerStreamRequest& request,
    std::string request_id, std::uint64_t observation_sequence) {
  const auto now = now_ms();
  request.set_request_id(std::move(request_id));
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(7);
  request.set_observation_sequence(observation_sequence);
  request.set_expires_at_unix_ms(now + 3600000);
  auto* event = request.mutable_apply_event();
  event->set_event_id(kEventId);
  event->set_occurred_at_unix_ms(now);
  auto* location = event->mutable_location_updated()->mutable_location();
  location->set_latitude(40.0);
  location->set_longitude(-74.0);
}

void fill_route_deviation_event(
    ::liveroute::v1::PlannerStreamRequest& request,
    std::string request_id, std::uint64_t observation_sequence) {
  fill_location_event(
      request, std::move(request_id), observation_sequence);
  auto* deviation =
      request.mutable_apply_event()->mutable_route_deviation_detected();
  deviation->mutable_location()->set_latitude(40.0);
  deviation->mutable_location()->set_longitude(-74.0);
  deviation->set_distance_from_route_meters(25);
}

}  // namespace

int main() {
  ToggleProvider provider;
  PlannerResponseRouter router;
  ConcurrentTripRuntime runtime(
      {.shard_count = 2,
       .max_active_trips = 4,
       .shard_queue_capacities = {8, 8, 8, 8},
       .completion_queue_capacity = 8,
       .priority_fairness_burst = 4,
       .provider_workers = 2,
       .provider_queue_capacity = 8,
       .planner_workers = 2,
       .planner_queue_capacity = 8,
       .essential_response_capacity = 16,
       .max_advisory_payload_bytes = 1024},
      provider, [&router](RuntimePlanningDelivery delivery) {
        return router.publish(std::move(delivery));
      });
  GrpcPlannerService service(
      {.cpp_instance_id = "phase8-test-cpp",
       .outbound_queue_capacity = 16,
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
  builder.AddListeningPort("127.0.0.1:0",
                           ::grpc::InsecureServerCredentials(), &port);
  builder.RegisterService(&service);
  auto server = builder.BuildAndStart();
  if (!server || port == 0) return 1;
  auto channel = ::grpc::CreateChannel(
      "127.0.0.1:" + std::to_string(port),
      ::grpc::InsecureChannelCredentials());
  auto stub =
      ::liveroute::v1::LiveRoutePlanner::NewStub(std::move(channel));

  auto open_stream = [&](
                         ::grpc::ClientContext& context) {
    context.set_deadline(std::chrono::system_clock::now() + 5s);
    auto stream = stub->PlanTrips(&context);
    ::liveroute::v1::PlannerStreamRequest request;
    fill_open(request, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa");
    if (!stream->Write(request)) return decltype(stream){};
    ::liveroute::v1::PlannerStreamResponse response;
    if (!stream->Read(&response) || !response.has_stream_ready() ||
        response.stream_ready().status() !=
            ::liveroute::v1::STATUS_CODE_OK) {
      return decltype(stream){};
    }
    return stream;
  };

  ::grpc::ClientContext first_context;
  auto first_stream = open_stream(first_context);
  if (!first_stream) return 1;
  ::liveroute::v1::PlannerStreamRequest request;
  fill_bootstrap(request, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", 0);
  if (!first_stream->Write(request)) return 1;
  ::liveroute::v1::PlannerStreamResponse response;
  if (!first_stream->Read(&response) ||
      !response.has_trip_bootstrapped() ||
      response.trip_bootstrapped().status() !=
          ::liveroute::v1::STATUS_CODE_OK ||
      response.trip_bootstrapped().current_plan_id() != kPlanId) {
    return 1;
  }

  request.Clear();
  fill_route_deviation_event(
      request, "cccccccc-cccc-cccc-cccc-cccccccccccc", 1);
  if (!first_stream->Write(request)) return 1;
  bool acknowledged = false;
  bool proposed = false;
  for (int index = 0; index < 2; ++index) {
    if (!first_stream->Read(&response) ||
        response.request_id() !=
            "cccccccc-cccc-cccc-cccc-cccccccccccc") {
      return 1;
    }
    acknowledged = acknowledged || response.has_event_acknowledged();
    proposed = proposed ||
               (response.has_replan_result() &&
                response.replan_result().has_proposal());
  }
  if (!acknowledged || !proposed) return 1;
  first_stream->WritesDone();
  if (!first_stream->Finish().ok()) return 1;

  ::grpc::ClientContext second_context;
  auto second_stream = open_stream(second_context);
  if (!second_stream) return 1;
  request.Clear();
  fill_bootstrap(request, "dddddddd-dddd-dddd-dddd-dddddddddddd", 1);
  if (!second_stream->Write(request) ||
      !second_stream->Read(&response) ||
      !response.has_trip_bootstrapped() ||
      response.trip_bootstrapped().status() !=
          ::liveroute::v1::STATUS_CODE_DUPLICATE) {
    return 1;
  }
  if (!second_stream->Read(&response) ||
      response.request_id() !=
          "dddddddd-dddd-dddd-dddd-dddddddddddd" ||
      !response.has_replan_result() ||
      !response.replan_result().has_proposal()) {
    return 1;
  }

  request.Clear();
  fill_location_event(
      request, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", 1);
  if (!second_stream->Write(request) ||
      !second_stream->Read(&response) ||
      !response.has_event_acknowledged() ||
      response.event_acknowledged().status() !=
          ::liveroute::v1::STATUS_CODE_STALE ||
      response.planner_state_version() != 1) {
    return 1;
  }

  request.Clear();
  fill_location_event(
      request, "18181818-1818-1818-1818-181818181818", 2);
  request.set_expires_at_unix_ms(now_ms() - 1);
  if (!second_stream->Write(request) ||
      !second_stream->Read(&response) || !response.has_error() ||
      response.error().status() !=
          ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED ||
      response.planner_state_version() != 0) {
    // Protocol-level expiry is rejected before runtime admission; the error
    // envelope therefore intentionally carries no runtime version claim.
    return 1;
  }

  provider.block();
  request.Clear();
  fill_route_deviation_event(
      request, "ffffffff-ffff-ffff-ffff-ffffffffffff", 2);
  if (!second_stream->Write(request) ||
      !second_stream->Read(&response) ||
      !response.has_event_acknowledged() ||
      !provider.wait_started()) {
    return 1;
  }
  second_context.TryCancel();
  if (!provider.wait_cancelled()) return 1;
  (void)second_stream->Finish();

  ::grpc::ClientContext third_context;
  auto third_stream = open_stream(third_context);
  if (!third_stream) return 1;
  request.Clear();
  fill_bootstrap(request, "12121212-1212-1212-1212-121212121212", 2);
  if (!third_stream->Write(request) ||
      !third_stream->Read(&response) ||
      !response.has_trip_bootstrapped() ||
      response.trip_bootstrapped().status() !=
          ::liveroute::v1::STATUS_CODE_DUPLICATE) {
    return 1;
  }

  request.Clear();
  request.set_request_id("13131313-1313-1313-1313-131313131313");
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(7);
  request.set_expires_at_unix_ms(now_ms() + 5000);
  request.mutable_confirm_finalized_mutations()
      ->set_finalized_mutation_sequence(1);
  if (!third_stream->Write(request) ||
      !third_stream->Read(&response) ||
      !response.has_finalized_mutations_acknowledged() ||
      response.finalized_mutations_acknowledged().status() !=
          ::liveroute::v1::STATUS_CODE_DUPLICATE) {
    return 1;
  }

  request.Clear();
  request.set_request_id("14141414-1414-1414-1414-141414141414");
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(7);
  request.set_expires_at_unix_ms(now_ms() + 5000);
  auto* snapshot_request = request.mutable_request_snapshot();
  snapshot_request->set_reason(
      ::liveroute::v1::SNAPSHOT_REASON_PERIODIC);
  snapshot_request->set_minimum_finalized_mutation_sequence(1);
  snapshot_request->set_minimum_planner_state_version(2);
  if (!third_stream->Write(request) ||
      !third_stream->Read(&response) ||
      !response.has_trip_snapshot() ||
      response.trip_snapshot().status() !=
          ::liveroute::v1::STATUS_CODE_OK ||
      !response.trip_snapshot().has_snapshot() ||
      response.trip_snapshot().snapshot().snapshot_schema_version() != 1 ||
      response.trip_snapshot().snapshot().payload_size_bytes() !=
          response.trip_snapshot().snapshot().payload().size() ||
      response.trip_snapshot().snapshot().checksum_sha256().size() != 32) {
    return 1;
  }

  request.Clear();
  request.set_request_id("15151515-1515-1515-1515-151515151515");
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(7);
  request.set_expires_at_unix_ms(now_ms() + 5000);
  auto* deactivate = request.mutable_deactivate_trip();
  deactivate->set_reason(
      ::liveroute::v1::DEACTIVATION_REASON_BACKEND_REQUEST);
  deactivate->set_final_snapshot_required(true);
  if (!third_stream->Write(request)) return 1;
  bool final_snapshot = false;
  bool deactivated = false;
  ::liveroute::v1::SnapshotBlob final_snapshot_blob;
  for (int index = 0; index < 2; ++index) {
    if (!third_stream->Read(&response) ||
        response.request_id() !=
            "15151515-1515-1515-1515-151515151515") {
      return 1;
    }
    final_snapshot =
        final_snapshot ||
        (response.has_trip_snapshot() &&
         response.trip_snapshot().status() ==
             ::liveroute::v1::STATUS_CODE_OK);
    if (response.has_trip_snapshot() &&
        response.trip_snapshot().has_snapshot()) {
      final_snapshot_blob = response.trip_snapshot().snapshot();
    }
    deactivated =
        deactivated ||
        (response.has_trip_deactivated() &&
         response.trip_deactivated().status() ==
             ::liveroute::v1::STATUS_CODE_OK &&
         response.trip_deactivated().final_snapshot_produced());
  }
  if (!final_snapshot || !deactivated ||
      runtime.active_trip_count() != 0) {
    return 1;
  }
  third_stream->WritesDone();
  if (!third_stream->Finish().ok()) return 1;

  ::grpc::ClientContext fourth_context;
  auto fourth_stream = open_stream(fourth_context);
  if (!fourth_stream) return 1;
  request.Clear();
  fill_bootstrap(request, "19191919-1919-1919-1919-191919191919", 1);
  request.set_runtime_epoch(8);
  if (!fourth_stream->Write(request) ||
      !fourth_stream->Read(&response) ||
      !response.has_trip_bootstrapped() ||
      response.trip_bootstrapped().status() !=
          ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT ||
      runtime.active_trip_count() != 0) {
    return 1;
  }

  request.Clear();
  request.set_request_id("16161616-1616-1616-1616-161616161616");
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(8);
  request.set_expires_at_unix_ms(now_ms() + 5000);
  auto* snapshot_bootstrap = request.mutable_bootstrap_trip();
  *snapshot_bootstrap->mutable_snapshot() = final_snapshot_blob;
  snapshot_bootstrap->set_finalized_mutation_sequence(
      final_snapshot_blob.covered_finalized_mutation_sequence());
  snapshot_bootstrap->set_trip_revision(
      final_snapshot_blob.trip_revision());
  if (!fourth_stream->Write(request) ||
      !fourth_stream->Read(&response) ||
      !response.has_trip_bootstrapped() ||
      response.trip_bootstrapped().status() !=
          ::liveroute::v1::STATUS_CODE_OK ||
      response.trip_bootstrapped().current_plan_id() != kPlanId) {
    return 1;
  }

  request.Clear();
  request.set_request_id("17171717-1717-1717-1717-171717171717");
  request.set_trip_id(kTripId);
  request.set_runtime_epoch(8);
  request.set_expires_at_unix_ms(now_ms() + 5000);
  request.mutable_deactivate_trip()->set_reason(
      ::liveroute::v1::DEACTIVATION_REASON_BACKEND_REQUEST);
  if (!fourth_stream->Write(request) ||
      !fourth_stream->Read(&response) ||
      !response.has_trip_deactivated() ||
      response.trip_deactivated().status() !=
          ::liveroute::v1::STATUS_CODE_OK) {
    return 1;
  }
  fourth_stream->WritesDone();
  if (!fourth_stream->Finish().ok()) return 1;

  server->Shutdown();
  runtime.stop_accepting();
  return 0;
}
