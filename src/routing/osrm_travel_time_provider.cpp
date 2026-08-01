#include "liveroute/routing/osrm_travel_time_provider.hpp"

#include <algorithm>
#include <array>
#include <charconv>
#include <chrono>
#include <cstdint>
#include <curl/curl.h>
#include <limits>
#include <mutex>
#include <stdexcept>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "liveroute/routing/osrm_response.hpp"

namespace liveroute::routing {
namespace {

TravelTimeLookupResult provider_error(TravelTimeProviderError error) {
  return TravelTimeLookupResult{error};
}

bool valid_endpoint(std::string_view endpoint) {
  return endpoint.starts_with("http://") && endpoint.size() > 7 &&
         endpoint.find_first_of("?#") == std::string_view::npos;
}

void append_double(std::string& output, double value) {
  std::array<char, 32> buffer{};
  const auto [end, error] = std::to_chars(
      buffer.data(), buffer.data() + buffer.size(), value,
      std::chars_format::general, std::numeric_limits<double>::max_digits10);
  if (error != std::errc{}) {
    throw std::invalid_argument("coordinate cannot be encoded");
  }
  output.append(buffer.data(), end);
}

std::string make_url(std::string_view endpoint, std::string_view profile,
                     std::span<const domain::Location> locations) {
  std::string result{endpoint};
  if (result.back() == '/') result.pop_back();
  result.append("/table/v1/").append(profile).push_back('/');
  bool first = true;
  for (const auto& location : locations) {
    if (!first) result.push_back(';');
    first = false;
    append_double(result, location.longitude);
    result.push_back(',');
    append_double(result, location.latitude);
  }
  result.append("?annotations=duration,distance");
  return result;
}

struct ResponseBuffer {
  std::string body;
  std::size_t limit{};
  bool exceeded{};
};

std::size_t write_response(char* data, std::size_t size, std::size_t count,
                           void* opaque) {
  auto& response = *static_cast<ResponseBuffer*>(opaque);
  if (size != 0 && count > std::numeric_limits<std::size_t>::max() / size) {
    response.exceeded = true;
    return 0;
  }
  const auto bytes = size * count;
  if (bytes > response.limit - std::min(response.limit, response.body.size())) {
    response.exceeded = true;
    return 0;
  }
  response.body.append(data, bytes);
  return bytes;
}

struct ProgressState {
  domain::Deadline deadline;
  std::stop_token stop_token;
};

int progress(void* opaque, curl_off_t, curl_off_t, curl_off_t, curl_off_t) {
  const auto& state = *static_cast<ProgressState*>(opaque);
  return state.stop_token.stop_requested() ||
                 std::chrono::steady_clock::now() >= state.deadline
             ? 1
             : 0;
}

class CurlGlobal {
 public:
  CurlGlobal() {
    if (curl_global_init(CURL_GLOBAL_DEFAULT) != CURLE_OK) {
      throw std::runtime_error("libcurl initialization failed");
    }
  }
  ~CurlGlobal() { curl_global_cleanup(); }
};

CurlGlobal& curl_global() {
  static CurlGlobal value;
  return value;
}

struct CurlSlot {
  CurlSlot() : multi(curl_multi_init()) {
    if (multi == nullptr) throw std::runtime_error("curl multi initialization failed");
  }
  ~CurlSlot() { curl_multi_cleanup(multi); }
  std::mutex mutex;
  CURLM* multi;
};

struct SlotLease {
  CurlSlot* slot{};
  std::unique_lock<std::mutex> lock;
};

}  // namespace

class OsrmTravelTimeProvider::Impl {
 public:
  explicit Impl(OsrmTravelTimeProviderConfig config) : config_(std::move(config)) {
    static_cast<void>(curl_global());
    if (!valid_endpoint(config_.car_endpoint) ||
        !valid_endpoint(config_.foot_endpoint) || config_.max_locations == 0 ||
        config_.max_matrix_cells < config_.max_locations ||
        config_.max_encoded_request_bytes == 0 || config_.max_response_bytes == 0 ||
        config_.per_profile_concurrency == 0 || config_.connect_timeout.count() <= 0 ||
        config_.request_timeout.count() <= 0 ||
        config_.connect_timeout > config_.request_timeout) {
      throw std::invalid_argument("invalid OSRM provider configuration");
    }
    car_slots_.reserve(config_.per_profile_concurrency);
    foot_slots_.reserve(config_.per_profile_concurrency);
    for (std::size_t index = 0; index < config_.per_profile_concurrency; ++index) {
      car_slots_.push_back(std::make_unique<CurlSlot>());
      foot_slots_.push_back(std::make_unique<CurlSlot>());
    }
  }

  TravelTimeLookupResult get_matrix(
      std::span<const domain::Location> locations, domain::TravelMode mode,
      domain::Deadline deadline, std::stop_token stop_token) {
    if (locations.empty()) {
      return provider_error(TravelTimeProviderError::kInvalidArgument);
    }
    for (const auto& location : locations) {
      if (!location.is_valid()) {
        return provider_error(TravelTimeProviderError::kInvalidArgument);
      }
    }
    if (locations.size() > config_.max_locations ||
        locations.size() > std::numeric_limits<std::size_t>::max() / locations.size() ||
        locations.size() * locations.size() > config_.max_matrix_cells) {
      return provider_error(TravelTimeProviderError::kMatrixTooLarge);
    }
    if (stop_token.stop_requested()) {
      return provider_error(TravelTimeProviderError::kCancelled);
    }
    if (std::chrono::steady_clock::now() >= deadline) {
      return provider_error(TravelTimeProviderError::kDeadlineExceeded);
    }

    std::string_view endpoint;
    std::string_view profile;
    std::vector<std::unique_ptr<CurlSlot>>* slots{};
    switch (mode) {
      case domain::TravelMode::kDriving:
        endpoint = config_.car_endpoint;
        profile = "driving";
        slots = &car_slots_;
        break;
      case domain::TravelMode::kWalking:
        endpoint = config_.foot_endpoint;
        profile = "walking";
        slots = &foot_slots_;
        break;
      default:
        return provider_error(TravelTimeProviderError::kInvalidArgument);
    }
    const auto url = make_url(endpoint, profile, locations);
    if (url.size() > config_.max_encoded_request_bytes) {
      return provider_error(TravelTimeProviderError::kMatrixTooLarge);
    }
    auto lease = acquire(*slots);
    if (lease.slot == nullptr) {
      return provider_error(TravelTimeProviderError::kResourceExhausted);
    }
    return perform(*lease.slot, url, locations.size(), deadline, stop_token);
  }

 private:
  static SlotLease acquire(std::vector<std::unique_ptr<CurlSlot>>& slots) {
    for (auto& slot : slots) {
      std::unique_lock lock{slot->mutex, std::try_to_lock};
      if (lock.owns_lock()) return SlotLease{slot.get(), std::move(lock)};
    }
    return {};
  }

  TravelTimeLookupResult perform(CurlSlot& slot, const std::string& url,
                                 std::size_t location_count,
                                 domain::Deadline deadline,
                                 std::stop_token stop_token) {
    auto* easy = curl_easy_init();
    if (easy == nullptr) return provider_error(TravelTimeProviderError::kInternal);
    struct EasyCleanup {
      CURL* value;
      ~EasyCleanup() { curl_easy_cleanup(value); }
    } cleanup{easy};

    ResponseBuffer response{{}, config_.max_response_bytes, false};
    ProgressState progress_state{deadline, stop_token};
    const auto remaining = std::chrono::duration_cast<std::chrono::milliseconds>(
        deadline - std::chrono::steady_clock::now());
    const auto total_timeout = std::max(
        std::chrono::milliseconds{1}, std::min(config_.request_timeout, remaining));
    const auto connect_timeout = std::min(config_.connect_timeout, total_timeout);

    curl_easy_setopt(easy, CURLOPT_URL, url.c_str());
    curl_easy_setopt(easy, CURLOPT_HTTPGET, 1L);
    curl_easy_setopt(easy, CURLOPT_NOSIGNAL, 1L);
    curl_easy_setopt(easy, CURLOPT_ACCEPT_ENCODING, "");
    curl_easy_setopt(easy, CURLOPT_WRITEFUNCTION, write_response);
    curl_easy_setopt(easy, CURLOPT_WRITEDATA, &response);
    curl_easy_setopt(easy, CURLOPT_XFERINFOFUNCTION, progress);
    curl_easy_setopt(easy, CURLOPT_XFERINFODATA, &progress_state);
    curl_easy_setopt(easy, CURLOPT_NOPROGRESS, 0L);
    curl_easy_setopt(easy, CURLOPT_CONNECTTIMEOUT_MS,
                     static_cast<long>(connect_timeout.count()));
    curl_easy_setopt(easy, CURLOPT_TIMEOUT_MS,
                     static_cast<long>(total_timeout.count()));

    if (curl_multi_add_handle(slot.multi, easy) != CURLM_OK) {
      return provider_error(TravelTimeProviderError::kInternal);
    }
    struct MultiRemoval {
      CURLM* multi;
      CURL* easy;
      ~MultiRemoval() { curl_multi_remove_handle(multi, easy); }
    } removal{slot.multi, easy};

    int running{};
    auto multi_code = curl_multi_perform(slot.multi, &running);
    while (multi_code == CURLM_OK && running != 0) {
      int descriptors{};
      multi_code = curl_multi_poll(slot.multi, nullptr, 0, 25, &descriptors);
      static_cast<void>(descriptors);
      if (multi_code == CURLM_OK) multi_code = curl_multi_perform(slot.multi, &running);
    }
    if (multi_code != CURLM_OK) {
      return provider_error(TravelTimeProviderError::kInternal);
    }
    CURLcode easy_code = CURLE_FAILED_INIT;
    int messages{};
    while (auto* message = curl_multi_info_read(slot.multi, &messages)) {
      if (message->msg == CURLMSG_DONE && message->easy_handle == easy) {
        easy_code = message->data.result;
        break;
      }
    }
    if (stop_token.stop_requested()) {
      return provider_error(TravelTimeProviderError::kCancelled);
    }
    if (std::chrono::steady_clock::now() >= deadline ||
        easy_code == CURLE_OPERATION_TIMEDOUT) {
      return provider_error(TravelTimeProviderError::kDeadlineExceeded);
    }
    if (response.exceeded) {
      return provider_error(TravelTimeProviderError::kResourceExhausted);
    }
    if (easy_code != CURLE_OK) {
      return provider_error(TravelTimeProviderError::kProviderUnavailable);
    }

    long status{};
    if (curl_easy_getinfo(easy, CURLINFO_RESPONSE_CODE, &status) != CURLE_OK) {
      return provider_error(TravelTimeProviderError::kInternal);
    }
    const auto parsed = parse_osrm_table_response(response.body, location_count);
    if (status == 429) {
      return provider_error(TravelTimeProviderError::kResourceExhausted);
    }
    if (status >= 500) {
      return provider_error(TravelTimeProviderError::kProviderUnavailable);
    }
    if (status >= 400 && status < 500) {
      if (is_recognized_osrm_error_response(response.body)) return parsed;
      return provider_error(TravelTimeProviderError::kInternal);
    }
    if (status < 200 || status >= 300) {
      return provider_error(TravelTimeProviderError::kInternal);
    }
    return parsed;
  }

  OsrmTravelTimeProviderConfig config_;
  std::vector<std::unique_ptr<CurlSlot>> car_slots_;
  std::vector<std::unique_ptr<CurlSlot>> foot_slots_;
};

OsrmTravelTimeProvider::OsrmTravelTimeProvider(
    OsrmTravelTimeProviderConfig config)
    : impl_(std::make_unique<Impl>(std::move(config))) {}

OsrmTravelTimeProvider::~OsrmTravelTimeProvider() = default;

TravelTimeLookupResult OsrmTravelTimeProvider::get_matrix(
    std::span<const domain::Location> locations, domain::TravelMode mode,
    std::chrono::system_clock::time_point departure_time,
    domain::Deadline deadline, std::stop_token stop_token) {
  static_cast<void>(departure_time);
  return impl_->get_matrix(locations, mode, deadline, stop_token);
}

}  // namespace liveroute::routing
