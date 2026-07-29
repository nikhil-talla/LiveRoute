#pragma once

#include "liveroute/domain/types.hpp"
#include "liveroute/runtime/priority_lanes.hpp"

#include <atomic>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <stdexcept>
#include <stop_token>
#include <thread>
#include <utility>
#include <vector>

namespace liveroute::runtime {

class ShardedExecutor {
 public:
  using Task = std::function<void(std::stop_token)>;

 private:
  struct QueuedTask {
    domain::TripId trip_id;
    Task task;
  };

 public:
  class CompletionReservation {
   public:
    CompletionReservation() = default;
    CompletionReservation(const CompletionReservation&) = delete;
    CompletionReservation& operator=(const CompletionReservation&) = delete;
    CompletionReservation(CompletionReservation&& other) noexcept
        : state_(std::move(other.state_)),
          active_(std::exchange(other.active_, false)) {}
    CompletionReservation& operator=(CompletionReservation&& other) noexcept {
      if (this != &other) {
        release();
        state_ = std::move(other.state_);
        active_ = std::exchange(other.active_, false);
      }
      return *this;
    }
    ~CompletionReservation() { release(); }

    [[nodiscard]] explicit operator bool() const noexcept {
      return active_ && state_ != nullptr;
    }

   private:
    struct State {
      explicit State(std::size_t capacity_value)
          : capacity(capacity_value) {}

      std::mutex mutex;
      std::deque<Task> tasks;
      std::size_t reserved{};
      std::size_t capacity;
    };

    explicit CompletionReservation(std::shared_ptr<State> state)
        : state_(std::move(state)), active_(true) {}

    void release() noexcept {
      if (!active_ || state_ == nullptr) return;
      std::scoped_lock lock(state_->mutex);
      if (state_->reserved != 0) --state_->reserved;
      active_ = false;
    }

    std::shared_ptr<State> state_;
    bool active_{};

    friend class ShardedExecutor;
  };

  ShardedExecutor(std::size_t shard_count, std::size_t queue_capacity_per_shard)
      : ShardedExecutor(
            shard_count,
            PriorityLaneCapacities{queue_capacity_per_shard,
                                   queue_capacity_per_shard,
                                   queue_capacity_per_shard,
                                   queue_capacity_per_shard},
            queue_capacity_per_shard, queue_capacity_per_shard) {}

  ShardedExecutor(std::size_t shard_count,
                  PriorityLaneCapacities capacities,
                  std::size_t max_preferred_pops_before_normal)
      : ShardedExecutor(shard_count, capacities,
                        max_preferred_pops_before_normal,
                        capacities.normal) {}

  ShardedExecutor(std::size_t shard_count,
                  PriorityLaneCapacities capacities,
                  std::size_t max_preferred_pops_before_normal,
                  std::size_t completion_capacity)
      : shards_() {
    if (shard_count == 0) {
      throw std::invalid_argument("sharded executor requires at least one shard");
    }
    if (!capacities.is_valid() || max_preferred_pops_before_normal == 0 ||
        completion_capacity == 0) {
      throw std::invalid_argument("invalid sharded priority configuration");
    }

    shards_.reserve(shard_count);
    for (std::size_t index = 0; index < shard_count; ++index) {
      shards_.push_back(std::make_unique<Shard>(
          capacities, max_preferred_pops_before_normal, completion_capacity));
    }
    for (auto& shard : shards_) {
      shard->worker =
          std::jthread([raw_shard = shard.get()](std::stop_token stop_token) {
            run_shard(*raw_shard, stop_token);
          });
    }
  }

  ShardedExecutor(const ShardedExecutor&) = delete;
  ShardedExecutor& operator=(const ShardedExecutor&) = delete;

  ~ShardedExecutor() {
    stop_accepting();
    for (auto& shard : shards_) {
      shard->worker.request_stop();
      shard->work_available.notify_all();
    }
  }

  [[nodiscard]] std::size_t shard_count() const noexcept { return shards_.size(); }

  [[nodiscard]] std::size_t shard_for(
      const domain::TripId& trip_id) const noexcept {
    std::uint64_t hash = 14695981039346656037ULL;
    for (const auto byte : trip_id.value()) {
      hash ^= std::to_integer<std::uint8_t>(byte);
      hash *= 1099511628211ULL;
    }
    return static_cast<std::size_t>(hash % shards_.size());
  }

  [[nodiscard]] bool try_submit(const domain::TripId& trip_id, Task task) {
    return try_submit(trip_id, domain::EventPriority::kNormal,
                      std::move(task));
  }

  [[nodiscard]] bool try_submit(const domain::TripId& trip_id,
                                domain::EventPriority priority, Task task) {
    if (!accepting_.load(std::memory_order_acquire)) return false;
    auto& shard = *shards_[shard_for(trip_id)];
    if (!shard.queue.try_push(
            priority, QueuedTask{trip_id, std::move(task)})) {
      return false;
    }
    shard.work_available.notify_one();
    return true;
  }

  void stop_accepting() noexcept {
    accepting_.store(false, std::memory_order_release);
  }

  [[nodiscard]] bool is_accepting() const noexcept {
    return accepting_.load(std::memory_order_acquire);
  }

  [[nodiscard]] std::size_t queue_size(
      const domain::TripId& trip_id) const {
    return shards_[shard_for(trip_id)]->queue.size();
  }

  [[nodiscard]] std::size_t queue_size(
      const domain::TripId& trip_id, domain::EventPriority priority) const {
    return shards_[shard_for(trip_id)]->queue.size(priority);
  }

  [[nodiscard]] std::size_t queue_size(
      domain::EventPriority priority) const {
    std::size_t total{};
    for (const auto& shard : shards_) {
      total += shard->queue.size(priority);
    }
    return total;
  }

  [[nodiscard]] std::size_t queue_size_for_trip(
      const domain::TripId& trip_id,
      domain::EventPriority priority) const {
    const auto& shard = *shards_[shard_for(trip_id)];
    return shard.queue.count_if(
        priority, [&trip_id](const QueuedTask& queued) {
          return queued.trip_id == trip_id;
        });
  }

  [[nodiscard]] std::optional<CompletionReservation>
  try_reserve_completion(const domain::TripId& trip_id) {
    auto state = shards_[shard_for(trip_id)]->completion;
    std::scoped_lock lock(state->mutex);
    if (state->tasks.size() + state->reserved >= state->capacity) {
      return std::nullopt;
    }
    ++state->reserved;
    return CompletionReservation{std::move(state)};
  }

  [[nodiscard]] bool submit_completion(CompletionReservation reservation,
                                       Task task) {
    if (!reservation || !task) return false;
    auto state = reservation.state_;
    {
      std::scoped_lock lock(state->mutex);
      if (state->reserved == 0 || state->tasks.size() >= state->capacity) {
        return false;
      }
      --state->reserved;
      reservation.active_ = false;
      state->tasks.push_back(std::move(task));
    }
    for (auto& shard : shards_) {
      if (shard->completion == state) {
        shard->work_available.notify_one();
        return true;
      }
    }
    return false;
  }

  [[nodiscard]] std::size_t completion_queue_size(
      const domain::TripId& trip_id) const {
    const auto state = shards_[shard_for(trip_id)]->completion;
    std::scoped_lock lock(state->mutex);
    return state->tasks.size();
  }

  [[nodiscard]] std::size_t completion_queue_size() const {
    std::size_t total{};
    for (const auto& shard : shards_) {
      std::scoped_lock lock(shard->completion->mutex);
      total += shard->completion->tasks.size();
    }
    return total;
  }

 private:
  struct Shard {
    Shard(PriorityLaneCapacities capacities,
          std::size_t max_preferred_pops_before_normal,
          std::size_t completion_capacity)
        : queue(capacities, max_preferred_pops_before_normal),
          completion(std::make_shared<CompletionReservation::State>(
              completion_capacity)) {}

    BoundedPriorityLanes<QueuedTask> queue;
    std::shared_ptr<CompletionReservation::State> completion;
    std::mutex wait_mutex;
    std::condition_variable_any work_available;
    std::jthread worker;
  };

  static void run_shard(Shard& shard, std::stop_token stop_token) {
    while (true) {
      std::optional<Task> completion;
      {
        std::scoped_lock lock(shard.completion->mutex);
        if (!shard.completion->tasks.empty()) {
          completion = std::move(shard.completion->tasks.front());
          shard.completion->tasks.pop_front();
        }
      }
      if (completion.has_value()) {
        (*completion)(stop_token);
        continue;
      }
      if (auto task = shard.queue.try_pop(); task.has_value()) {
        task->task(stop_token);
        continue;
      }
      // Stop requests are visible to every task, but work accepted before
      // shutdown is drained instead of disappearing from a durable lane.
      if (stop_token.stop_requested()) return;

      std::unique_lock lock(shard.wait_mutex);
      shard.work_available.wait(lock, stop_token, [&shard] {
        if (shard.queue.size() != 0) return true;
        std::scoped_lock completion_lock(shard.completion->mutex);
        return !shard.completion->tasks.empty();
      });
    }
  }

  std::atomic<bool> accepting_{true};
  std::vector<std::unique_ptr<Shard>> shards_;
};

}  // namespace liveroute::runtime
