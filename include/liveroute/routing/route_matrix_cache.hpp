#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <atomic>
#include <deque>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

#include "liveroute/domain/travel_time_matrix.hpp"
#include "liveroute/domain/types.hpp"

namespace liveroute::routing {

struct RouteMatrixCacheConfig {
  bool enabled{true};
  std::string policy_version{"liveroute-route-cache-v1"};
  std::size_t shard_count{16};
  std::size_t max_entries{131072};
  std::size_t max_bytes{67108864};
  std::size_t coordinate_scale{100000};
  std::chrono::seconds time_bucket{900};
  std::chrono::seconds fresh_ttl{21600};
  std::chrono::seconds stale_if_error_max_age{86400};
  std::size_t eviction_scan_limit{64};

  [[nodiscard]] bool is_valid() const noexcept;
};

struct RouteCacheKey {
  std::int32_t origin_latitude_e5{};
  std::int32_t origin_longitude_e5{};
  std::int32_t destination_latitude_e5{};
  std::int32_t destination_longitude_e5{};
  std::int64_t departure_bucket{};
  domain::TravelMode travel_mode{domain::TravelMode::kWalking};
  std::uint32_t provider_namespace{};

  friend bool operator==(const RouteCacheKey&, const RouteCacheKey&) = default;
};

struct RouteCacheMetricsSnapshot {
  std::uint64_t fresh_hits{};
  std::uint64_t misses{};
  std::uint64_t stale_hits{};
  std::uint64_t insertions{};
  std::uint64_t evictions{};
  std::uint64_t entries{};
  std::uint64_t bytes{};
  std::uint64_t max_entries{};
  std::uint64_t max_bytes{};
  std::uint64_t lookup_count{};
  std::uint64_t lookup_sum_microseconds{};
  std::uint64_t lookup_max_microseconds{};
};

class RouteMatrixCache {
 public:
  RouteMatrixCache(RouteMatrixCacheConfig config,
                   std::vector<std::string> provider_identities);
  ~RouteMatrixCache();

  RouteMatrixCache(const RouteMatrixCache&) = delete;
  RouteMatrixCache& operator=(const RouteMatrixCache&) = delete;

  [[nodiscard]] bool enabled() const noexcept { return config_.enabled; }
  [[nodiscard]] std::uint32_t provider_namespace(
      std::string_view identity) const;

  [[nodiscard]] std::optional<domain::RouteEstimate> lookup_fresh(
      const RouteCacheKey& key,
      std::chrono::steady_clock::time_point now);
  [[nodiscard]] std::optional<domain::RouteEstimate> lookup_stale(
      const RouteCacheKey& key,
      std::chrono::steady_clock::time_point now);
  void insert(const RouteCacheKey& key, domain::RouteEstimate estimate,
              std::chrono::steady_clock::time_point now);

  [[nodiscard]] RouteCacheMetricsSnapshot metrics() const;

  [[nodiscard]] static std::optional<std::int32_t> coordinate_e5(
      double coordinate) noexcept;
  [[nodiscard]] static std::int64_t departure_bucket(
      std::chrono::system_clock::time_point departure,
      std::chrono::seconds bucket) noexcept;
  [[nodiscard]] static std::uint64_t hash_key(
      const RouteCacheKey& key) noexcept;

 private:
  struct Slot {
    RouteCacheKey key{};
    domain::RouteEstimate estimate{};
    std::chrono::steady_clock::time_point inserted_at{};
    bool occupied{};
    bool tombstone{};
    bool referenced{};
  };

  struct Shard {
    mutable std::mutex mutex;
    std::vector<Slot> slots;
    std::size_t hand{};
    std::size_t entries{};
  };

  [[nodiscard]] std::size_t shard_index(const RouteCacheKey& key) const
      noexcept;
  [[nodiscard]] static std::size_t next_power_of_two(
      std::size_t value);
  [[nodiscard]] std::size_t find_slot(const Shard& shard,
                                      const RouteCacheKey& key) const noexcept;
  [[nodiscard]] std::size_t find_insert_slot(
      const Shard& shard, const RouteCacheKey& key) const noexcept;
  [[nodiscard]] std::size_t choose_eviction_slot(
      Shard& shard, std::chrono::steady_clock::time_point now);
  [[nodiscard]] std::optional<domain::RouteEstimate> lookup(
      const RouteCacheKey& key, std::chrono::steady_clock::time_point now,
      bool stale);
  void record_lookup(std::chrono::steady_clock::time_point started,
                     bool hit) noexcept;

  RouteMatrixCacheConfig config_;
  std::vector<std::pair<std::string, std::uint32_t>> namespaces_;
  std::deque<Shard> shards_;
  std::size_t max_entries_per_shard_{};
  std::uint64_t storage_bytes_{};
  std::atomic<std::uint64_t> fresh_hits_{};
  std::atomic<std::uint64_t> misses_{};
  std::atomic<std::uint64_t> stale_hits_{};
  std::atomic<std::uint64_t> insertions_{};
  std::atomic<std::uint64_t> evictions_{};
  std::atomic<std::uint64_t> lookup_count_{};
  std::atomic<std::uint64_t> lookup_sum_microseconds_{};
  std::atomic<std::uint64_t> lookup_max_microseconds_{};
};

}  // namespace liveroute::routing
