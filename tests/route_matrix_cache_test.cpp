#include "liveroute/routing/route_matrix_cache.hpp"

#include <chrono>
#include <cstdlib>
#include <iostream>
#include <string>
#include <vector>

using namespace std::chrono_literals;
using liveroute::domain::RouteEstimate;
using liveroute::domain::TravelMode;
using liveroute::routing::RouteCacheKey;
using liveroute::routing::RouteMatrixCache;
using liveroute::routing::RouteMatrixCacheConfig;

namespace {

RouteMatrixCacheConfig config(std::size_t entries = 8,
                              std::size_t shards = 2) {
  return {.enabled = true,
          .policy_version = "liveroute-route-cache-v1",
          .shard_count = shards,
          .max_entries = entries,
          .max_bytes = 1U << 20,
          .coordinate_scale = 100000,
          .time_bucket = 900s,
          .fresh_ttl = 21600s,
          .stale_if_error_max_age = 86400s,
          .eviction_scan_limit = 64};
}

RouteCacheKey key(std::int32_t origin, std::int32_t destination,
                  TravelMode mode = TravelMode::kWalking) {
  return {.origin_latitude_e5 = origin,
          .origin_longitude_e5 = origin,
          .destination_latitude_e5 = destination,
          .destination_longitude_e5 = destination,
          .departure_bucket = 0,
          .travel_mode = mode,
          .provider_namespace = 1};
}

}  // namespace

int main() {
  const auto require = [](bool condition, const char* label) {
    if (!condition) {
      std::cerr << "route cache test failed: " << label << '\n';
      std::abort();
    }
  };

  const auto positive = RouteMatrixCache::coordinate_e5(1.234565);
  const auto negative = RouteMatrixCache::coordinate_e5(-1.234565);
  require(positive.has_value() && *positive == 123457, "positive E5 rounding");
  require(negative.has_value() && *negative == -123457, "negative E5 rounding");
  require(RouteMatrixCache::coordinate_e5(180.00001) == std::nullopt,
          "E5 range");

  require(RouteMatrixCache::departure_bucket(
              std::chrono::system_clock::time_point{std::chrono::milliseconds{-1}},
              15min) == -1, "negative time bucket");
  require(RouteMatrixCache::departure_bucket(
              std::chrono::system_clock::time_point{std::chrono::milliseconds{0}},
              15min) == 0, "zero time bucket");

  RouteMatrixCache cache(config(), {"dataset:car", "dataset:foot"});
  const auto now = std::chrono::steady_clock::now();
  const auto route = key(1, 2);
  cache.insert(route, RouteEstimate{30s, 100, true}, now);
  require(cache.lookup_fresh(route, now + 6h).has_value(), "fresh boundary");
  require(!cache.lookup_fresh(route, now + 6h + 1ms).has_value(),
          "fresh expiry");
  require(cache.lookup_stale(route, now + 6h + 1ms).has_value(),
          "stale boundary");
  require(!cache.lookup_stale(route, now + 24h + 1ms).has_value(),
          "stale expiry");

  const auto before = cache.metrics();
  require(before.fresh_hits == 1 && before.misses == 1 &&
              before.stale_hits == 1 && before.insertions == 1,
          "cache counters");

  RouteMatrixCache eviction_cache(config(4, 1), {"dataset:car"});
  const auto eviction_now = std::chrono::steady_clock::now();
  for (std::int32_t index = 0; index < 4; ++index) {
    eviction_cache.insert(key(index, index + 10),
                          RouteEstimate{1s, 1, true}, eviction_now);
  }
  for (std::int32_t index = 0; index < 4; ++index) {
    static_cast<void>(eviction_cache.lookup_fresh(
        key(index, index + 10), eviction_now));
  }
  eviction_cache.insert(key(99, 100), RouteEstimate{1s, 1, true},
                        eviction_now);
  require(eviction_cache.metrics().entries == 4, "entry bound");
  require(eviction_cache.metrics().evictions == 1, "eviction count");

  const auto first_hash = RouteMatrixCache::hash_key(key(3, 4));
  require(first_hash == RouteMatrixCache::hash_key(key(3, 4)),
          "hash determinism");
  require(first_hash !=
              RouteMatrixCache::hash_key(key(3, 4, TravelMode::kDriving)),
          "hash mode isolation");
  return 0;
}
