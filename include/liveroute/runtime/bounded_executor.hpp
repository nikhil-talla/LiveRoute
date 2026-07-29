#pragma once

#include <atomic>
#include <condition_variable>
#include <cstddef>
#include <functional>
#include <mutex>
#include <stdexcept>
#include <stop_token>
#include <thread>
#include <utility>
#include <vector>

#include "liveroute/runtime/bounded_queue.hpp"

namespace liveroute::runtime {

class BoundedExecutor {
 public:
  using Task = std::function<void(std::stop_token)>;

  BoundedExecutor(std::size_t worker_count, std::size_t queue_capacity)
      : queue_(queue_capacity) {
    if (worker_count == 0) {
      throw std::invalid_argument("bounded executor requires at least one worker");
    }
    workers_.reserve(worker_count);
    for (std::size_t index = 0; index < worker_count; ++index) {
      workers_.emplace_back([this](std::stop_token stop_token) {
        run(stop_token);
      });
    }
  }

  BoundedExecutor(const BoundedExecutor&) = delete;
  BoundedExecutor& operator=(const BoundedExecutor&) = delete;

  ~BoundedExecutor() {
    stop_accepting();
    for (auto& worker : workers_) worker.request_stop();
    work_available_.notify_all();
  }

  [[nodiscard]] bool try_submit(Task task) {
    if (stopping_.load(std::memory_order_acquire) || !queue_.try_push(std::move(task))) {
      return false;
    }
    work_available_.notify_one();
    return true;
  }

  void stop_accepting() noexcept {
    stopping_.store(true, std::memory_order_release);
  }

  [[nodiscard]] std::size_t queue_size() const { return queue_.size(); }
  [[nodiscard]] std::size_t queue_capacity() const noexcept { return queue_.capacity(); }
  [[nodiscard]] std::size_t worker_count() const noexcept { return workers_.size(); }

 private:
  void run(std::stop_token stop_token) {
    while (!stop_token.stop_requested()) {
      if (auto task = queue_.try_pop(); task.has_value()) {
        (*task)(stop_token);
        continue;
      }
      std::unique_lock lock(wait_mutex_);
      work_available_.wait(lock, stop_token, [this] {
        return queue_.size() != 0;
      });
    }
  }

  BoundedQueue<Task> queue_;
  std::mutex wait_mutex_;
  std::condition_variable_any work_available_;
  std::atomic<bool> stopping_{false};
  std::vector<std::jthread> workers_;
};

// The two pools are intentionally distinct so provider waits cannot consume
// the fixed CPU capacity reserved for planner work.
struct ExecutorConfiguration {
  std::size_t provider_workers{};
  std::size_t provider_queue_capacity{};
  std::size_t planner_workers{};
  std::size_t planner_queue_capacity{};
};

class ProviderAndPlannerExecutors {
 public:
  explicit ProviderAndPlannerExecutors(ExecutorConfiguration configuration)
      : provider_(configuration.provider_workers, configuration.provider_queue_capacity),
        planner_(configuration.planner_workers, configuration.planner_queue_capacity) {}

  [[nodiscard]] BoundedExecutor& provider() noexcept { return provider_; }
  [[nodiscard]] BoundedExecutor& planner() noexcept { return planner_; }
  [[nodiscard]] const BoundedExecutor& provider() const noexcept {
    return provider_;
  }
  [[nodiscard]] const BoundedExecutor& planner() const noexcept {
    return planner_;
  }

 private:
  BoundedExecutor provider_;
  BoundedExecutor planner_;
};

}  // namespace liveroute::runtime
