#include "liveroute/domain/types.hpp"
#include "liveroute/runtime/sharded_executor.hpp"

#include <array>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <memory>
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

  std::mutex priority_mutex;
  std::condition_variable priority_condition;
  bool priority_blocker_started = false;
  bool release_priority_blocker = false;
  std::vector<int> priority_observed;
  {
    ShardedExecutor executor(1, {1, 1, 2, 1}, 1);
    if (!executor.try_submit(
            trip_id, liveroute::domain::EventPriority::kNormal,
            [&priority_mutex, &priority_condition,
             &priority_blocker_started,
             &release_priority_blocker](std::stop_token) {
              std::unique_lock lock(priority_mutex);
              priority_blocker_started = true;
              priority_condition.notify_one();
              priority_condition.wait(lock, [&release_priority_blocker] {
                return release_priority_blocker;
              });
            })) {
      return 1;
    }
    std::unique_lock priority_lock(priority_mutex);
    if (!priority_condition.wait_for(
            priority_lock, std::chrono::seconds{1},
            [&priority_blocker_started] { return priority_blocker_started; })) {
      return 1;
    }
    priority_lock.unlock();

    if (!executor.try_submit(
            trip_id, liveroute::domain::EventPriority::kNormal,
            [&priority_mutex, &priority_condition, &priority_observed](
                std::stop_token) {
              std::scoped_lock lock(priority_mutex);
              priority_observed.push_back(1);
              priority_condition.notify_one();
            }) ||
        !executor.try_submit(
            trip_id, liveroute::domain::EventPriority::kHigh,
            [&priority_mutex, &priority_condition, &priority_observed](
                std::stop_token) {
              std::scoped_lock lock(priority_mutex);
              priority_observed.push_back(2);
              priority_condition.notify_one();
            }) ||
        !executor.try_submit(
            trip_id, liveroute::domain::EventPriority::kNormal,
            [&priority_mutex, &priority_condition, &priority_observed](
                std::stop_token) {
              std::scoped_lock lock(priority_mutex);
              priority_observed.push_back(3);
              priority_condition.notify_one();
            }) ||
        executor.queue_size(trip_id, liveroute::domain::EventPriority::kNormal) !=
            2 ||
        executor.queue_size(liveroute::domain::EventPriority::kNormal) != 2) {
      return 1;
    }

    priority_lock.lock();
    release_priority_blocker = true;
    priority_condition.notify_one();
    if (!priority_condition.wait_for(
            priority_lock, std::chrono::seconds{1},
            [&priority_observed] { return priority_observed.size() == 3; })) {
      return 1;
    }
  }

  if (priority_observed != std::vector<int>{2, 1, 3}) {
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

  std::mutex completion_mutex;
  std::condition_variable completion_condition;
  bool completion_blocker_started = false;
  bool release_completion_blocker = false;
  bool reserved_completion_ran = false;
  {
    ShardedExecutor executor(1, {1, 1, 1, 1}, 1, 1);
    if (!executor.try_submit(
            trip_id,
            [&completion_mutex, &completion_condition,
             &completion_blocker_started,
             &release_completion_blocker](std::stop_token) {
              std::unique_lock lock(completion_mutex);
              completion_blocker_started = true;
              completion_condition.notify_one();
              completion_condition.wait(lock, [&release_completion_blocker] {
                return release_completion_blocker;
              });
            })) {
      return 1;
    }
    std::unique_lock lock(completion_mutex);
    if (!completion_condition.wait_for(
            lock, std::chrono::seconds{1},
            [&completion_blocker_started] {
              return completion_blocker_started;
            })) {
      return 1;
    }
    lock.unlock();

    auto reservation = executor.try_reserve_completion(trip_id);
    if (!reservation.has_value() ||
        executor.try_reserve_completion(trip_id).has_value() ||
        !executor.submit_completion(
            std::move(*reservation),
            [&completion_mutex, &completion_condition,
             &reserved_completion_ran](std::stop_token) {
              std::scoped_lock task_lock(completion_mutex);
              reserved_completion_ran = true;
              completion_condition.notify_one();
            })) {
      return 1;
    }

    lock.lock();
    release_completion_blocker = true;
    completion_condition.notify_one();
    if (!completion_condition.wait_for(
            lock, std::chrono::seconds{1},
            [&reserved_completion_ran] { return reserved_completion_ran; })) {
      return 1;
    }
  }

  std::mutex shutdown_mutex;
  std::condition_variable_any shutdown_condition;
  bool shutdown_blocker_started = false;
  std::atomic<bool> shutdown_stop_observed{false};
  std::atomic<bool> queued_task_drained{false};
  {
    auto executor = std::make_unique<ShardedExecutor>(1, 2);
    if (!executor->try_submit(
            trip_id,
            [&shutdown_mutex, &shutdown_condition,
             &shutdown_blocker_started,
             &shutdown_stop_observed](std::stop_token stop_token) {
              std::unique_lock lock(shutdown_mutex);
              shutdown_blocker_started = true;
              shutdown_condition.notify_one();
              (void)shutdown_condition.wait(
                  lock, stop_token, [] { return false; });
              shutdown_stop_observed.store(
                  stop_token.stop_requested(), std::memory_order_release);
            })) {
      return 1;
    }
    {
      std::unique_lock lock(shutdown_mutex);
      if (!shutdown_condition.wait_for(
              lock, std::chrono::seconds{1},
              [&shutdown_blocker_started] {
                return shutdown_blocker_started;
              })) {
        return 1;
      }
    }
    if (!executor->try_submit(
            trip_id, [&queued_task_drained](std::stop_token stop_token) {
              if (stop_token.stop_requested()) {
                queued_task_drained.store(true, std::memory_order_release);
              }
            })) {
      return 1;
    }
    executor->stop_accepting();
    if (executor->is_accepting() ||
        executor->try_submit(trip_id, [](std::stop_token) {})) {
      return 1;
    }
    executor.reset();
  }
  if (!shutdown_stop_observed.load(std::memory_order_acquire) ||
      !queued_task_drained.load(std::memory_order_acquire)) {
    return 1;
  }

  return 0;
}
