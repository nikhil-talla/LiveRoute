#include "liveroute/runtime/bounded_executor.hpp"

#include <chrono>
#include <condition_variable>
#include <mutex>
#include <stdexcept>
#include <vector>

namespace {

using liveroute::runtime::BoundedExecutor;
using liveroute::runtime::ExecutorConfiguration;
using liveroute::runtime::ProviderAndPlannerExecutors;

bool rejects_zero_workers() {
  try {
    [[maybe_unused]] BoundedExecutor executor(0, 1);
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

}  // namespace

int main() {
  std::mutex mutex;
  std::condition_variable completed;
  std::vector<int> observed;
  bool first_started = false;
  bool release_first = false;

  {
    BoundedExecutor executor(1, 1);
    if (executor.worker_count() != 1 || executor.queue_capacity() != 1 ||
        !executor.try_submit([&](std::stop_token) {
          std::unique_lock lock(mutex);
          first_started = true;
          completed.notify_one();
          completed.wait(lock, [&] { return release_first; });
          observed.push_back(1);
          completed.notify_one();
        })) {
      return 1;
    }

    std::unique_lock lock(mutex);
    if (!completed.wait_for(lock, std::chrono::seconds{1}, [&] { return first_started; })) {
      return 1;
    }
    lock.unlock();
    if (!executor.try_submit([&](std::stop_token) {
          std::scoped_lock task_lock(mutex);
          observed.push_back(2);
          completed.notify_one();
        }) ||
        executor.try_submit([](std::stop_token) {})) {
      return 1;
    }

    lock.lock();
    release_first = true;
    completed.notify_one();
    if (!completed.wait_for(lock, std::chrono::seconds{1}, [&] {
          return observed.size() == 2;
        })) {
      return 1;
    }
    lock.unlock();

    executor.stop_accepting();
    if (executor.try_submit([](std::stop_token) {})) return 1;
  }

  if (observed != std::vector<int>{1, 2} || !rejects_zero_workers()) return 1;

  ProviderAndPlannerExecutors pools({
      .provider_workers = 1,
      .provider_queue_capacity = 1,
      .planner_workers = 2,
      .planner_queue_capacity = 3,
  });
  if (&pools.provider() == &pools.planner() ||
      pools.provider().worker_count() != 1 ||
      pools.planner().worker_count() != 2 ||
      pools.provider().queue_capacity() != 1 ||
      pools.planner().queue_capacity() != 3) {
    return 1;
  }

  bool provider_started = false;
  bool release_provider = false;
  bool planner_completed = false;
  if (!pools.provider().try_submit([&](std::stop_token) {
        std::unique_lock lock(mutex);
        provider_started = true;
        completed.notify_one();
        completed.wait(lock, [&] { return release_provider; });
      })) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!completed.wait_for(lock, std::chrono::seconds{1}, [&] { return provider_started; })) {
      return 1;
    }
  }
  if (!pools.planner().try_submit([&](std::stop_token) {
        std::scoped_lock lock(mutex);
        planner_completed = true;
        completed.notify_one();
      })) {
    return 1;
  }
  {
    std::unique_lock lock(mutex);
    if (!completed.wait_for(lock, std::chrono::seconds{1}, [&] { return planner_completed; })) {
      return 1;
    }
    release_provider = true;
  }
  completed.notify_one();
  return 0;
}
