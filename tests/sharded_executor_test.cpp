#include "liveroute/domain/types.hpp"
#include "liveroute/runtime/sharded_executor.hpp"

#include <array>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <mutex>
#include <stdexcept>
#include <vector>

namespace {

using liveroute::domain::TripId;
using liveroute::runtime::ShardedExecutor;

TripId trip_id_with_first_byte(std::byte first_byte) {
  std::array<std::byte, 16> bytes{};
  bytes.front() = first_byte;
  return TripId{bytes};
}

bool rejects_zero_shards() {
  try {
    [[maybe_unused]] ShardedExecutor executor(0, 1);
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

}  // namespace

int main() {
  const auto trip_id = trip_id_with_first_byte(std::byte{1});
  std::mutex mutex;
  std::condition_variable completed;
  std::vector<int> observed;

  {
    ShardedExecutor executor(2, 4);
    if (executor.shard_count() != 2 ||
        executor.shard_for(trip_id) != executor.shard_for(trip_id) ||
        !executor.try_submit(trip_id, [&mutex, &completed, &observed](std::stop_token) {
          std::scoped_lock lock(mutex);
          observed.push_back(1);
          completed.notify_one();
        }) ||
        !executor.try_submit(trip_id, [&mutex, &completed, &observed](std::stop_token) {
          std::scoped_lock lock(mutex);
          observed.push_back(2);
          completed.notify_one();
        }) ||
        !executor.try_submit(trip_id, [&mutex, &completed, &observed](std::stop_token) {
          std::scoped_lock lock(mutex);
          observed.push_back(3);
          completed.notify_one();
        })) {
      return 1;
    }

    std::unique_lock lock(mutex);
    if (!completed.wait_for(lock, std::chrono::seconds{1}, [&observed] {
          return observed.size() == 3;
        })) {
      return 1;
    }
  }

  if (observed != std::vector<int>{1, 2, 3} || !rejects_zero_shards()) {
    return 1;
  }

  std::mutex overload_mutex;
  std::condition_variable overload_condition;
  bool first_task_started = false;
  bool release_first_task = false;
  bool second_task_completed = false;

  {
    ShardedExecutor executor(1, 1);
    if (!executor.try_submit(
            trip_id, [&overload_mutex, &overload_condition, &first_task_started,
                      &release_first_task](std::stop_token) {
              std::unique_lock lock(overload_mutex);
              first_task_started = true;
              overload_condition.notify_one();
              overload_condition.wait(lock, [&release_first_task] {
                return release_first_task;
              });
            })) {
      return 1;
    }

    std::unique_lock lock(overload_mutex);
    if (!overload_condition.wait_for(lock, std::chrono::seconds{1},
                                     [&first_task_started] {
                                       return first_task_started;
                                     })) {
      return 1;
    }
    lock.unlock();

    if (!executor.try_submit(trip_id, [&overload_mutex, &overload_condition,
                                       &second_task_completed](std::stop_token) {
          std::scoped_lock task_lock(overload_mutex);
          second_task_completed = true;
          overload_condition.notify_one();
        }) ||
        executor.try_submit(trip_id, [](std::stop_token) {})) {
      return 1;
    }

    lock.lock();
    release_first_task = true;
    overload_condition.notify_one();
    if (!overload_condition.wait_for(lock, std::chrono::seconds{1},
                                     [&second_task_completed] {
                                       return second_task_completed;
                                     })) {
      return 1;
    }
  }

  return 0;
}
