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
                     std::span<const domain::Location> locations,
                     std::span<const std::size_t> sources,
                     std::span<const std::size_t> destinations) {
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
  result.append("?sources=");
  const auto append_indexes = [&result](std::span<const std::size_t> indexes) {
    bool first_index = true;
    for (const auto index : indexes) {
      if (!first_index) result.push_back(';');
      first_index = false;
      result.append(std::to_string(index));
    }
  };
  append_indexes(sources);
  result.append("&destinations=");
  append_indexes(destinations);
  result.append("&annotations=duration,distance");
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
        config_.connect_timeout > config_.request_timeout ||
        (config_.route_cache.has_value() && config_.dataset_version.empty())) {
      throw std::invalid_argument("invalid OSRM provider configuration");
    }
    if (config_.route_cache.has_value() && config_.route_cache->enabled) {
      cache_.emplace(*config_.route_cache,
                     std::vector<std::string>{config_.dataset_version + ":car",
                                              config_.dataset_version + ":foot"});
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
    if (!cache_.has_value()) {
      std::vector<std::size_t> indexes(locations.size());
      for (std::size_t index = 0; index < indexes.size(); ++index) {
        indexes[index] = index;
      }
      const auto url = make_url(endpoint, profile, locations, indexes, indexes);
      if (url.size() > config_.max_encoded_request_bytes) {
        return provider_error(TravelTimeProviderError::kMatrixTooLarge);
      }
      auto lease = acquire(*slots);
      if (lease.slot == nullptr) {
        return provider_error(TravelTimeProviderError::kResourceExhausted);
      }
      const auto result = perform(*lease.slot, url, locations.size(),
                                  locations.size(), deadline, stop_token);
      if (const auto* error = std::get_if<TravelTimeProviderError>(&result)) {
        return provider_error(*error);
      }
      const auto& grid = std::get<OsrmTableGrid>(result);
      return TravelTimeLookupResult{domain::TravelTimeMatrix{
          grid.row_count, grid.estimates}};
    }
    return get_matrix_cached(locations, mode, endpoint, profile, *slots,
                             deadline, stop_token);
  }

 private:
  struct Cell {
    std::int32_t latitude_e5{};
    std::int32_t longitude_e5{};

    friend bool operator==(const Cell&, const Cell&) = default;
  };

  static Cell cell_for(const domain::Location& location) {
    const auto latitude = RouteMatrixCache::coordinate_e5(location.latitude);
    const auto longitude =
        RouteMatrixCache::coordinate_e5(location.longitude);
    if (!latitude.has_value() || !longitude.has_value()) {
      throw std::invalid_argument("location cannot be encoded for route cache");
    }
    return Cell{*latitude, *longitude};
  }

  static domain::Location location_for(Cell cell) {
    return {.latitude = static_cast<double>(cell.latitude_e5) / 100000.0,
            .longitude = static_cast<double>(cell.longitude_e5) / 100000.0};
  }

  static std::size_t find_cell(const std::vector<Cell>& cells, Cell cell) {
    const auto found = std::find(cells.begin(), cells.end(), cell);
    return static_cast<std::size_t>(found - cells.begin());
  }

  RouteCacheKey cache_key(Cell origin, Cell destination,
                          domain::TravelMode mode) const {
    const auto identity = config_.dataset_version +
                           (mode == domain::TravelMode::kDriving ? ":car"
                                                                  : ":foot");
    return {.origin_latitude_e5 = origin.latitude_e5,
            .origin_longitude_e5 = origin.longitude_e5,
            .destination_latitude_e5 = destination.latitude_e5,
            .destination_longitude_e5 = destination.longitude_e5,
            .departure_bucket = 0,
            .travel_mode = mode,
            .provider_namespace = cache_->provider_namespace(identity)};
  }

  TravelTimeLookupResult get_matrix_cached(
      std::span<const domain::Location> locations, domain::TravelMode mode,
      std::string_view endpoint, std::string_view profile,
      std::vector<std::unique_ptr<CurlSlot>>& slots,
      domain::Deadline deadline, std::stop_token stop_token) {
    const auto now = std::chrono::steady_clock::now();
    std::vector<Cell> cells;
    cells.reserve(locations.size());
    for (const auto& location : locations) cells.push_back(cell_for(location));

    const auto cell_key = [&](std::size_t origin, std::size_t destination) {
      return cache_key(cells[origin], cells[destination], mode);
    };
    std::vector<std::optional<domain::RouteEstimate>> assembled(
        locations.size() * locations.size());
    std::vector<Cell> source_cells;
    std::vector<Cell> destination_cells;
    bool has_miss = false;
    for (std::size_t origin = 0; origin < locations.size(); ++origin) {
      for (std::size_t destination = 0; destination < locations.size();
           ++destination) {
        const auto index = origin * locations.size() + destination;
        assembled[index] = cache_->lookup_fresh(cell_key(origin, destination),
                                                now);
        if (assembled[index].has_value()) continue;
        has_miss = true;
        if (find_cell(source_cells, cells[origin]) == source_cells.size()) {
          source_cells.push_back(cells[origin]);
        }
        if (find_cell(destination_cells, cells[destination]) ==
            destination_cells.size()) {
          destination_cells.push_back(cells[destination]);
        }
      }
    }
    if (!has_miss) {
      std::vector<domain::RouteEstimate> estimates;
      estimates.reserve(assembled.size());
      for (const auto& estimate : assembled) estimates.push_back(*estimate);
      return TravelTimeLookupResult{
          domain::TravelTimeMatrix{locations.size(), std::move(estimates)}};
    }

    std::vector<Cell> combined_cells = source_cells;
    for (const auto destination : destination_cells) {
      if (find_cell(combined_cells, destination) == combined_cells.size()) {
        combined_cells.push_back(destination);
      }
    }
    std::vector<domain::Location> combined_locations;
    combined_locations.reserve(combined_cells.size());
    for (const auto cell : combined_cells) {
      combined_locations.push_back(location_for(cell));
    }
    std::vector<std::size_t> source_indexes;
    std::vector<std::size_t> destination_indexes;
    source_indexes.reserve(source_cells.size());
    destination_indexes.reserve(destination_cells.size());
    for (const auto source : source_cells) {
      source_indexes.push_back(find_cell(combined_cells, source));
    }
    for (const auto destination : destination_cells) {
      destination_indexes.push_back(find_cell(combined_cells, destination));
    }
    const auto url = make_url(endpoint, profile, combined_locations,
                              source_indexes, destination_indexes);
    if (url.size() > config_.max_encoded_request_bytes) {
      return provider_error(TravelTimeProviderError::kMatrixTooLarge);
    }
    auto lease = acquire(slots);
    if (lease.slot == nullptr) {
      return provider_error(TravelTimeProviderError::kResourceExhausted);
    }
    const auto result = perform(*lease.slot, url, source_cells.size(),
                                destination_cells.size(), deadline,
                                stop_token);
    if (const auto* error = std::get_if<TravelTimeProviderError>(&result)) {
      if (*error != TravelTimeProviderError::kProviderUnavailable) {
        return provider_error(*error);
      }
      for (std::size_t origin = 0; origin < locations.size(); ++origin) {
        for (std::size_t destination = 0; destination < locations.size();
             ++destination) {
          const auto index = origin * locations.size() + destination;
          if (assembled[index].has_value()) continue;
          assembled[index] = cache_->lookup_stale(
              cell_key(origin, destination), now);
          if (!assembled[index].has_value()) {
            return provider_error(*error);
          }
        }
      }
      std::vector<domain::RouteEstimate> estimates;
      estimates.reserve(assembled.size());
      for (const auto& estimate : assembled) estimates.push_back(*estimate);
      return TravelTimeLookupResult{
          domain::TravelTimeMatrix{locations.size(), std::move(estimates)},
          TravelTimeLookupQuality::kStaleCache};
    }

    const auto& grid = std::get<OsrmTableGrid>(result);
    for (std::size_t source = 0; source < source_cells.size(); ++source) {
      for (std::size_t destination = 0; destination < destination_cells.size();
           ++destination) {
        auto estimate = grid.at(source, destination);
        if (grid.no_table && source_cells[source] == destination_cells[destination]) {
          estimate = {.duration = std::chrono::seconds::zero(),
                      .distance_meters = 0,
                      .reachable = true};
        }
        cache_->insert(cache_key(source_cells[source], destination_cells[destination],
                                 mode), estimate, now);
      }
    }
    for (std::size_t origin = 0; origin < locations.size(); ++origin) {
      for (std::size_t destination = 0; destination < locations.size();
           ++destination) {
        const auto index = origin * locations.size() + destination;
        if (assembled[index].has_value()) continue;
        const auto source = find_cell(source_cells, cells[origin]);
        const auto target = find_cell(destination_cells, cells[destination]);
        auto estimate = grid.at(source, target);
        if (grid.no_table && cells[origin] == cells[destination]) {
          estimate = {.duration = std::chrono::seconds::zero(),
                      .distance_meters = 0,
                      .reachable = true};
        }
        assembled[index] = estimate;
      }
    }
    std::vector<domain::RouteEstimate> estimates;
    estimates.reserve(assembled.size());
    for (const auto& estimate : assembled) {
      if (!estimate.has_value()) {
        return provider_error(TravelTimeProviderError::kProviderUnavailable);
      }
      estimates.push_back(*estimate);
    }
    return TravelTimeLookupResult{
        domain::TravelTimeMatrix{locations.size(), std::move(estimates)}};
  }

  static SlotLease acquire(std::vector<std::unique_ptr<CurlSlot>>& slots) {
    for (auto& slot : slots) {
      std::unique_lock lock{slot->mutex, std::try_to_lock};
      if (lock.owns_lock()) return SlotLease{slot.get(), std::move(lock)};
    }
    return {};
  }

  OsrmTableGridResult perform(CurlSlot& slot, const std::string& url,
                              std::size_t row_count,
                              std::size_t column_count,
                              domain::Deadline deadline,
                              std::stop_token stop_token) {
    auto* easy = curl_easy_init();
    if (easy == nullptr) return TravelTimeProviderError::kInternal;
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
      return TravelTimeProviderError::kInternal;
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
      return TravelTimeProviderError::kInternal;
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
      return TravelTimeProviderError::kCancelled;
    }
    if (std::chrono::steady_clock::now() >= deadline ||
        easy_code == CURLE_OPERATION_TIMEDOUT) {
      return TravelTimeProviderError::kDeadlineExceeded;
    }
    if (response.exceeded) {
      return TravelTimeProviderError::kResourceExhausted;
    }
    if (easy_code != CURLE_OK) {
      return TravelTimeProviderError::kProviderUnavailable;
    }

    long status{};
    if (curl_easy_getinfo(easy, CURLINFO_RESPONSE_CODE, &status) != CURLE_OK) {
      return TravelTimeProviderError::kInternal;
    }
    const auto parsed = parse_osrm_table_response_grid(
        response.body, row_count, column_count);
    if (status == 429) {
      return TravelTimeProviderError::kResourceExhausted;
    }
    if (status >= 500) {
      return TravelTimeProviderError::kProviderUnavailable;
    }
    if (status >= 400 && status < 500) {
      if (is_recognized_osrm_error_response(response.body)) return parsed;
      return TravelTimeProviderError::kInternal;
    }
    if (status < 200 || status >= 300) {
      return TravelTimeProviderError::kInternal;
    }
    return parsed;
  }

  OsrmTravelTimeProviderConfig config_;
  std::optional<RouteMatrixCache> cache_;
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
