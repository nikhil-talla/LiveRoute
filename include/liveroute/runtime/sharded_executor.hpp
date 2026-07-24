#pragma once

#include "liveroute/domain/types.hpp"
#include "liveroute/runtime/bounded_queue.hpp"

#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <stop_token>
#include <thread>
#include <utility>
#include <vector>

namespace liveroute::runtime {

class ShardedExecutor {
 public:
  using Task = std::function<void(std::stop_token)>;

  ShardedExecutor(std::size_t shard_count, std::size_t queue_capacity_per_shard)
      : shards_() {
    if (shard_count == 0) {
      throw std::invalid_argument("sharded executor requires at least one shard");
    }

    shards_.reserve(shard_count);
    for (std::size_t index = 0; index < shard_count; ++index) {
      shards_.push_back(
          std::make_unique<Shard>(queue_capacity_per_shard));
    }
    for (auto& shard : shards_) {
      shard->worker = std::jthread([raw_shard = shard.get()](std::stop_token stop_token) {
        run_shard(*raw_shard, stop_token);
      });
    }
  }

  ShardedExecutor(const ShardedExecutor&) = delete;
  ShardedExecutor& operator=(const ShardedExecutor&) = delete;

  ~ShardedExecutor() {
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
    auto& shard = *shards_[shard_for(trip_id)];
    if (!shard.queue.try_push(std::move(task))) {
      return false;
    }
    shard.work_available.notify_one();
    return true;
  }

 private:
  struct Shard {
    explicit Shard(std::size_t queue_capacity) : queue(queue_capacity) {}

    BoundedQueue<Task> queue;
    std::mutex wait_mutex;
    std::condition_variable_any work_available;
    std::jthread worker;
  };

  static void run_shard(Shard& shard, std::stop_token stop_token) {
    while (!stop_token.stop_requested()) {
      if (auto task = shard.queue.try_pop(); task.has_value()) {
        (*task)(stop_token);
        continue;
      }

      std::unique_lock lock(shard.wait_mutex);
      shard.work_available.wait(lock, stop_token, [&shard] {
        return shard.queue.size() != 0;
      });
    }
  }

  std::vector<std::unique_ptr<Shard>> shards_;
};

}  // namespace liveroute::runtime
