#include "liveroute/routing/route_matrix_cache.hpp"

#include <algorithm>
#include <array>
#include <charconv>
#include <cmath>
#include <limits>
#include <string_view>
#include <stdexcept>
#include <type_traits>
#include <utility>

namespace liveroute::routing {
namespace {

constexpr std::uint64_t kFnvOffset = 14695981039346656037ULL;
constexpr std::uint64_t kFnvPrime = 1099511628211ULL;

template <typename Integer>
void append_big_endian(std::uint64_t& hash, Integer value) noexcept {
  using Unsigned = std::make_unsigned_t<Integer>;
  const auto unsigned_value = static_cast<Unsigned>(value);
  for (std::size_t shift = sizeof(Integer); shift != 0; --shift) {
    const auto byte = static_cast<std::uint8_t>(
        unsigned_value >> ((shift - 1) * 8));
    hash ^= byte;
    hash *= kFnvPrime;
  }
}

[[nodiscard]] std::uint64_t elapsed_microseconds(
    std::chrono::steady_clock::time_point start,
    std::chrono::steady_clock::time_point end) noexcept {
  if (end <= start) return 0;
  return static_cast<std::uint64_t>(
      std::chrono::duration_cast<std::chrono::microseconds>(end - start)
          .count());
}

[[nodiscard]] std::optional<std::int32_t> round_decimal_e5(
    double coordinate) noexcept {
  // Quantize the shortest decimal representation of the input.  This keeps
  // decimal half-way inputs such as 1.234565 at an exact half after scaling;
  // multiplying the binary double directly can place that value just below
  // the half and violate the wire/storage rounding rule.
  char buffer[64]{};
  const auto converted = std::to_chars(buffer, buffer + sizeof(buffer),
                                       coordinate);
  if (converted.ec != std::errc{}) return std::nullopt;
  const std::string_view text(buffer,
                              static_cast<std::size_t>(converted.ptr - buffer));

  std::size_t position = 0;
  bool negative = false;
  if (position < text.size() && (text[position] == '-' || text[position] == '+')) {
    negative = text[position] == '-';
    ++position;
  }

  std::uint64_t significand = 0;
  std::size_t fractional_digits = 0;
  bool after_decimal = false;
  while (position < text.size() && text[position] != 'e' &&
         text[position] != 'E') {
    const char character = text[position++];
    if (character == '.') {
      after_decimal = true;
      continue;
    }
    if (character < '0' || character > '9' ||
        significand > (std::numeric_limits<std::uint64_t>::max() -
                       static_cast<std::uint64_t>(character - '0')) /
                          10) {
      return std::nullopt;
    }
    significand = significand * 10 + static_cast<std::uint64_t>(character - '0');
    if (after_decimal) ++fractional_digits;
  }

  int exponent = 0;
  if (position < text.size()) {
    ++position;
    bool exponent_negative = false;
    if (position < text.size() &&
        (text[position] == '-' || text[position] == '+')) {
      exponent_negative = text[position] == '-';
      ++position;
    }
    while (position < text.size()) {
      const char character = text[position++];
      if (character < '0' || character > '9' || exponent > 1000) {
        return std::nullopt;
      }
      exponent = exponent * 10 + (character - '0');
    }
    if (exponent_negative) exponent = -exponent;
  }
  if (significand == 0) return 0;

  const auto decimal_power = static_cast<int>(5) + exponent -
                              static_cast<int>(fractional_digits);
  std::uint64_t magnitude = significand;
  if (decimal_power >= 0) {
    if (decimal_power > 19) return std::nullopt;
    for (int index = 0; index < decimal_power; ++index) {
      if (magnitude > std::numeric_limits<std::uint64_t>::max() / 10) {
        return std::nullopt;
      }
      magnitude *= 10;
    }
    if (magnitude >
        static_cast<std::uint64_t>(std::numeric_limits<std::int32_t>::max()) +
            1) {
      return std::nullopt;
    }
  } else {
    const auto divisor_power = -decimal_power;
    if (divisor_power > 19) return 0;
    std::uint64_t divisor = 1;
    for (int index = 0; index < divisor_power; ++index) divisor *= 10;
    const std::uint64_t remainder = magnitude % divisor;
    magnitude /= divisor;
    if (remainder >= divisor / 2) ++magnitude;
  }

  const auto signed_magnitude = static_cast<std::int64_t>(magnitude);
  const auto result = negative ? -signed_magnitude : signed_magnitude;
  if (result < std::numeric_limits<std::int32_t>::min() ||
      result > std::numeric_limits<std::int32_t>::max()) {
    return std::nullopt;
  }
  return static_cast<std::int32_t>(result);
}

}  // namespace

bool RouteMatrixCacheConfig::is_valid() const noexcept {
  return policy_version == "liveroute-route-cache-v1" && shard_count != 0 &&
         max_entries != 0 && max_bytes != 0 && coordinate_scale == 100000 &&
         time_bucket == std::chrono::seconds{900} &&
         fresh_ttl == std::chrono::seconds{21600} &&
         stale_if_error_max_age == std::chrono::seconds{86400} &&
         eviction_scan_limit == 64 && max_entries >= shard_count &&
         max_entries % shard_count == 0;
}

RouteMatrixCache::RouteMatrixCache(
    RouteMatrixCacheConfig config,
    std::vector<std::string> provider_identities)
    : config_(std::move(config)) {
  if (!config_.is_valid()) {
    throw std::invalid_argument("invalid route cache configuration");
  }
  std::sort(provider_identities.begin(), provider_identities.end());
  provider_identities.erase(
      std::unique(provider_identities.begin(), provider_identities.end()),
      provider_identities.end());
  if (provider_identities.empty() || provider_identities.size() >=
                                        std::numeric_limits<std::uint32_t>::max()) {
    throw std::invalid_argument("route cache provider identities are invalid");
  }
  std::uint32_t namespace_id = 1;
  for (auto& identity : provider_identities) {
    namespaces_.emplace_back(std::move(identity), namespace_id++);
  }
  if (!config_.enabled) return;

  const auto entries_per_shard = config_.max_entries / config_.shard_count;
  if (entries_per_shard > std::numeric_limits<std::size_t>::max() / 2) {
    throw std::invalid_argument("route cache entry count overflows");
  }
  const auto slots_per_shard = next_power_of_two(entries_per_shard * 2);
  if (slots_per_shard > std::numeric_limits<std::size_t>::max() /
                             config_.shard_count ||
      sizeof(Slot) > std::numeric_limits<std::size_t>::max() /
                          (slots_per_shard * config_.shard_count)) {
    throw std::invalid_argument("route cache storage size overflows");
  }
  storage_bytes_ = static_cast<std::uint64_t>(
      sizeof(Slot) * slots_per_shard * config_.shard_count);
  for (const auto& entry : namespaces_) {
    storage_bytes_ += entry.first.capacity() + sizeof(entry);
  }
  if (storage_bytes_ > config_.max_bytes) {
    throw std::invalid_argument("route cache exceeds configured memory bound");
  }
  shards_.resize(config_.shard_count);
  max_entries_per_shard_ = entries_per_shard;
  for (auto& shard : shards_) shard.slots.resize(slots_per_shard);
}

RouteMatrixCache::~RouteMatrixCache() = default;

std::uint32_t RouteMatrixCache::provider_namespace(
    std::string_view identity) const {
  const auto found = std::lower_bound(
      namespaces_.begin(), namespaces_.end(), identity,
      [](const auto& entry, std::string_view value) {
        return entry.first < value;
      });
  if (found == namespaces_.end() || found->first != identity) {
    throw std::invalid_argument("unknown route cache provider identity");
  }
  return found->second;
}

std::optional<std::int32_t> RouteMatrixCache::coordinate_e5(
    double coordinate) noexcept {
  if (!std::isfinite(coordinate)) return std::nullopt;
  if (coordinate < -180.0 || coordinate > 180.0) return std::nullopt;
  return round_decimal_e5(coordinate);
}

std::int64_t RouteMatrixCache::departure_bucket(
    std::chrono::system_clock::time_point departure,
    std::chrono::seconds bucket) noexcept {
  const auto milliseconds = std::chrono::duration_cast<
      std::chrono::milliseconds>(departure.time_since_epoch()).count();
  const auto bucket_ms = bucket.count() * 1000;
  auto quotient = milliseconds / bucket_ms;
  if (milliseconds < 0 && milliseconds % bucket_ms != 0) --quotient;
  return quotient;
}

std::uint64_t RouteMatrixCache::hash_key(const RouteCacheKey& key) noexcept {
  auto hash = kFnvOffset;
  append_big_endian(hash, key.origin_latitude_e5);
  append_big_endian(hash, key.origin_longitude_e5);
  append_big_endian(hash, key.destination_latitude_e5);
  append_big_endian(hash, key.destination_longitude_e5);
  append_big_endian(hash, key.departure_bucket);
  hash ^= key.travel_mode == domain::TravelMode::kWalking ? 1U : 2U;
  hash *= kFnvPrime;
  append_big_endian(hash, key.provider_namespace);
  return hash;
}

std::size_t RouteMatrixCache::shard_index(const RouteCacheKey& key) const
    noexcept {
  return static_cast<std::size_t>(hash_key(key) % config_.shard_count);
}

std::size_t RouteMatrixCache::next_power_of_two(std::size_t value) {
  if (value < 2) return 2;
  std::size_t result = 1;
  while (result < value) {
    if (result > std::numeric_limits<std::size_t>::max() / 2) {
      throw std::invalid_argument("route cache slot count overflows");
    }
    result *= 2;
  }
  return result;
}

std::size_t RouteMatrixCache::find_slot(const Shard& shard,
                                        const RouteCacheKey& key) const
    noexcept {
  if (shard.slots.empty()) return std::numeric_limits<std::size_t>::max();
  const auto mask = shard.slots.size() - 1;
  const auto start = static_cast<std::size_t>(hash_key(key) & mask);
  for (std::size_t offset = 0; offset < shard.slots.size(); ++offset) {
    const auto index = (start + offset) & mask;
    const auto& slot = shard.slots[index];
    if (!slot.occupied) {
      if (!slot.tombstone) return std::numeric_limits<std::size_t>::max();
      continue;
    }
    if (slot.key == key) return index;
  }
  return std::numeric_limits<std::size_t>::max();
}

std::size_t RouteMatrixCache::find_insert_slot(
    const Shard& shard, const RouteCacheKey& key) const noexcept {
  if (shard.slots.empty()) return std::numeric_limits<std::size_t>::max();
  const auto mask = shard.slots.size() - 1;
  const auto start = static_cast<std::size_t>(hash_key(key) & mask);
  std::size_t tombstone = std::numeric_limits<std::size_t>::max();
  for (std::size_t offset = 0; offset < shard.slots.size(); ++offset) {
    const auto index = (start + offset) & mask;
    const auto& slot = shard.slots[index];
    if (slot.occupied && slot.key == key) return index;
    if (!slot.occupied) {
      if (slot.tombstone) {
        if (tombstone == std::numeric_limits<std::size_t>::max()) {
          tombstone = index;
        }
      } else {
        return tombstone == std::numeric_limits<std::size_t>::max()
                   ? index
                   : tombstone;
      }
    }
  }
  return tombstone;
}

std::size_t RouteMatrixCache::choose_eviction_slot(
    Shard& shard, std::chrono::steady_clock::time_point now) {
  std::size_t last_occupied = std::numeric_limits<std::size_t>::max();
  for (std::size_t offset = 0; offset < config_.eviction_scan_limit;
       ++offset) {
    const auto index = (shard.hand + offset) % shard.slots.size();
    auto& slot = shard.slots[index];
    if (!slot.occupied) {
      continue;
    }
    last_occupied = index;
    if (now - slot.inserted_at > config_.stale_if_error_max_age) {
      shard.hand = (index + 1) % shard.slots.size();
      return index;
    }
    if (slot.referenced) {
      slot.referenced = false;
      continue;
    }
    shard.hand = (index + 1) % shard.slots.size();
    return index;
  }
  if (last_occupied == std::numeric_limits<std::size_t>::max()) {
    return 0;
  }
  shard.hand = (last_occupied + 1) % shard.slots.size();
  return last_occupied;
}

void RouteMatrixCache::record_lookup(
    std::chrono::steady_clock::time_point started, bool hit) noexcept {
  const auto elapsed = elapsed_microseconds(started,
                                            std::chrono::steady_clock::now());
  lookup_count_.fetch_add(1, std::memory_order_relaxed);
  lookup_sum_microseconds_.fetch_add(elapsed, std::memory_order_relaxed);
  auto current = lookup_max_microseconds_.load(std::memory_order_relaxed);
  while (current < elapsed &&
         !lookup_max_microseconds_.compare_exchange_weak(
             current, elapsed, std::memory_order_relaxed,
             std::memory_order_relaxed)) {
  }
  if (hit) fresh_hits_.fetch_add(1, std::memory_order_relaxed);
}

std::optional<domain::RouteEstimate> RouteMatrixCache::lookup(
    const RouteCacheKey& key, std::chrono::steady_clock::time_point now,
    bool stale) {
  const auto started = std::chrono::steady_clock::now();
  if (!config_.enabled) {
    misses_.fetch_add(1, std::memory_order_relaxed);
    record_lookup(started, false);
    return std::nullopt;
  }
  auto& shard = shards_[shard_index(key)];
  std::scoped_lock lock(shard.mutex);
  const auto index = find_slot(shard, key);
  if (index == std::numeric_limits<std::size_t>::max()) {
    if (!stale) misses_.fetch_add(1, std::memory_order_relaxed);
    record_lookup(started, false);
    return std::nullopt;
  }
  auto& slot = shard.slots[index];
  const auto age = now >= slot.inserted_at ? now - slot.inserted_at
                                           : std::chrono::steady_clock::duration{};
  const bool fresh = age <= config_.fresh_ttl;
  const bool stale_eligible = age > config_.fresh_ttl &&
                              age <= config_.stale_if_error_max_age;
  if ((stale && !stale_eligible) || (!stale && !fresh)) {
    if (!stale) misses_.fetch_add(1, std::memory_order_relaxed);
    record_lookup(started, false);
    return std::nullopt;
  }
  if (stale) {
    stale_hits_.fetch_add(1, std::memory_order_relaxed);
  } else {
    slot.referenced = true;
  }
  record_lookup(started, !stale);
  return slot.estimate;
}

std::optional<domain::RouteEstimate> RouteMatrixCache::lookup_fresh(
    const RouteCacheKey& key, std::chrono::steady_clock::time_point now) {
  return lookup(key, now, false);
}

std::optional<domain::RouteEstimate> RouteMatrixCache::lookup_stale(
    const RouteCacheKey& key, std::chrono::steady_clock::time_point now) {
  return lookup(key, now, true);
}

void RouteMatrixCache::insert(
    const RouteCacheKey& key, domain::RouteEstimate estimate,
    std::chrono::steady_clock::time_point now) {
  if (!config_.enabled || !estimate.is_valid()) return;
  auto& shard = shards_[shard_index(key)];
  std::scoped_lock lock(shard.mutex);
  auto index = find_insert_slot(shard, key);
  const bool replacing = index != std::numeric_limits<std::size_t>::max() &&
                        shard.slots[index].occupied;
  if (!replacing && (index == std::numeric_limits<std::size_t>::max() ||
                     shard.entries >= max_entries_per_shard_)) {
    index = choose_eviction_slot(shard, now);
    auto& victim = shard.slots[index];
    if (victim.occupied) {
      victim.occupied = false;
      victim.tombstone = true;
      victim.referenced = false;
      --shard.entries;
      evictions_.fetch_add(1, std::memory_order_relaxed);
    }
    index = find_insert_slot(shard, key);
  }
  auto& slot = shard.slots[index];
  slot.key = key;
  slot.estimate = estimate;
  slot.inserted_at = now;
  slot.occupied = true;
  slot.tombstone = false;
  slot.referenced = true;
  if (!replacing) ++shard.entries;
  insertions_.fetch_add(1, std::memory_order_relaxed);
}

RouteCacheMetricsSnapshot RouteMatrixCache::metrics() const {
  RouteCacheMetricsSnapshot result{
      .fresh_hits = fresh_hits_.load(std::memory_order_relaxed),
      .misses = misses_.load(std::memory_order_relaxed),
      .stale_hits = stale_hits_.load(std::memory_order_relaxed),
      .insertions = insertions_.load(std::memory_order_relaxed),
      .evictions = evictions_.load(std::memory_order_relaxed),
      .bytes = storage_bytes_,
      .max_entries = config_.max_entries,
      .max_bytes = config_.max_bytes,
      .lookup_count = lookup_count_.load(std::memory_order_relaxed),
      .lookup_sum_microseconds =
          lookup_sum_microseconds_.load(std::memory_order_relaxed),
      .lookup_max_microseconds =
          lookup_max_microseconds_.load(std::memory_order_relaxed),
  };
  for (const auto& shard : shards_) {
    std::scoped_lock lock(shard.mutex);
    result.entries += shard.entries;
  }
  return result;
}

}  // namespace liveroute::routing
