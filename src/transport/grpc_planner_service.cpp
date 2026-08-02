#include "liveroute/transport/grpc_planner_service.hpp"

#include <algorithm>
#include <chrono>
#include <deque>
#include <map>
#include <memory>
#include <optional>
#include <set>
#include <string>
#include <tuple>
#include <utility>

#include <grpcpp/support/status.h>

#include "liveroute/transport/proto_conversion.hpp"

namespace liveroute::transport {
namespace {

using Request = ::liveroute::v1::PlannerStreamRequest;
using Response = ::liveroute::v1::PlannerStreamResponse;
using Reactor = ::grpc::ServerBidiReactor<Request, Response>;

constexpr std::string_view kProtocolVersion = "liveroute.v1";

struct CorrelationKey {
  domain::TripId trip_id;
  std::uint64_t runtime_epoch{};
  std::uint64_t planner_state_version{};

  auto operator<=>(const CorrelationKey&) const = default;
};

class StreamEndpoint;

class DeserializationTimer {
 public:
  // gRPC has already decoded the wire message before OnReadDone. This timer
  // deliberately covers only application validation and domain construction.
  DeserializationTimer(runtime::ConcurrentTripRuntime& runtime,
                       std::chrono::steady_clock::time_point started_at)
      : runtime_(runtime), started_at_(started_at) {}

  ~DeserializationTimer() { finish(); }

  DeserializationTimer(const DeserializationTimer&) = delete;
  DeserializationTimer& operator=(const DeserializationTimer&) = delete;

  void finish() noexcept {
    if (finished_) return;
    const auto elapsed = std::chrono::duration_cast<std::chrono::microseconds>(
        std::chrono::steady_clock::now() - started_at_);
    runtime_.observe_deserialization(
        static_cast<std::uint64_t>(std::max<std::int64_t>(0, elapsed.count())));
    finished_ = true;
  }

 private:
  runtime::ConcurrentTripRuntime& runtime_;
  std::chrono::steady_clock::time_point started_at_;
  bool finished_{};
};

class ResponseReservation {
 public:
  ResponseReservation(std::weak_ptr<StreamEndpoint> endpoint,
                      std::size_t count)
      : endpoint_(std::move(endpoint)), count_(count) {}
  ~ResponseReservation();

  ResponseReservation(const ResponseReservation&) = delete;
  ResponseReservation& operator=(const ResponseReservation&) = delete;

 private:
  std::weak_ptr<StreamEndpoint> endpoint_;
  std::size_t count_{};
  bool consumed_{};

  friend class StreamEndpoint;
};

class StreamEndpoint final : public PlannerResponseSink,
                             public std::enable_shared_from_this<StreamEndpoint> {
 public:
  explicit StreamEndpoint(std::size_t capacity) : capacity_(capacity) {}

  void attach(Reactor* reactor) {
    std::scoped_lock lock(mutex_);
    reactor_ = reactor;
  }

  void detach() {
    std::scoped_lock lock(mutex_);
    reactor_ = nullptr;
    queue_.clear();
    current_.reset();
    correlations_.clear();
  }

  [[nodiscard]] std::shared_ptr<ResponseReservation> try_reserve(
      std::size_t count = 1) {
    std::scoped_lock lock(mutex_);
    if (reactor_ == nullptr || closing_ || count == 0 ||
        in_use_locked() + count > capacity_) {
      return {};
    }
    reserved_ += count;
    return std::make_shared<ResponseReservation>(weak_from_this(), count);
  }

  [[nodiscard]] bool enqueue_reserved(
      Response response,
      const std::shared_ptr<ResponseReservation>& reservation) {
    std::scoped_lock lock(mutex_);
    if (!reservation || reservation->consumed_ ||
        reservation->count_ == 0 ||
        reserved_ < reservation->count_) {
      return false;
    }
    reserved_ -= reservation->count_;
    reservation->consumed_ = true;
    if (reactor_ == nullptr || closing_ ||
        in_use_locked() >= capacity_) {
      return false;
    }
    queue_.push_back(
        std::make_unique<Response>(std::move(response)));
    start_write_locked();
    return true;
  }

  [[nodiscard]] bool enqueue_pair_reserved(
      Response first, Response second,
      const std::shared_ptr<ResponseReservation>& reservation) {
    std::scoped_lock lock(mutex_);
    if (!reservation || reservation->consumed_ ||
        reservation->count_ != 2 || reserved_ < 2) {
      return false;
    }
    reserved_ -= 2;
    reservation->consumed_ = true;
    if (reactor_ == nullptr || closing_ ||
        in_use_locked() + 2 > capacity_) {
      return false;
    }
    queue_.push_back(std::make_unique<Response>(std::move(first)));
    queue_.push_back(std::make_unique<Response>(std::move(second)));
    start_write_locked();
    return true;
  }

  [[nodiscard]] bool enqueue_immediate(Response response) {
    std::scoped_lock lock(mutex_);
    if (reactor_ == nullptr || closing_ ||
        in_use_locked() >= capacity_) {
      return false;
    }
    queue_.push_back(
        std::make_unique<Response>(std::move(response)));
    start_write_locked();
    return true;
  }

  [[nodiscard]] bool publish(
      runtime::RuntimePlanningDelivery delivery) override {
    Response response;
    response.set_trip_id(format_trip_id(delivery.trip_id));
    set_response_versions(delivery.versions, response);
    {
      std::scoped_lock lock(mutex_);
      const CorrelationKey key{
          delivery.trip_id, delivery.versions.runtime_epoch,
          delivery.versions.planner_state_version};
      const auto found = correlations_.find(key);
      if (found == correlations_.end()) return false;
      response.set_request_id(found->second);
      correlations_.erase(found);

      auto* result = response.mutable_replan_result();
      result->set_status(status_to_proto(delivery.status));
      result->set_retryable(delivery.retryable);
      if (delivery.proposal) {
        proposal_to_proto(*delivery.proposal, *result);
      } else if (delivery.status ==
                 runtime::RuntimePlanningStatus::kNoNewProposal) {
        result->set_notification(
            ::liveroute::v1::NOTIFICATION_TYPE_NONE);
        result->add_reasons(
            ::liveroute::v1::PLAN_REASON_CODE_DEADLINE_BUDGET);
        auto* quality = result->mutable_quality();
        quality->set_plan_quality(
            ::liveroute::v1::PLAN_QUALITY_NO_NEW_PROPOSAL);
        quality->set_routing_quality(
            ::liveroute::v1::ROUTING_QUALITY_FRESH);
        quality->set_recovery_state(
            ::liveroute::v1::RECOVERY_STATE_CURRENT);
      }

      if (reactor_ == nullptr || closing_) return false;
      if (in_use_locked() >= capacity_) {
        for (auto iterator = queue_.rbegin();
             iterator != queue_.rend(); ++iterator) {
          if ((*iterator)->payload_case() ==
                  Response::kReplanResult &&
              (*iterator)->trip_id() == response.trip_id()) {
            **iterator = std::move(response);
            return true;
          }
        }
        return false;
      }
      queue_.push_back(
          std::make_unique<Response>(std::move(response)));
      start_write_locked();
    }
    return true;
  }

  void record_correlation(CorrelationKey key, std::string request_id) {
    std::scoped_lock lock(mutex_);
    for (auto iterator = correlations_.begin();
         iterator != correlations_.end();) {
      if (iterator->first.trip_id == key.trip_id) {
        iterator = correlations_.erase(iterator);
      } else {
        ++iterator;
      }
    }
    correlations_.insert_or_assign(std::move(key),
                                   std::move(request_id));
  }

  void bind_trip(const domain::TripId& trip_id, std::uint64_t epoch) {
    std::scoped_lock lock(mutex_);
    bound_trips_.insert_or_assign(trip_id, epoch);
  }

  [[nodiscard]] std::vector<std::pair<domain::TripId, std::uint64_t>>
  bound_trips() const {
    std::scoped_lock lock(mutex_);
    std::vector<std::pair<domain::TripId, std::uint64_t>> result;
    result.reserve(bound_trips_.size());
    for (const auto& entry : bound_trips_) result.push_back(entry);
    return result;
  }

  void remove_trip(const domain::TripId& trip_id) {
    std::scoped_lock lock(mutex_);
    bound_trips_.erase(trip_id);
  }

  [[nodiscard]] bool mark_open() {
    std::scoped_lock lock(mutex_);
    if (opened_) return false;
    opened_ = true;
    return true;
  }

  [[nodiscard]] bool is_open() const {
    std::scoped_lock lock(mutex_);
    return opened_;
  }

  void on_write_done(bool ok) {
    std::scoped_lock lock(mutex_);
    current_.reset();
    writing_ = false;
    if (!ok) {
      finish_locked(::grpc::Status(
          ::grpc::StatusCode::UNAVAILABLE, "stream write failed"));
      return;
    }
    start_write_locked();
    maybe_finish_locked();
  }

  void finish_when_drained(::grpc::Status status) {
    std::scoped_lock lock(mutex_);
    closing_ = true;
    finish_status_ = std::move(status);
    maybe_finish_locked();
  }

 private:
  [[nodiscard]] std::size_t in_use_locked() const noexcept {
    return queue_.size() + (writing_ ? 1U : 0U) + reserved_;
  }

  void release(std::size_t count) {
    std::scoped_lock lock(mutex_);
    if (count <= reserved_) reserved_ -= count;
    maybe_finish_locked();
  }

  void start_write_locked() {
    if (reactor_ == nullptr || writing_ || queue_.empty()) return;
    current_ = std::move(queue_.front());
    queue_.pop_front();
    writing_ = true;
    reactor_->StartWrite(current_.get());
  }

  void maybe_finish_locked() {
    if (closing_ && !writing_ && queue_.empty() && reserved_ == 0) {
      finish_locked(finish_status_);
    }
  }

  void finish_locked(const ::grpc::Status& status) {
    if (reactor_ == nullptr || finish_started_) return;
    finish_started_ = true;
    reactor_->Finish(status);
  }

  mutable std::mutex mutex_;
  Reactor* reactor_{};
  std::size_t capacity_{};
  std::size_t reserved_{};
  bool opened_{};
  bool writing_{};
  bool closing_{};
  bool finish_started_{};
  ::grpc::Status finish_status_;
  std::deque<std::unique_ptr<Response>> queue_;
  std::unique_ptr<Response> current_;
  std::map<CorrelationKey, std::string> correlations_;
  std::map<domain::TripId, std::uint64_t> bound_trips_;

  friend class ResponseReservation;
};

ResponseReservation::~ResponseReservation() {
  if (!consumed_ && count_ != 0) {
    if (auto endpoint = endpoint_.lock()) endpoint->release(count_);
  }
}

[[nodiscard]] Response base_response(const Request& request) {
  Response response;
  response.set_request_id(request.request_id());
  response.set_trip_id(request.trip_id());
  response.set_runtime_epoch(request.runtime_epoch());
  return response;
}

[[nodiscard]] Response error_response(
    const Request& request, ::liveroute::v1::StatusCode status,
    bool retryable, std::string safe_message,
    ::liveroute::v1::StaleReason stale_reason =
        ::liveroute::v1::STALE_REASON_UNSPECIFIED) {
  auto response = base_response(request);
  auto* error = response.mutable_error();
  error->set_status(status);
  error->set_retryable(retryable);
  error->set_stale_reason(stale_reason);
  error->set_safe_message(std::move(safe_message));
  error->set_related_mutation_sequence(request.mutation_sequence());
  error->set_related_observation_sequence(
      request.observation_sequence());
  if (request.has_expected_planner_state_version()) {
    error->set_related_planner_state_version(
        request.expected_planner_state_version());
  }
  return response;
}

[[nodiscard]] bool no_trip_envelope(const Request& request) noexcept {
  return request.trip_id().empty() && request.runtime_epoch() == 0 &&
         request.mutation_sequence() == 0 &&
         request.observation_sequence() == 0 &&
         !request.has_expected_planner_state_version() &&
         !request.has_expected_trip_revision() &&
         request.expires_at_unix_ms() == 0;
}

[[nodiscard]] bool control_envelope_valid(
    const Request& request) noexcept {
  return !request.trip_id().empty() && request.runtime_epoch() != 0 &&
         request.mutation_sequence() == 0 &&
         request.observation_sequence() == 0 &&
         !request.has_expected_planner_state_version() &&
         !request.has_expected_trip_revision() &&
         request.expires_at_unix_ms() > 0;
}

[[nodiscard]] bool expired(const Request& request) noexcept {
  const auto now = std::chrono::duration_cast<std::chrono::milliseconds>(
                       std::chrono::system_clock::now().time_since_epoch())
                       .count();
  return request.expires_at_unix_ms() <= now;
}

[[nodiscard]] bool capabilities_valid(
    const ::liveroute::v1::OpenStream& open) {
  if (open.protocol_version() != kProtocolVersion ||
      open.backend_instance_id().empty() ||
      !std::is_sorted(open.capabilities().begin(),
                      open.capabilities().end()) ||
      std::adjacent_find(open.capabilities().begin(),
                         open.capabilities().end()) !=
          open.capabilities().end()) {
    return false;
  }
  const auto required = required_v1_capabilities();
  return std::all_of(required.begin(), required.end(),
                     [&](const std::string& capability) {
                       return std::binary_search(
                           open.capabilities().begin(),
                           open.capabilities().end(), capability);
                     });
}

[[nodiscard]] ::liveroute::v1::StatusCode submission_status(
    runtime::RuntimeSubmissionStatus status) noexcept {
  switch (status) {
    case runtime::RuntimeSubmissionStatus::kResponseCapacityFull:
    case runtime::RuntimeSubmissionStatus::kShardQueueFull:
      return ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED;
    case runtime::RuntimeSubmissionStatus::kStopping:
      return ::liveroute::v1::STATUS_CODE_UNAVAILABLE;
    case runtime::RuntimeSubmissionStatus::kAccepted:
      return ::liveroute::v1::STATUS_CODE_OK;
  }
  return ::liveroute::v1::STATUS_CODE_INTERNAL;
}

class PlanTripsReactor final : public Reactor {
 public:
  PlanTripsReactor(GrpcPlannerConfiguration configuration,
                   runtime::ConcurrentTripRuntime& runtime,
                   PlannerResponseRouter& router,
                   std::uint64_t stream_binding)
      : configuration_(std::move(configuration)),
        runtime_(runtime),
        router_(router),
        stream_binding_(stream_binding),
        endpoint_(
            std::make_shared<StreamEndpoint>(
                configuration_.outbound_queue_capacity)) {
    endpoint_->attach(this);
    router_.bind(stream_binding_, endpoint_);
    StartRead(&request_);
  }

  void OnReadDone(bool ok) override {
    if (!ok) {
      endpoint_->finish_when_drained(::grpc::Status::OK);
      return;
    }
    const auto admission_started = std::chrono::steady_clock::now();
    Request request = std::move(request_);
    request_.Clear();
    handle(std::move(request), admission_started);
    if (!closing_) StartRead(&request_);
  }

  void OnWriteDone(bool ok) override { endpoint_->on_write_done(ok); }

  void OnCancel() override {
    closing_ = true;
    cancel_bindings();
    endpoint_->finish_when_drained(
        ::grpc::Status(::grpc::StatusCode::CANCELLED,
                       "stream cancelled"));
  }

  void OnDone() override {
    cancel_bindings();
    router_.unbind(stream_binding_);
    endpoint_->detach();
    delete this;
  }

 private:
  void handle(Request request,
              std::chrono::steady_clock::time_point admission_started) {
    DeserializationTimer timer(runtime_, admission_started);
    if (request.ByteSizeLong() > configuration_.max_message_bytes ||
        !is_canonical_uuid(request.request_id())) {
      timer.finish();
      protocol_error(
          request, ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
          "invalid request envelope", false);
      return;
    }
    if (request.payload_case() == Request::kPing) {
      handle_ping(request, timer);
      return;
    }
    if (!endpoint_->is_open()) {
      if (request.payload_case() != Request::kOpenStream) {
        timer.finish();
        protocol_error(
            request, ::liveroute::v1::STATUS_CODE_UNSUPPORTED_VERSION,
            "OpenStream must be the first non-ping message", false);
        return;
      }
      handle_open(request, timer);
      return;
    }
    if (request.payload_case() == Request::kOpenStream) {
      timer.finish();
      protocol_error(request,
                     ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                     "stream is already open", false);
      return;
    }
    switch (request.payload_case()) {
      case Request::kBootstrapTrip:
        handle_bootstrap(std::move(request), timer);
        break;
      case Request::kApplyEvent:
        handle_event(std::move(request), timer);
        break;
      case Request::kConfirmFinalizedMutations:
        handle_confirm(std::move(request), timer);
        break;
      case Request::kRequestSnapshot:
        handle_snapshot(std::move(request), timer);
        break;
      case Request::kDeactivateTrip:
        handle_deactivate(std::move(request), timer);
        break;
      default:
        timer.finish();
        protocol_error(request,
                       ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                       "request payload is required", false);
        break;
    }
  }

  void handle_open(const Request& request, DeserializationTimer& timer) {
    if (!no_trip_envelope(request) ||
        !capabilities_valid(request.open_stream()) ||
        !endpoint_->mark_open()) {
      timer.finish();
      protocol_error(
          request, ::liveroute::v1::STATUS_CODE_UNSUPPORTED_VERSION,
          "unsupported protocol or capabilities", false);
      return;
    }
    timer.finish();
    auto response = base_response(request);
    auto* ready = response.mutable_stream_ready();
    ready->set_cpp_instance_id(configuration_.cpp_instance_id);
    ready->set_protocol_version(kProtocolVersion);
    for (const auto& capability : required_v1_capabilities()) {
      ready->add_capabilities(capability);
    }
    ready->set_max_message_bytes(configuration_.max_message_bytes);
    ready->set_max_snapshot_bytes(configuration_.max_snapshot_bytes);
    ready->set_max_active_trips(configuration_.max_active_trips);
    ready->set_status(::liveroute::v1::STATUS_CODE_OK);
    if (!endpoint_->enqueue_immediate(std::move(response))) {
      close_resource_exhausted();
    }
  }

  void handle_ping(const Request& request, DeserializationTimer& timer) {
    if (!no_trip_envelope(request) || request.ping().nonce().empty() ||
        request.ping().nonce().size() > 64) {
      timer.finish();
      protocol_error(request,
                     ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                     "invalid ping", false);
      return;
    }
    timer.finish();
    auto response = base_response(request);
    auto* pong = response.mutable_pong();
    pong->set_nonce(request.ping().nonce());
    pong->set_received_at_unix_ms(
        std::chrono::duration_cast<std::chrono::milliseconds>(
            std::chrono::system_clock::now().time_since_epoch())
            .count());
    if (!endpoint_->enqueue_immediate(std::move(response))) {
      close_resource_exhausted();
    }
  }

  void handle_bootstrap(Request request, DeserializationTimer& timer) {
    if (!control_envelope_valid(request) || expired(request)) {
      timer.finish();
      protocol_error(
          request,
          expired(request)
              ? ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED
              : ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
          expired(request) ? "request deadline exceeded"
                           : "invalid bootstrap envelope",
          expired(request));
      return;
    }
    auto converted = bootstrap_from_proto(request, stream_binding_);
    if (!converted) {
      timer.finish();
      const auto status =
          request.bootstrap_trip().base_case() ==
                  ::liveroute::v1::BootstrapTrip::kSnapshot
              ? ::liveroute::v1::STATUS_CODE_SNAPSHOT_INCOMPATIBLE
              : ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT;
      send_error(request, status, false,
                 converted.error->safe_message);
      return;
    }
    timer.finish();
    auto reservation = endpoint_->try_reserve(2);
    if (!reservation) {
      close_resource_exhausted();
      return;
    }
    const auto trip_id = converted.value->state.trip_id;
    const auto request_id = request.request_id();
    const auto submitted = runtime_.try_bootstrap(
        std::move(*converted.value),
        [endpoint = endpoint_, reservation, request_id, trip_id,
         epoch = request.runtime_epoch()](
            runtime::RuntimeBootstrapResult result) mutable {
          Response response;
          response.set_request_id(request_id);
          response.set_trip_id(format_trip_id(trip_id));
          set_response_versions(result.versions, response);
          response.set_runtime_epoch(
              result.versions.runtime_epoch == 0
                  ? epoch
                  : result.versions.runtime_epoch);
          auto* output = response.mutable_trip_bootstrapped();
          switch (result.status) {
            case runtime::RuntimeBootstrapStatus::kAccepted:
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_OK);
              break;
            case runtime::RuntimeBootstrapStatus::kDuplicate:
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_DUPLICATE);
              break;
            case runtime::RuntimeBootstrapStatus::kStale:
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_STALE);
              break;
            case runtime::RuntimeBootstrapStatus::kInvalidArgument:
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT);
              break;
            case runtime::RuntimeBootstrapStatus::kResourceExhausted:
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED);
              output->set_retryable(true);
              break;
          }
          output->set_accepted_mutation_sequence(
              result.versions.accepted_mutation_sequence);
          output->set_finalized_mutation_sequence(
              result.versions.finalized_mutation_sequence);
          if (result.current_plan_id) {
            output->set_current_plan_id(
                format_plan_id(*result.current_plan_id));
          }
          if (result.status == runtime::RuntimeBootstrapStatus::kAccepted ||
              result.status ==
                  runtime::RuntimeBootstrapStatus::kDuplicate) {
            endpoint->bind_trip(trip_id, epoch);
          }
          if (!result.retained_proposal) {
            (void)endpoint->enqueue_reserved(
                std::move(response), reservation);
            return;
          }
          Response retained;
          retained.set_request_id(request_id);
          retained.set_trip_id(format_trip_id(trip_id));
          set_response_versions(result.versions, retained);
          proposal_to_proto(*result.retained_proposal,
                            *retained.mutable_replan_result());
          (void)endpoint->enqueue_pair_reserved(
              std::move(response), std::move(retained), reservation);
        });
    if (submitted != runtime::RuntimeSubmissionStatus::kAccepted) {
      send_submission_failure(request, submitted, reservation);
    }
  }

  void handle_event(Request request, DeserializationTimer& timer) {
    const auto system_now = std::chrono::system_clock::now();
    if (request.expires_at_unix_ms() <=
        std::chrono::duration_cast<std::chrono::milliseconds>(
            system_now.time_since_epoch())
            .count()) {
      timer.finish();
      send_error(request,
                 ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED,
                 true, "request deadline exceeded");
      return;
    }
    auto converted = event_from_proto(
        request, system_now, std::chrono::steady_clock::now(),
        configuration_.default_attempt_timeout,
        configuration_.max_candidates, configuration_.beam_width,
        configuration_.max_expansions);
    if (!converted) {
      timer.finish();
      send_error(request,
                 ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 false, converted.error->safe_message);
      return;
    }
    timer.finish();
    auto reservation = endpoint_->try_reserve();
    if (!reservation) {
      close_resource_exhausted();
      return;
    }
    const auto trip_id = converted.value->trip_id;
    const auto request_id = request.request_id();
    const auto event_id = request.apply_event().event_id();
    const auto submitted = runtime_.try_apply_event(
        std::move(*converted.value),
        [endpoint = endpoint_, reservation, request_id, event_id,
         trip_id](runtime::RuntimeEventAcknowledgement result) mutable {
          Response response;
          response.set_request_id(request_id);
          response.set_trip_id(format_trip_id(trip_id));
          set_response_versions(
              result.admission.version_snapshot, response);
          auto* output = response.mutable_event_acknowledged();
          using Status = runtime::EventCoordinatorStatus;
          switch (result.admission.status) {
            case Status::kAccepted:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_ACCEPTED);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_OK);
              break;
            case Status::kDuplicate:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_DUPLICATE);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_DUPLICATE);
              break;
            case Status::kStale:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_STALE);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_STALE);
              break;
            case Status::kInvalidArgument:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_REJECTED);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT);
              break;
            case Status::kCommandExpired:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_REJECTED);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_COMMAND_EXPIRED);
              break;
            case Status::kInactive:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_REJECTED);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_INACTIVE_TRIP);
              break;
            case Status::kInternal:
              output->set_disposition(
                  ::liveroute::v1::EVENT_DISPOSITION_REJECTED);
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_INTERNAL);
              break;
          }
          output->set_retryable(result.admission.retryable);
          output->set_stale_reason(
              stale_reason_to_proto(
                  result.admission.stale_reason));
          output->set_event_id(event_id);
          output->set_resolved_mutation_sequence(
              result.admission.version_snapshot
                  .accepted_mutation_sequence);
          output->set_resolved_observation_sequence(
              result.admission.version_snapshot
                  .accepted_observation_sequence);
          output->set_replan_scheduled(
              result.admission.planning_seed.has_value());
          if (result.admission.resulting_current_plan_id) {
            output->set_resulting_current_plan_id(format_plan_id(
                *result.admission.resulting_current_plan_id));
          }
          if (result.admission.planning_seed) {
            endpoint->record_correlation(
                {trip_id,
                 result.admission.version_snapshot.runtime_epoch,
                 result.admission.version_snapshot
                     .planner_state_version},
                request_id);
          }
          (void)endpoint->enqueue_reserved(std::move(response),
                                            reservation);
        });
    if (submitted != runtime::RuntimeSubmissionStatus::kAccepted) {
      send_submission_failure(request, submitted, reservation);
    }
  }

  void handle_confirm(Request request, DeserializationTimer& timer) {
    if (!control_envelope_valid(request) || expired(request) ||
        request.confirm_finalized_mutations()
                .finalized_mutation_sequence() == 0) {
      timer.finish();
      send_error(request,
                 expired(request)
                     ? ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED
                     : ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 expired(request), "invalid finalization request");
      return;
    }
    const auto trip_id = parse_trip_id(request.trip_id());
    if (!trip_id) {
      timer.finish();
      send_error(request,
                 ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 false, "invalid trip id");
      return;
    }
    timer.finish();
    auto reservation = endpoint_->try_reserve();
    if (!reservation) {
      close_resource_exhausted();
      return;
    }
    const auto request_id = request.request_id();
    const auto submitted = runtime_.try_confirm_finalized(
        *trip_id, request.runtime_epoch(),
        request.confirm_finalized_mutations()
            .finalized_mutation_sequence(),
        [endpoint = endpoint_, reservation, request_id,
         trip_id = *trip_id](runtime::RuntimeControlResult result) mutable {
          Response response;
          response.set_request_id(request_id);
          response.set_trip_id(format_trip_id(trip_id));
          set_response_versions(result.versions, response);
          auto* output =
              response.mutable_finalized_mutations_acknowledged();
          output->set_status(status_to_proto(result.status));
          output->set_retryable(result.retryable);
          output->set_finalized_mutation_sequence(
              result.versions.finalized_mutation_sequence);
          (void)endpoint->enqueue_reserved(std::move(response),
                                            reservation);
        });
    if (submitted != runtime::RuntimeSubmissionStatus::kAccepted) {
      send_submission_failure(request, submitted, reservation);
    }
  }

  void handle_snapshot(Request request, DeserializationTimer& timer) {
    if (!control_envelope_valid(request) || expired(request) ||
        request.request_snapshot().reason() ==
            ::liveroute::v1::SNAPSHOT_REASON_UNSPECIFIED) {
      timer.finish();
      send_error(request,
                 expired(request)
                     ? ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED
                     : ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 expired(request), "invalid snapshot request");
      return;
    }
    const auto trip_id = parse_trip_id(request.trip_id());
    if (!trip_id) {
      timer.finish();
      send_error(request,
                 ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 false, "invalid trip id");
      return;
    }
    timer.finish();
    auto reservation = endpoint_->try_reserve();
    if (!reservation) {
      close_resource_exhausted();
      return;
    }
    const auto request_id = request.request_id();
    const auto max_snapshot_bytes = configuration_.max_snapshot_bytes;
    const auto submitted = runtime_.try_snapshot(
        *trip_id, request.runtime_epoch(),
        request.request_snapshot()
            .minimum_finalized_mutation_sequence(),
        request.request_snapshot().minimum_planner_state_version(),
        [endpoint = endpoint_, reservation, request_id,
         max_snapshot_bytes,
         trip_id = *trip_id](runtime::RuntimeControlResult result) mutable {
          Response response;
          response.set_request_id(request_id);
          response.set_trip_id(format_trip_id(trip_id));
          set_response_versions(result.versions, response);
          auto* output = response.mutable_trip_snapshot();
          output->set_status(status_to_proto(result.status));
          output->set_retryable(result.retryable);
          if (result.snapshot_state) {
            auto snapshot = snapshot_to_proto(
                *result.snapshot_state, result.versions,
                result.owner_user_id);
            if (!snapshot ||
                snapshot.value->ByteSizeLong() >
                    max_snapshot_bytes) {
              output->set_status(
                  ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED);
              output->set_retryable(true);
            } else {
              *output->mutable_snapshot() =
                  std::move(*snapshot.value);
            }
          }
          (void)endpoint->enqueue_reserved(std::move(response),
                                            reservation);
        });
    if (submitted != runtime::RuntimeSubmissionStatus::kAccepted) {
      send_submission_failure(request, submitted, reservation);
    }
  }

  void handle_deactivate(Request request, DeserializationTimer& timer) {
    if (!control_envelope_valid(request) || expired(request) ||
        request.deactivate_trip().reason() ==
            ::liveroute::v1::DEACTIVATION_REASON_UNSPECIFIED) {
      timer.finish();
      send_error(request,
                 expired(request)
                     ? ::liveroute::v1::STATUS_CODE_DEADLINE_EXCEEDED
                     : ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 expired(request), "invalid deactivation request");
      return;
    }
    const auto trip_id = parse_trip_id(request.trip_id());
    if (!trip_id) {
      timer.finish();
      send_error(request,
                 ::liveroute::v1::STATUS_CODE_INVALID_ARGUMENT,
                 false, "invalid trip id");
      return;
    }
    timer.finish();
    const bool final_required =
        request.deactivate_trip().final_snapshot_required();
    auto reservation = endpoint_->try_reserve(final_required ? 2 : 1);
    if (!reservation) {
      close_resource_exhausted();
      return;
    }
    const auto request_id = request.request_id();
    const auto max_snapshot_bytes = configuration_.max_snapshot_bytes;
    auto* runtime = &runtime_;
    const auto submitted = runtime_.try_deactivate(
        *trip_id, request.runtime_epoch(), final_required,
        [runtime, endpoint = endpoint_, reservation, request_id,
         max_snapshot_bytes, epoch = request.runtime_epoch(),
         trip_id = *trip_id,
         final_required](runtime::RuntimeControlResult result) mutable {
          Response deactivated;
          deactivated.set_request_id(request_id);
          deactivated.set_trip_id(format_trip_id(trip_id));
          set_response_versions(result.versions, deactivated);
          auto* output = deactivated.mutable_trip_deactivated();
          output->set_status(status_to_proto(result.status));
          output->set_retryable(result.retryable);
          output->set_final_snapshot_produced(
              result.snapshot_state.has_value());
          if (!final_required || !result.snapshot_state) {
            (void)endpoint->enqueue_reserved(
                std::move(deactivated), reservation);
            if (result.status == runtime::RuntimeControlStatus::kOk) {
              endpoint->remove_trip(trip_id);
            }
            return;
          }

          Response snapshot_response;
          snapshot_response.set_request_id(request_id);
          snapshot_response.set_trip_id(format_trip_id(trip_id));
          set_response_versions(result.versions, snapshot_response);
          auto* snapshot_output =
              snapshot_response.mutable_trip_snapshot();
          auto snapshot = snapshot_to_proto(
              *result.snapshot_state, result.versions,
              result.owner_user_id);
          if (!snapshot ||
              snapshot.value->ByteSizeLong() >
                  max_snapshot_bytes) {
            snapshot_output->set_status(
                ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED);
            snapshot_output->set_retryable(true);
            output->set_status(
                ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED);
            output->set_retryable(true);
            output->set_final_snapshot_produced(false);
            auto pending_responses =
                std::make_shared<std::pair<Response, Response>>(
                    std::move(snapshot_response),
                    std::move(deactivated));
            const auto abort_submission =
                runtime->try_abort_deactivation(
                    trip_id, epoch,
                    [endpoint, reservation, pending_responses](
                        runtime::RuntimeControlResult) mutable {
                      (void)endpoint->enqueue_pair_reserved(
                          std::move(pending_responses->first),
                          std::move(pending_responses->second),
                          reservation);
                    });
            if (abort_submission !=
                runtime::RuntimeSubmissionStatus::kAccepted) {
              (void)endpoint->enqueue_pair_reserved(
                  std::move(pending_responses->first),
                  std::move(pending_responses->second), reservation);
            }
            return;
          }
          snapshot_output->set_status(
              ::liveroute::v1::STATUS_CODE_OK);
          *snapshot_output->mutable_snapshot() =
              std::move(*snapshot.value);
          auto pending_snapshot =
              std::make_shared<Response>(std::move(snapshot_response));
          const auto finalize_submission = runtime->try_deactivate(
              trip_id, epoch, false,
              [endpoint, reservation, trip_id, pending_snapshot,
               request_id](runtime::RuntimeControlResult finalized) mutable {
                Response final_response;
                final_response.set_request_id(request_id);
                final_response.set_trip_id(format_trip_id(trip_id));
                set_response_versions(finalized.versions, final_response);
                auto* final_output =
                    final_response.mutable_trip_deactivated();
                final_output->set_status(
                    status_to_proto(finalized.status));
                final_output->set_retryable(finalized.retryable);
                final_output->set_final_snapshot_produced(
                    finalized.status ==
                    runtime::RuntimeControlStatus::kOk);
                (void)endpoint->enqueue_pair_reserved(
                    std::move(*pending_snapshot),
                    std::move(final_response), reservation);
                if (finalized.status ==
                    runtime::RuntimeControlStatus::kOk) {
                  endpoint->remove_trip(trip_id);
                }
              });
          if (finalize_submission !=
              runtime::RuntimeSubmissionStatus::kAccepted) {
            output->set_status(submission_status(finalize_submission));
            output->set_retryable(true);
            output->set_final_snapshot_produced(false);
            auto pending_responses =
                std::make_shared<std::pair<Response, Response>>(
                    std::move(*pending_snapshot),
                    std::move(deactivated));
            const auto abort_submission =
                runtime->try_abort_deactivation(
                    trip_id, epoch,
                    [endpoint, reservation, pending_responses](
                        runtime::RuntimeControlResult) mutable {
                      (void)endpoint->enqueue_pair_reserved(
                          std::move(pending_responses->first),
                          std::move(pending_responses->second),
                          reservation);
                    });
            if (abort_submission !=
                runtime::RuntimeSubmissionStatus::kAccepted) {
              (void)endpoint->enqueue_pair_reserved(
                  std::move(pending_responses->first),
                  std::move(pending_responses->second), reservation);
            }
          }
        });
    if (submitted != runtime::RuntimeSubmissionStatus::kAccepted) {
      send_submission_failure(request, submitted, reservation);
    }
  }

  void send_submission_failure(
      const Request& request, runtime::RuntimeSubmissionStatus status,
      const std::shared_ptr<ResponseReservation>& reservation) {
    auto response = error_response(
        request, submission_status(status), true,
        status == runtime::RuntimeSubmissionStatus::kStopping
            ? "planner service is stopping"
            : "planner capacity exhausted");
    if (!endpoint_->enqueue_reserved(std::move(response), reservation)) {
      close_resource_exhausted();
    }
  }

  void send_error(const Request& request,
                  ::liveroute::v1::StatusCode status, bool retryable,
                  std::string message) {
    if (!endpoint_->enqueue_immediate(error_response(
            request, status, retryable, std::move(message)))) {
      close_resource_exhausted();
    }
  }

  void protocol_error(const Request& request,
                      ::liveroute::v1::StatusCode status,
                      std::string message, bool retryable) {
    send_error(request, status, retryable, std::move(message));
    closing_ = true;
    endpoint_->finish_when_drained(::grpc::Status(
        status == ::liveroute::v1::STATUS_CODE_RESOURCE_EXHAUSTED
            ? ::grpc::StatusCode::RESOURCE_EXHAUSTED
            : ::grpc::StatusCode::FAILED_PRECONDITION,
        "protocol error"));
  }

  void close_resource_exhausted() {
    closing_ = true;
    endpoint_->finish_when_drained(::grpc::Status(
        ::grpc::StatusCode::RESOURCE_EXHAUSTED,
        "bounded stream capacity exhausted"));
  }

  void cancel_bindings() {
    for (const auto& [trip_id, epoch] : endpoint_->bound_trips()) {
      (void)runtime_.try_unbind_stream(
          trip_id, epoch, stream_binding_);
    }
  }

  GrpcPlannerConfiguration configuration_;
  runtime::ConcurrentTripRuntime& runtime_;
  PlannerResponseRouter& router_;
  std::uint64_t stream_binding_;
  std::shared_ptr<StreamEndpoint> endpoint_;
  Request request_;
  bool closing_{};
};

}  // namespace

bool GrpcPlannerConfiguration::is_valid() const noexcept {
  return !cpp_instance_id.empty() && outbound_queue_capacity >= 2 &&
         max_message_bytes != 0 && max_snapshot_bytes != 0 &&
         max_snapshot_bytes <= max_message_bytes &&
         max_active_trips != 0 &&
         default_attempt_timeout > std::chrono::milliseconds::zero() &&
         max_candidates != 0 && beam_width != 0 &&
         max_expansions != 0;
}

void PlannerResponseRouter::bind(
    std::uint64_t stream_binding,
    std::weak_ptr<PlannerResponseSink> sink) {
  std::scoped_lock lock(mutex_);
  bindings_.insert_or_assign(stream_binding, std::move(sink));
}

void PlannerResponseRouter::unbind(std::uint64_t stream_binding) {
  std::scoped_lock lock(mutex_);
  bindings_.erase(stream_binding);
}

bool PlannerResponseRouter::publish(
    runtime::RuntimePlanningDelivery delivery) {
  if (!delivery.stream_binding) return false;
  std::shared_ptr<PlannerResponseSink> sink;
  {
    std::scoped_lock lock(mutex_);
    const auto found = bindings_.find(*delivery.stream_binding);
    if (found == bindings_.end()) return false;
    sink = found->second.lock();
    if (!sink) {
      bindings_.erase(found);
      return false;
    }
  }
  return sink->publish(std::move(delivery));
}

std::vector<std::string> required_v1_capabilities() {
  return {"canonical_first_plan_sync",
          "durable_plan_proposals",
          "epoch_scoped_observations",
          "finalized_mutation_watermark",
          "result_quality_metadata",
          "snapshot_schema_1",
          "user_authoritative_current_plan"};
}

GrpcPlannerService::GrpcPlannerService(
    GrpcPlannerConfiguration configuration,
    runtime::ConcurrentTripRuntime& runtime,
    PlannerResponseRouter& response_router)
    : configuration_(std::move(configuration)),
      runtime_(runtime),
      response_router_(response_router) {
  if (!configuration_.is_valid()) {
    throw std::invalid_argument("invalid gRPC planner configuration");
  }
}

Reactor* GrpcPlannerService::PlanTrips(
    ::grpc::CallbackServerContext*) {
  auto binding =
      next_stream_binding_.fetch_add(1, std::memory_order_relaxed);
  if (binding == 0) {
    binding =
        next_stream_binding_.fetch_add(1, std::memory_order_relaxed);
  }
  return new PlanTripsReactor(configuration_, runtime_,
                              response_router_, binding);
}

}  // namespace liveroute::transport
