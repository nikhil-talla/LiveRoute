#include "liveroute/load/websocket_runner.hpp"

#include <curl/curl.h>
#include <curl/websockets.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iterator>
#include <ostream>
#include <poll.h>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>

namespace liveroute::load {
namespace {

using namespace std::chrono_literals;

constexpr std::size_t kMaxWebSocketMessageBytes = 256 * 1024;
constexpr std::size_t kWebSocketReadChunkBytes = 16 * 1024;
constexpr std::int64_t kTripBase = 8000000;
constexpr std::int64_t kPlanBase = 8100000;
constexpr std::int64_t kActivityBase = 8200000;
constexpr std::int64_t kMessageBase = 8300000;

[[nodiscard]] std::int64_t now_ms() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

[[nodiscard]] std::string uuid(std::int64_t value) {
  std::ostringstream output;
  output << "00000000-0000-4000-8000-" << std::hex << std::setfill('0')
         << std::setw(12) << value;
  return output.str();
}

[[nodiscard]] std::string trip_id(std::size_t index) {
  return uuid(kTripBase + static_cast<std::int64_t>(index));
}

[[nodiscard]] std::string plan_id(std::size_t index) {
  return uuid(kPlanBase + static_cast<std::int64_t>(index));
}

[[nodiscard]] std::string activity_id(std::size_t index) {
  return uuid(kActivityBase + static_cast<std::int64_t>(index));
}

[[nodiscard]] std::string envelope(std::string_view message_id,
                                    std::string_view kind,
                                    std::string_view trip,
                                    std::string_view payload) {
  std::string output;
  output.reserve(256 + payload.size());
  output += R"({"protocol_version":"liveroute.v1","message_id":")";
  output += message_id;
  output += R"(","kind":")";
  output += kind;
  output += R"(","trip_id":")";
  output += trip;
  output += R"(","payload":)";
  output += payload;
  output += '}';
  return output;
}

[[nodiscard]] std::string create_payload(
    std::size_t index, std::string_view activity, std::string_view plan) {
  std::ostringstream output;
  output << R"({"default_time_zone_name":"America/New_York","activities":[{"activity_id":")"
         << activity
         << R"(","place_id":"load-place","display_name":"Load activity","location":{"latitude":41.82,"longitude":-71.41},"time_zone_name":"America/New_York","inbound_travel_mode":"driving","activity_class":"flexible","activity_state":"planned","priority_rank":)"
         << (index % 8)
         << R"(,"utility_score":10,"activity_delay_seconds":0,"timing":{"open_windows":[],"reservation_grace_seconds":0,"min_duration_seconds":60,"preferred_duration_seconds":60,"max_duration_seconds":60,"mandatory":false,"can_shorten":false,"can_move":true,"can_skip":true}}],"current_plan":{"plan_id":")"
         << plan
         << R"(","segments":[{"activity_id":")"
         << activity << R"(","state":"omitted"}]}})";
  return output.str();
}

[[nodiscard]] std::string telemetry_payload(
    const WorkloadEvent& event, std::int64_t observed_at) {
  if (event.kind == WorkloadEventKind::kRouteDeviation) {
    return "{\"observation_kind\":\"route_deviation\",\"observed_at_unix_ms\":" +
           std::to_string(observed_at) +
           ",\"observation\":{\"location\":{\"latitude\":41.82,\"longitude\":-71.41},\"distance_from_route_meters\":25}}";
  }
  return "{\"observation_kind\":\"location\",\"observed_at_unix_ms\":" +
         std::to_string(observed_at) +
         ",\"observation\":{\"latitude\":41.82,\"longitude\":-71.41}}";
}

[[nodiscard]] std::string command_payload(
    const WorkloadEvent& event, std::string_view activity) {
  const auto observed_at = now_ms();
  std::string command_kind;
  std::string command;
  switch (event.kind) {
    case WorkloadEventKind::kReservationChanged:
      command_kind = "reservation_changed";
      command = "{\"activity_id\":\"" + std::string(activity) +
                "\",\"reservation_start_unix_ms\":" +
                std::to_string(observed_at + 600000) +
                ",\"reservation_grace_seconds\":30}";
      break;
    case WorkloadEventKind::kOperatingHoursChanged:
      command_kind = "operating_hours_changed";
      command = "{\"activity_id\":\"" + std::string(activity) +
                "\",\"open_windows\":[{\"opens_at_unix_ms\":" +
                std::to_string(observed_at - 60000) +
                ",\"closes_at_unix_ms\":" +
                std::to_string(observed_at + 3600000) + "}]}";
      break;
    case WorkloadEventKind::kPlaceFoundClosed:
      command_kind = "place_found_closed";
      command = "{\"activity_id\":\"" + std::string(activity) +
                "\",\"observed_at_unix_ms\":" +
                std::to_string(observed_at) + "}";
      break;
    case WorkloadEventKind::kLocation:
    case WorkloadEventKind::kRouteDeviation:
      return {};
  }
  return "{\"command_kind\":\"" + command_kind +
         "\",\"command\":" + command + "}";
}

[[nodiscard]] std::string event_message(
    const WorkloadEvent& event) {
  const auto trip = trip_id(event.trip_index);
  const auto activity = activity_id(event.trip_index);
  const auto message = uuid(kMessageBase +
                            static_cast<std::int64_t>(event.event_index));
  if (event.kind == WorkloadEventKind::kLocation ||
      event.kind == WorkloadEventKind::kRouteDeviation) {
    return envelope(message, "telemetry_update", trip,
                    telemetry_payload(event, now_ms()));
  }
  auto payload = command_payload(event, activity);
  if (payload.empty()) return {};
  return envelope(message, "trip_command", trip, payload);
}

[[nodiscard]] bool extract_kind(std::string_view message, std::string* kind) {
  constexpr std::string_view key = R"("kind")";
  const auto key_position = message.find(key);
  if (key_position == std::string_view::npos) return false;
  auto position = message.find(':', key_position + key.size());
  if (position == std::string_view::npos) return false;
  ++position;
  while (position < message.size() &&
         (message[position] == ' ' || message[position] == '\n' ||
          message[position] == '\r' || message[position] == '\t')) {
    ++position;
  }
  if (position >= message.size() || message[position] != '"') return false;
  ++position;
  const auto end = message.find('"', position);
  if (end == std::string_view::npos) return false;
  *kind = std::string(message.substr(position, end - position));
  return true;
}

class WebSocketConnection {
 public:
  WebSocketConnection() = default;
  WebSocketConnection(const WebSocketConnection&) = delete;
  WebSocketConnection& operator=(const WebSocketConnection&) = delete;

  ~WebSocketConnection() {
    if (curl_ != nullptr) curl_easy_cleanup(curl_);
    if (headers_ != nullptr) curl_slist_free_all(headers_);
  }

  [[nodiscard]] bool connect(const std::string& target, std::string* error) {
    curl_ = curl_easy_init();
    if (curl_ == nullptr) {
      *error = "curl_easy_init failed";
      return false;
    }
    headers_ =
        curl_slist_append(headers_, "Origin: http://localhost:5173");
    if (target.rfind("ws://", 0) != 0) {
      *error = "V1 load target must use loopback ws://";
      return false;
    }
    curl_easy_setopt(curl_, CURLOPT_URL, target.c_str());
    curl_easy_setopt(curl_, CURLOPT_HTTPHEADER, headers_);
    curl_easy_setopt(curl_, CURLOPT_CONNECT_ONLY, 2L);
    curl_easy_setopt(curl_, CURLOPT_CONNECTTIMEOUT_MS, 5000L);
    curl_easy_setopt(curl_, CURLOPT_TIMEOUT_MS, 30000L);
    const auto code = curl_easy_perform(curl_);
    if (code != CURLE_OK) {
      *error = curl_easy_strerror(code);
      return false;
    }
    return true;
  }

  [[nodiscard]] bool send(std::string_view message, std::string* error) {
    if (message.empty() || message.size() > kMaxWebSocketMessageBytes) {
      *error = "WebSocket message size is invalid";
      return false;
    }
    size_t sent = 0;
    const auto code = curl_ws_send(
        curl_, message.data(), message.size(), &sent, 0, CURLWS_TEXT);
    if (code != CURLE_OK || sent != message.size()) {
      *error = code == CURLE_OK ? "short WebSocket write"
                                : curl_easy_strerror(code);
      return false;
    }
    return true;
  }

  [[nodiscard]] bool receive(std::string* message, std::string* error,
                             std::chrono::steady_clock::time_point deadline) {
    message->clear();
    std::array<char, kWebSocketReadChunkBytes> buffer{};
    for (;;) {
      if (std::chrono::steady_clock::now() >= deadline) {
        *error = "WebSocket receive deadline exceeded";
        return false;
      }
      size_t received = 0;
      const curl_ws_frame* metadata = nullptr;
      const auto code = curl_ws_recv(
          curl_, buffer.data(), buffer.size(), &received, &metadata);
      if (code == CURLE_AGAIN) {
        curl_socket_t socket = CURL_SOCKET_BAD;
        curl_easy_getinfo(curl_, CURLINFO_ACTIVESOCKET, &socket);
        if (socket == CURL_SOCKET_BAD) {
          *error = "WebSocket active socket is unavailable";
          return false;
        }
        pollfd descriptor{socket, POLLIN, 0};
        const auto remaining = std::chrono::duration_cast<std::chrono::milliseconds>(
            deadline - std::chrono::steady_clock::now());
        if (::poll(&descriptor, 1,
                   static_cast<int>(std::clamp<std::int64_t>(
                       remaining.count(), 1, 1000))) < 0) {
          *error = "WebSocket poll failed";
          return false;
        }
        continue;
      }
      if (code != CURLE_OK || metadata == nullptr) {
        *error = code == CURLE_OK ? "WebSocket metadata is missing"
                                  : curl_easy_strerror(code);
        return false;
      }
      if ((metadata->flags & CURLWS_PING) != 0) {
        size_t sent = 0;
        if (curl_ws_send(curl_, buffer.data(), received, &sent, 0,
                         CURLWS_PONG) != CURLE_OK || sent != received) {
          *error = "WebSocket pong failed";
          return false;
        }
        continue;
      }
      if ((metadata->flags & CURLWS_CLOSE) != 0) {
        *error = "WebSocket peer closed the connection";
        return false;
      }
      if (message->size() + received > kMaxWebSocketMessageBytes) {
        *error = "WebSocket response exceeds the configured limit";
        return false;
      }
      message->append(buffer.data(), received);
      if (metadata->bytesleft == 0) return true;
    }
  }

 private:
  CURL* curl_{};
  curl_slist* headers_{};
};

[[nodiscard]] bool receive_expected(
    WebSocketConnection& connection, std::string_view expected,
    WebSocketLoadResult* result, std::string* error) {
  for (;;) {
    std::string message;
    if (!connection.receive(&message, error,
                            std::chrono::steady_clock::now() + 30s)) {
      return false;
    }
    std::string kind;
    if (!extract_kind(message, &kind)) {
      *error = "server envelope has no kind";
      return false;
    }
    if (kind == "plan_proposal") {
      ++result->plan_notifications;
      continue;
    }
    if (kind == "error") {
      ++result->protocol_errors;
      *error = "server returned an error envelope";
      return false;
    }
    if (kind != expected) {
      *error = "unexpected server envelope kind: " + kind;
      return false;
    }
    return true;
  }
}

[[nodiscard]] std::string read_token(const std::string& path,
                                     std::string* error) {
  std::error_code filesystem_error;
  if (!std::filesystem::is_regular_file(path, filesystem_error)) {
    *error = "development token path must be a regular file";
    return {};
  }
  std::ifstream input(path, std::ios::binary);
  if (!input) {
    *error = "development token file cannot be opened";
    return {};
  }
  std::string token((std::istreambuf_iterator<char>(input)),
                    std::istreambuf_iterator<char>());
  if (token.size() != 43) {
    *error = "development token must contain exactly 43 characters";
    return {};
  }
  return token;
}

}  // namespace

bool websocket_transport_available() noexcept {
  const auto* version = curl_version_info(CURLVERSION_NOW);
  if (version == nullptr || version->protocols == nullptr) return false;
  for (auto protocol = version->protocols; *protocol != nullptr; ++protocol) {
    if (std::string_view{*protocol} == "ws") return true;
  }
  return false;
}

bool WebSocketLoadResult::completed() const noexcept {
  return connected && authenticated && trips_created && subscribed &&
         transport_ok && submitted == acknowledged + protocol_errors;
}

WebSocketLoadResult run_websocket_workload(
    const std::string& target, const std::string& token_file,
    const WorkloadConfiguration& configuration) {
  WebSocketLoadResult result;
  if (target.empty() || token_file.empty() || !configuration.is_valid()) {
    result.transport_message = "invalid WebSocket load configuration";
    return result;
  }
  if (!websocket_transport_available()) {
    result.transport_message =
        "configured libcurl was built without ws protocol support";
    return result;
  }
  std::string error;
  const auto token = read_token(token_file, &error);
  if (token.empty()) {
    result.transport_message = error;
    return result;
  }
  if (curl_global_init(CURL_GLOBAL_DEFAULT) != CURLE_OK) {
    result.transport_message = "curl global initialization failed";
    return result;
  }
  struct CurlCleanup {
    ~CurlCleanup() { curl_global_cleanup(); }
  } curl_cleanup;

  WebSocketConnection connection;
  if (!connection.connect(target, &error)) {
    result.transport_message = error;
    return result;
  }
  result.connected = true;
  // Authentication is connection-scoped and therefore has no trip_id.
  const auto auth_message =
      std::string(R"({"protocol_version":"liveroute.v1","message_id":")") +
      uuid(1) + R"(","kind":"authenticate","payload":{"token":")" +
      token + R"("}})";
  if (!connection.send(auth_message, &error) ||
      !receive_expected(connection, "connection_ready", &result, &error)) {
    result.transport_message = error;
    return result;
  }
  result.authenticated = true;

  for (std::size_t index = 0; index < configuration.active_trips; ++index) {
    const auto trip = trip_id(index);
    const auto activity = activity_id(index);
    const auto plan = plan_id(index);
    const auto message = envelope(
        uuid(1000 + static_cast<std::int64_t>(index)), "create_trip", trip,
        create_payload(index, activity, plan));
    if (!connection.send(message, &error) ||
        !receive_expected(connection, "command_acknowledgement", &result,
                          &error)) {
      result.transport_message = error;
      return result;
    }
  }
  result.trips_created = true;
  for (std::size_t index = 0; index < configuration.active_trips; ++index) {
    const auto trip = trip_id(index);
    const auto message = envelope(
        uuid(2000 + static_cast<std::int64_t>(index)), "subscribe_trip", trip,
        "{}");
    if (!connection.send(message, &error) ||
        !receive_expected(connection, "subscription_state", &result,
                          &error)) {
      result.transport_message = error;
      return result;
    }
  }
  result.subscribed = true;

  const auto workload = generate_workload(configuration);
  const auto run_start = std::chrono::steady_clock::now();
  for (const auto& event : workload) {
    std::this_thread::sleep_until(run_start + event.scheduled_offset);
    const auto message = event_message(event);
    if (message.empty()) {
      result.transport_message = "workload event could not be encoded";
      return result;
    }
    const auto sent_at = std::chrono::steady_clock::now();
    if (!connection.send(message, &error)) {
      result.transport_message = error;
      return result;
    }
    ++result.submitted;
    const auto expected = event.kind == WorkloadEventKind::kLocation ||
                                  event.kind == WorkloadEventKind::kRouteDeviation
                              ? "telemetry_status"
                              : "command_acknowledgement";
    if (!receive_expected(connection, expected, &result, &error)) {
      result.transport_message = error;
      return result;
    }
    ++result.acknowledged;
    result.acknowledgement_latencies_us.push_back(static_cast<std::uint64_t>(
        std::chrono::duration_cast<std::chrono::microseconds>(
            std::chrono::steady_clock::now() - sent_at)
            .count()));
  }
  result.elapsed_microseconds = static_cast<std::uint64_t>(
      std::chrono::duration_cast<std::chrono::microseconds>(
          std::chrono::steady_clock::now() - run_start)
          .count());
  result.transport_ok = true;
  result.transport_message = "OK";
  return result;
}

void write_websocket_load_report(
    std::ostream& output, const std::string& target,
    const WorkloadConfiguration& configuration,
    const WebSocketLoadResult& result) {
  const auto elapsed_seconds =
      static_cast<double>(result.elapsed_microseconds) / 1000000.0;
  output << "mode=websocket target=" << target
         << " profile=" << workload_profile_name(configuration.profile)
         << " seed=" << configuration.seed
         << " trips=" << configuration.active_trips
         << " events=" << configuration.event_count
         << " submitted=" << result.submitted
         << " acknowledged=" << result.acknowledged
         << " protocol_errors=" << result.protocol_errors
         << " plan_notifications=" << result.plan_notifications
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
         << " transport_ok=" << result.transport_ok
         << " transport_message=" << std::quoted(result.transport_message)
         << '\n';
}

}  // namespace liveroute::load
