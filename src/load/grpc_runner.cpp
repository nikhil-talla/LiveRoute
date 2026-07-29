#include "liveroute/load/grpc_runner.hpp"

#include <algorithm>
#include <array>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <iomanip>
#include <memory>
#include <mutex>
#include <ostream>
#include <sstream>
#include <string>
#include <thread>
#include <unordered_map>

#include <grpcpp/create_channel.h>
#include <grpcpp/security/credentials.h>

#include "liveroute/transport/grpc_planner_service.hpp"
#include "liveroute/v1/planner.grpc.pb.h"

namespace liveroute::load {
namespace {

using namespace std::chrono_literals;

constexpr std::size_t kStatusCount = 23;
constexpr std::uint64_t kBootstrapRequestBase = 1000000;
constexpr std::uint64_t kPrimeRequestBase = 2000000;
constexpr std::uint64_t kEventRequestBase = 3000000;
constexpr std::uint64_t kTripBase = 4000000;
constexpr std::uint64_t kPlanBase = 5000000;
constexpr std::uint64_t kActivityBase = 6000000;
constexpr std::uint64_t kEventBase = 7000000;

[[nodiscard]] std::int64_t now_ms() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

[[nodiscard]] std::string uuid(std::uint64_t value) {
  std::ostringstream output;
  output << "00000000-0000-0000-0000-" << std::hex
         << std::setfill('0') << std::setw(12) << value;
  return output.str();
}

[[nodiscard]] std::string trip_id(std::size_t trip_index) {
  return uuid(kTripBase + trip_index);
}

[[nodiscard]] std::string plan_id(std::size_t trip_index) {
  return uuid(kPlanBase + trip_index);
}

[[nodiscard]] std::string activity_id(std::size_t trip_index,
                                      std::size_t activity_index) {
  return uuid(kActivityBase +
              trip_index * 64 + activity_index);
}

void fill_open(::liveroute::v1::PlannerStreamRequest& request) {
  request.set_request_id(uuid(1));
  auto* open = request.mutable_open_stream();
  open->set_backend_instance_id("liveroute-grpc-loadgen");
  open->set_protocol_version("liveroute.v1");
  for (const auto& capability :
       transport::required_v1_capabilities()) {
    open->add_capabilities(capability);
  }
}

void fill_bootstrap(
    ::liveroute::v1::PlannerStreamRequest& request,
    const WorkloadConfiguration& configuration,
    std::size_t trip_index) {
  const auto current_time = now_ms();
  request.set_request_id(
      uuid(kBootstrapRequestBase + trip_index));
  request.set_trip_id(trip_id(trip_index));
  request.set_runtime_epoch(1);
  request.set_expires_at_unix_ms(current_time + 10000);

  auto* bootstrap = request.mutable_bootstrap_trip();
  bootstrap->set_finalized_mutation_sequence(1);
  bootstrap->set_trip_revision(1);
  auto* trip = bootstrap->mutable_full_trip();
  trip->set_trip_id(trip_id(trip_index));
  trip->set_owner_user_id(
      "00000000-0000-0000-0000-000000000001");
  trip->set_default_time_zone_name("America/New_York");
  trip->set_current_plan_id(plan_id(trip_index));

  auto* plan = bootstrap->mutable_current_plan();
  plan->set_plan_id(plan_id(trip_index));
  plan->set_plan_revision(1);
  plan->set_origin(
      ::liveroute::v1::PLAN_ORIGIN_USER_AUTHORED);
  plan->set_created_at_unix_ms(current_time);

  for (std::size_t index = 0;
       index < configuration.suffix_size; ++index) {
    auto* activity = trip->add_activities();
    activity->set_activity_id(activity_id(trip_index, index));
    activity->set_place_id("load-place");
    activity->set_display_name("Load activity");
    activity->mutable_location()->set_latitude(
        40.0 + static_cast<double>(index) * 0.001);
    activity->mutable_location()->set_longitude(-74.0);
    activity->set_time_zone_name("America/New_York");
    activity->set_inbound_travel_mode(
        ::liveroute::v1::TRAVEL_MODE_WALKING);
    activity->set_activity_class(
        ::liveroute::v1::ACTIVITY_CLASS_FLEXIBLE);
    activity->set_activity_state(
        ::liveroute::v1::ACTIVITY_STATE_PLANNED);
    activity->set_priority_rank(static_cast<std::int32_t>(index));
    activity->set_utility_score(10);
    auto* timing = activity->mutable_timing();
    auto* window = timing->add_open_windows();
    window->set_opens_at_unix_ms(current_time - 60000);
    window->set_closes_at_unix_ms(current_time + 3600000);
    timing->set_min_duration_seconds(60);
    timing->set_preferred_duration_seconds(60);
    timing->set_max_duration_seconds(60);
    timing->set_can_move(true);
    timing->set_can_skip(true);

    auto* segment = plan->add_segments();
    segment->set_activity_id(activity_id(trip_index, index));
    segment->set_state(
        ::liveroute::v1::PLAN_ENTRY_STATE_OMITTED);
  }
}

void fill_event_envelope(
    ::liveroute::v1::PlannerStreamRequest& request,
    std::size_t trip_index, std::uint64_t request_value,
    std::uint64_t event_value, std::int64_t expires_at) {
  request.set_request_id(uuid(request_value));
  request.set_trip_id(trip_id(trip_index));
  request.set_runtime_epoch(1);
  request.set_expires_at_unix_ms(expires_at);
  request.mutable_apply_event()->set_event_id(uuid(event_value));
  request.mutable_apply_event()->set_occurred_at_unix_ms(now_ms());
}

void fill_prime(::liveroute::v1::PlannerStreamRequest& request,
                std::size_t trip_index) {
  fill_event_envelope(
      request, trip_index, kPrimeRequestBase + trip_index,
      kEventBase + trip_index, now_ms() + 10000);
  request.set_observation_sequence(1);
  auto* location =
      request.mutable_apply_event()
          ->mutable_location_updated()
          ->mutable_location();
  location->set_latitude(40.0);
  location->set_longitude(-74.0);
}

void fill_workload_event(
    ::liveroute::v1::PlannerStreamRequest& request,
    const WorkloadConfiguration& configuration,
    const WorkloadEvent& event) {
  fill_event_envelope(
      request, event.trip_index,
      kEventRequestBase + event.event_index,
      kEventBase + configuration.active_trips + event.event_index,
      now_ms() + configuration.deadline.count());
  request.set_observation_sequence(event.observation_sequence);
  request.set_mutation_sequence(event.mutation_sequence);
  if (event.mutation_sequence != 0) {
    request.set_expected_trip_revision(
        event.expected_trip_revision);
  }

  auto* payload = request.mutable_apply_event();
  switch (event.kind) {
    case WorkloadEventKind::kLocation: {
      auto* location =
          payload->mutable_location_updated()->mutable_location();
      location->set_latitude(40.0);
      location->set_longitude(-74.0);
      break;
    }
    case WorkloadEventKind::kRouteDeviation: {
      auto* deviation = payload->mutable_route_deviation_detected();
      deviation->mutable_location()->set_latitude(40.0);
      deviation->mutable_location()->set_longitude(-74.0);
      deviation->set_distance_from_route_meters(25);
      break;
    }
    case WorkloadEventKind::kReservationChanged: {
      auto* changed = payload->mutable_reservation_changed();
      changed->set_activity_id(activity_id(event.trip_index, 0));
      changed->set_reservation_start_unix_ms(now_ms() + 600000);
      changed->set_reservation_grace_seconds(30);
      break;
    }
    case WorkloadEventKind::kOperatingHoursChanged: {
      auto* changed = payload->mutable_operating_hours_changed();
      changed->set_activity_id(activity_id(event.trip_index, 0));
      auto* window = changed->add_open_windows();
      const auto current_time = now_ms();
      window->set_opens_at_unix_ms(current_time - 60000);
      window->set_closes_at_unix_ms(current_time + 3600000);
      break;
    }
    case WorkloadEventKind::kPlaceFoundClosed: {
      auto* closed = payload->mutable_place_found_closed();
      closed->set_activity_id(activity_id(event.trip_index, 0));
      closed->set_observed_at_unix_ms(now_ms());
      break;
    }
  }
}

[[nodiscard]] bool successful_bootstrap_status(
    ::liveroute::v1::StatusCode status) {
  return status == ::liveroute::v1::STATUS_CODE_OK ||
         status == ::liveroute::v1::STATUS_CODE_DUPLICATE;
}

void increment_status(std::vector<std::uint64_t>& statuses,
                      ::liveroute::v1::StatusCode status) {
  const auto index = static_cast<std::size_t>(status);
  if (index < statuses.size()) ++statuses[index];
}

}  // namespace

bool GrpcLoadResult::completed() const noexcept {
  return stream_opened && bootstrapped && primed && transport_ok &&
         submitted == acknowledged + protocol_errors;
}

GrpcLoadResult run_grpc_workload(
    const std::string& target,
    const WorkloadConfiguration& configuration) {
  GrpcLoadResult result;
  result.acknowledgement_statuses.resize(kStatusCount);
  result.replan_statuses.resize(kStatusCount);
  result.error_statuses.resize(kStatusCount);
  if (target.empty() || !configuration.is_valid()) {
    result.transport_message = "invalid load configuration";
    return result;
  }

  auto channel = ::grpc::CreateChannel(
      target, ::grpc::InsecureChannelCredentials());
  if (!channel->WaitForConnected(
          std::chrono::system_clock::now() + 5s)) {
    result.transport_message = "target connection timed out";
    return result;
  }
  auto stub =
      ::liveroute::v1::LiveRoutePlanner::NewStub(std::move(channel));
  ::grpc::ClientContext context;
  const auto workload = generate_workload(configuration);
  const auto last_offset =
      workload.empty() ? std::chrono::nanoseconds::zero()
                       : workload.back().scheduled_offset;
  context.set_deadline(
      std::chrono::system_clock::now() +
      std::chrono::duration_cast<std::chrono::system_clock::duration>(
          last_offset) +
      30s);
  auto stream = stub->PlanTrips(&context);

  ::liveroute::v1::PlannerStreamRequest request;
  ::liveroute::v1::PlannerStreamResponse response;
  fill_open(request);
  if (!stream->Write(request) || !stream->Read(&response) ||
      !response.has_stream_ready() ||
      response.stream_ready().status() !=
          ::liveroute::v1::STATUS_CODE_OK) {
    context.TryCancel();
    result.transport_message = "stream handshake failed";
    (void)stream->Finish();
    return result;
  }
  result.stream_opened = true;

  for (std::size_t index = 0;
       index < configuration.active_trips; ++index) {
    request.Clear();
    fill_bootstrap(request, configuration, index);
    if (!stream->Write(request) || !stream->Read(&response) ||
        !response.has_trip_bootstrapped() ||
        !successful_bootstrap_status(
            response.trip_bootstrapped().status())) {
      context.TryCancel();
      result.transport_message = "trip bootstrap failed";
      (void)stream->Finish();
      return result;
    }
  }
  result.bootstrapped = true;

  for (std::size_t index = 0;
       index < configuration.active_trips; ++index) {
    request.Clear();
    fill_prime(request, index);
    if (!stream->Write(request) || !stream->Read(&response) ||
        !response.has_event_acknowledged() ||
        response.event_acknowledged().status() !=
            ::liveroute::v1::STATUS_CODE_OK) {
      context.TryCancel();
      result.transport_message = "trip observation priming failed";
      (void)stream->Finish();
      return result;
    }
  }
  result.primed = true;

  std::mutex mutex;
  std::condition_variable condition;
  std::unordered_map<std::string,
                     std::chrono::steady_clock::time_point>
      submitted_at;
  bool read_finished = false;
  const auto run_start = std::chrono::steady_clock::now();

  std::thread reader([&] {
    ::liveroute::v1::PlannerStreamResponse incoming;
    while (stream->Read(&incoming)) {
      const auto received_at = std::chrono::steady_clock::now();
      std::scoped_lock lock(mutex);
      const auto found = submitted_at.find(incoming.request_id());
      const auto elapsed =
          found == submitted_at.end()
              ? std::chrono::microseconds::zero()
              : std::chrono::duration_cast<std::chrono::microseconds>(
                    received_at - found->second);
      if (incoming.has_event_acknowledged()) {
        ++result.acknowledged;
        if (incoming.event_acknowledged().replan_scheduled()) {
          ++result.scheduled_replans;
        }
        increment_status(
            result.acknowledgement_statuses,
            incoming.event_acknowledged().status());
        result.acknowledgement_latencies_us.push_back(
            static_cast<std::uint64_t>(elapsed.count()));
      } else if (incoming.has_replan_result()) {
        ++result.replan_results;
        increment_status(result.replan_statuses,
                         incoming.replan_result().status());
        result.replan_latencies_us.push_back(
            static_cast<std::uint64_t>(elapsed.count()));
      } else if (incoming.has_error()) {
        ++result.protocol_errors;
        increment_status(result.error_statuses,
                         incoming.error().status());
      }
      condition.notify_all();
    }
    {
      std::scoped_lock lock(mutex);
      read_finished = true;
    }
    condition.notify_all();
  });

  for (const auto& event : workload) {
    std::this_thread::sleep_until(run_start + event.scheduled_offset);
    request.Clear();
    fill_workload_event(request, configuration, event);
    {
      std::scoped_lock lock(mutex);
      submitted_at.emplace(request.request_id(),
                           std::chrono::steady_clock::now());
    }
    if (!stream->Write(request)) break;
    ++result.submitted;
  }

  {
    std::unique_lock lock(mutex);
    (void)condition.wait_for(lock, 15s, [&] {
      return read_finished ||
             result.acknowledged + result.protocol_errors ==
                 result.submitted;
    });
    const auto result_wait =
        std::max(configuration.deadline + 250ms, 500ms);
    (void)condition.wait_for(lock, result_wait);
  }
  (void)stream->WritesDone();
  reader.join();
  const auto status = stream->Finish();
  const auto run_end = std::chrono::steady_clock::now();
  result.elapsed_microseconds =
      static_cast<std::uint64_t>(
          std::chrono::duration_cast<std::chrono::microseconds>(
              run_end - run_start)
              .count());
  result.transport_ok = status.ok();
  result.transport_message =
      status.ok() ? "OK" : status.error_message();
  result.superseded_replan_correlations =
      result.scheduled_replans > result.replan_results
          ? result.scheduled_replans - result.replan_results
          : 0;
  return result;
}

void write_grpc_load_report(
    std::ostream& output, const std::string& target,
    const WorkloadConfiguration& configuration,
    const GrpcLoadResult& result) {
  const auto elapsed_seconds =
      static_cast<double>(result.elapsed_microseconds) / 1000000.0;
  output
      << "mode=grpc"
      << " target=" << target
      << " profile=" << workload_profile_name(configuration.profile)
      << " seed=" << configuration.seed
      << " trips=" << configuration.active_trips
      << " events=" << configuration.event_count
      << " target_events_per_second="
      << configuration.events_per_second
      << " burst_size=" << configuration.location_burst_size
      << " suffix_size=" << configuration.suffix_size
      << " reservation_percent=" << configuration.reservation_percent
      << " deadline_ms=" << configuration.deadline.count()
      << " submitted=" << result.submitted
      << " acknowledged=" << result.acknowledged
      << " protocol_errors=" << result.protocol_errors
      << " scheduled_replans=" << result.scheduled_replans
      << " replan_results=" << result.replan_results
      << " superseded_replan_correlations="
      << result.superseded_replan_correlations
      << " throughput_events_per_second="
      << (elapsed_seconds == 0.0
              ? 0.0
              : static_cast<double>(result.acknowledged) /
                    elapsed_seconds)
      << " elapsed_ms=" << result.elapsed_microseconds / 1000
      << " ack_p50_us="
      << percentile(result.acknowledgement_latencies_us, 50)
      << " ack_p95_us="
      << percentile(result.acknowledgement_latencies_us, 95)
      << " ack_p99_us="
      << percentile(result.acknowledgement_latencies_us, 99)
      << " plan_p50_us="
      << percentile(result.replan_latencies_us, 50)
      << " plan_p95_us="
      << percentile(result.replan_latencies_us, 95)
      << " plan_p99_us="
      << percentile(result.replan_latencies_us, 99)
      << " ok_acks="
      << result.acknowledgement_statuses[
             ::liveroute::v1::STATUS_CODE_OK]
      << " duplicate_acks="
      << result.acknowledgement_statuses[
             ::liveroute::v1::STATUS_CODE_DUPLICATE]
      << " stale_acks="
      << result.acknowledgement_statuses[
             ::liveroute::v1::STATUS_CODE_STALE]
      << " deadline_errors="
      << result.error_statuses[
             ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED]
      << " cancelled_results="
      << result.replan_statuses[
             ::liveroute::v1::STATUS_CODE_CANCELLED]
      << " provider_failures="
      << result.replan_statuses[
             ::liveroute::v1::STATUS_CODE_PROVIDER_UNAVAILABLE]
      << " transport_ok=" << result.transport_ok
      << " transport_message=" << std::quoted(result.transport_message)
      << '\n';
}

}  // namespace liveroute::load
