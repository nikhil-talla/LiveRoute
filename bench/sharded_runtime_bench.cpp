#include "liveroute/domain/types.hpp"
#include "liveroute/runtime/sharded_executor.hpp"

#include <algorithm>
#include <array>
#include <atomic>
#include <barrier>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <iostream>
#include <mutex>
#include <string_view>
#include <thread>
#include <vector>

namespace {

using Clock = std::chrono::steady_clock;
using liveroute::domain::TripId;
using liveroute::runtime::ShardedExecutor;

constexpr std::size_t kTripCount = 64;
constexpr std::size_t kOperations = 32768;
constexpr std::size_t kProducerCount = 8;

enum class MutexDesign : std::uint8_t {
  kGlobal,
  kPerTrip,
  kSharded,
};

struct BenchmarkResult {
  std::string_view design;
  std::size_t shard_count{};
  std::size_t producer_count{};
  std::size_t operations{};
  std::uint64_t elapsed_nanoseconds{};
  std::uint64_t total_lock_wait_nanoseconds{};
  std::uint64_t p99_lock_wait_nanoseconds{};
  std::uint64_t maximum_lock_wait_nanoseconds{};
  std::uint64_t total_queue_wait_nanoseconds{};
  std::uint64_t p99_queue_wait_nanoseconds{};
  std::uint64_t maximum_queue_wait_nanoseconds{};
  std::uint64_t checksum{};
  bool valid{};
};

[[nodiscard]] TripId make_trip_id(std::size_t ordinal) {
  std::array<std::byte, 16> bytes{};
  bytes[0] = static_cast<std::byte>(ordinal);
  bytes[15] = static_cast<std::byte>(ordinal ^ 0xa5U);
  return TripId{bytes};
}

[[nodiscard]] std::size_t shard_for(const TripId& trip_id,
                                    std::size_t shard_count) {
  std::uint64_t hash = 14695981039346656037ULL;
  for (const auto byte : trip_id.value()) {
    hash ^= std::to_integer<std::uint8_t>(byte);
    hash *= 1099511628211ULL;
  }
  return static_cast<std::size_t>(hash % shard_count);
}

void mutate(std::uint64_t& state, std::size_t operation) {
  auto value = state ^ (static_cast<std::uint64_t>(operation) +
                        0x9e3779b97f4a7c15ULL);
  for (std::size_t iteration = 0; iteration < 64; ++iteration) {
    value ^= value >> 12U;
    value ^= value << 25U;
    value ^= value >> 27U;
    value *= 0x2545f4914f6cdd1dULL;
  }
  state = value;
}

void update_maximum(std::atomic<std::uint64_t>& maximum,
                    std::uint64_t value) {
  auto current = maximum.load(std::memory_order_relaxed);
  while (current < value &&
         !maximum.compare_exchange_weak(
             current, value, std::memory_order_relaxed,
             std::memory_order_relaxed)) {
  }
}

[[nodiscard]] std::uint64_t p99(
    std::array<std::uint64_t, kOperations> samples) {
  std::sort(samples.begin(), samples.end());
  constexpr auto index = (kOperations * 99 + 99) / 100 - 1;
  return samples[index];
}

[[nodiscard]] std::uint64_t checksum(
    const std::array<std::uint64_t, kTripCount>& states) {
  std::uint64_t result{};
  for (const auto state : states) {
    result ^= state + 0x9e3779b97f4a7c15ULL +
              (result << 6U) + (result >> 2U);
  }
  return result;
}

[[nodiscard]] BenchmarkResult run_mutex_design(
    MutexDesign design, std::size_t shard_count) {
  std::vector<TripId> trip_ids;
  trip_ids.reserve(kTripCount);
  std::array<std::uint64_t, kTripCount> states{};
  std::array<std::mutex, kTripCount> locks;
  for (std::size_t index = 0; index < kTripCount; ++index) {
    trip_ids.push_back(make_trip_id(index));
  }

  std::atomic<std::uint64_t> total_wait{};
  std::atomic<std::uint64_t> maximum_wait{};
  std::array<std::uint64_t, kOperations> wait_samples{};
  std::barrier start_barrier{
      static_cast<std::ptrdiff_t>(kProducerCount + 1)};
  std::vector<std::thread> producers;
  producers.reserve(kProducerCount);
  for (std::size_t producer = 0; producer < kProducerCount; ++producer) {
    producers.emplace_back([&, producer] {
      std::uint64_t local_wait{};
      std::uint64_t local_maximum{};
      start_barrier.arrive_and_wait();
      for (std::size_t operation = producer; operation < kOperations;
           operation += kProducerCount) {
        const auto trip_index = operation % kTripCount;
        std::size_t lock_index{};
        switch (design) {
          case MutexDesign::kGlobal:
            lock_index = 0;
            break;
          case MutexDesign::kPerTrip:
            lock_index = trip_index;
            break;
          case MutexDesign::kSharded:
            lock_index = shard_for(trip_ids[trip_index], shard_count);
            break;
        }
        const auto wait_start = Clock::now();
        std::unique_lock lock(locks[lock_index]);
        const auto acquired = Clock::now();
        const auto waited =
            std::chrono::duration_cast<std::chrono::nanoseconds>(
                acquired - wait_start)
                .count();
        const auto waited_value = static_cast<std::uint64_t>(waited);
        wait_samples[operation] = waited_value;
        local_wait += waited_value;
        local_maximum = std::max(local_maximum, waited_value);
        mutate(states[trip_index], operation);
      }
      total_wait.fetch_add(local_wait, std::memory_order_relaxed);
      update_maximum(maximum_wait, local_maximum);
    });
  }

  const auto started = Clock::now();
  start_barrier.arrive_and_wait();
  for (auto& producer : producers) producer.join();
  const auto elapsed =
      std::chrono::duration_cast<std::chrono::nanoseconds>(
          Clock::now() - started)
          .count();

  std::string_view name;
  switch (design) {
    case MutexDesign::kGlobal:
      name = "global_mutex";
      break;
    case MutexDesign::kPerTrip:
      name = "per_trip_mutex";
      break;
    case MutexDesign::kSharded:
      name = "sharded_mutex";
      break;
  }
  return {.design = name,
          .shard_count = shard_count,
          .producer_count = kProducerCount,
          .operations = kOperations,
          .elapsed_nanoseconds = static_cast<std::uint64_t>(elapsed),
          .total_lock_wait_nanoseconds =
              total_wait.load(std::memory_order_relaxed),
          .p99_lock_wait_nanoseconds = p99(wait_samples),
          .maximum_lock_wait_nanoseconds =
              maximum_wait.load(std::memory_order_relaxed),
          .total_queue_wait_nanoseconds = 0,
          .p99_queue_wait_nanoseconds = 0,
          .maximum_queue_wait_nanoseconds = 0,
          .checksum = checksum(states),
          .valid = elapsed > 0};
}

[[nodiscard]] BenchmarkResult run_single_writer(
    std::size_t shard_count) {
  std::vector<TripId> trip_ids;
  trip_ids.reserve(kTripCount);
  std::array<std::uint64_t, kTripCount> states{};
  for (std::size_t index = 0; index < kTripCount; ++index) {
    trip_ids.push_back(make_trip_id(index));
  }

  std::atomic<std::size_t> completed{};
  std::atomic<std::uint64_t> total_queue_wait{};
  std::atomic<std::uint64_t> maximum_queue_wait{};
  std::array<std::uint64_t, kOperations> queue_wait_samples{};
  std::atomic<bool> submission_succeeded{true};
  std::mutex completion_mutex;
  std::condition_variable completion_condition;
  ShardedExecutor executor(shard_count, kOperations);
  std::barrier start_barrier{
      static_cast<std::ptrdiff_t>(kProducerCount + 1)};
  std::vector<std::thread> producers;
  producers.reserve(kProducerCount);
  for (std::size_t producer = 0; producer < kProducerCount; ++producer) {
    producers.emplace_back([&, producer] {
      start_barrier.arrive_and_wait();
      for (std::size_t operation = producer; operation < kOperations;
           operation += kProducerCount) {
        const auto trip_index = operation % kTripCount;
        const auto submitted_at = Clock::now();
        if (!executor.try_submit(
                trip_ids[trip_index],
                [&, operation, trip_index,
                 submitted_at](std::stop_token) {
                  const auto started_at = Clock::now();
                  const auto waited =
                      std::chrono::duration_cast<std::chrono::nanoseconds>(
                          started_at - submitted_at)
                          .count();
                  const auto waited_value =
                      static_cast<std::uint64_t>(waited);
                  queue_wait_samples[operation] = waited_value;
                  total_queue_wait.fetch_add(
                      waited_value, std::memory_order_relaxed);
                  update_maximum(maximum_queue_wait, waited_value);
                  mutate(states[trip_index], operation);
                  if (completed.fetch_add(
                          1, std::memory_order_acq_rel) +
                          1 ==
                      kOperations) {
                    completion_condition.notify_one();
                  }
                })) {
          submission_succeeded.store(false, std::memory_order_relaxed);
          break;
        }
      }
    });
  }

  const auto started = Clock::now();
  start_barrier.arrive_and_wait();
  for (auto& producer : producers) producer.join();
  {
    std::unique_lock lock(completion_mutex);
    (void)completion_condition.wait_for(
        lock, std::chrono::seconds{30}, [&] {
          return completed.load(std::memory_order_acquire) ==
                 kOperations;
        });
  }
  const auto elapsed =
      std::chrono::duration_cast<std::chrono::nanoseconds>(
          Clock::now() - started)
          .count();
  const auto completed_count =
      completed.load(std::memory_order_acquire);

  return {.design = "single_writer_queue",
          .shard_count = shard_count,
          .producer_count = kProducerCount,
          .operations = completed_count,
          .elapsed_nanoseconds = static_cast<std::uint64_t>(elapsed),
          .total_lock_wait_nanoseconds = 0,
          .p99_lock_wait_nanoseconds = 0,
          .maximum_lock_wait_nanoseconds = 0,
          .total_queue_wait_nanoseconds =
              total_queue_wait.load(std::memory_order_relaxed),
          .p99_queue_wait_nanoseconds =
              completed_count == kOperations ? p99(queue_wait_samples) : 0,
          .maximum_queue_wait_nanoseconds =
              maximum_queue_wait.load(std::memory_order_relaxed),
          .checksum = checksum(states),
          .valid =
              submission_succeeded.load(std::memory_order_relaxed) &&
              completed_count == kOperations && elapsed > 0};
}

void print(const BenchmarkResult& result) {
  const auto throughput =
      result.elapsed_nanoseconds == 0
          ? 0.0L
          : static_cast<long double>(result.operations) *
                1'000'000'000.0L /
                static_cast<long double>(result.elapsed_nanoseconds);
  const auto average_lock_wait =
      result.operations == 0
          ? 0
          : result.total_lock_wait_nanoseconds / result.operations;
  const auto average_queue_wait =
      result.operations == 0
          ? 0
          : result.total_queue_wait_nanoseconds / result.operations;
  std::cout << "design=" << result.design
            << " shards=" << result.shard_count
            << " producers=" << result.producer_count
            << " operations=" << result.operations
            << " elapsed_ns=" << result.elapsed_nanoseconds
            << " throughput_ops_per_second=" << throughput
            << " average_lock_wait_ns=" << average_lock_wait
            << " p99_lock_wait_ns=" << result.p99_lock_wait_nanoseconds
            << " maximum_lock_wait_ns="
            << result.maximum_lock_wait_nanoseconds
            << " average_queue_wait_ns=" << average_queue_wait
            << " p99_queue_wait_ns=" << result.p99_queue_wait_nanoseconds
            << " maximum_queue_wait_ns="
            << result.maximum_queue_wait_nanoseconds
            << " checksum=" << result.checksum << '\n';
}

}  // namespace

int main() {
  std::vector<BenchmarkResult> results;
  results.push_back(run_mutex_design(MutexDesign::kGlobal, 1));
  results.push_back(run_mutex_design(MutexDesign::kPerTrip, kTripCount));
  for (const auto shard_count : {1U, 2U, 4U, 8U}) {
    results.push_back(
        run_mutex_design(MutexDesign::kSharded, shard_count));
    results.push_back(run_single_writer(shard_count));
  }

  for (const auto& result : results) {
    if (!result.valid) return 1;
    print(result);
  }
  return 0;
}
