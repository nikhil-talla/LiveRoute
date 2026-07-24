#pragma once

#include <cstddef>
#include <deque>
#include <mutex>
#include <optional>
#include <stdexcept>
#include <utility>

namespace liveroute::runtime {

template <typename T>
class BoundedQueue {
 public:
  explicit BoundedQueue(std::size_t capacity) : capacity_(capacity) {
    if (capacity_ == 0) {
      throw std::invalid_argument("bounded queue capacity must be positive");
    }
  }

  BoundedQueue(const BoundedQueue&) = delete;
  BoundedQueue& operator=(const BoundedQueue&) = delete;

  [[nodiscard]] bool try_push(T value) {
    std::scoped_lock lock(mutex_);
    if (values_.size() == capacity_) {
      return false;
    }
    values_.push_back(std::move(value));
    return true;
  }

  [[nodiscard]] std::optional<T> try_pop() {
    std::scoped_lock lock(mutex_);
    if (values_.empty()) {
      return std::nullopt;
    }

    T value = std::move(values_.front());
    values_.pop_front();
    return value;
  }

  [[nodiscard]] std::size_t size() const {
    std::scoped_lock lock(mutex_);
    return values_.size();
  }

  [[nodiscard]] std::size_t capacity() const noexcept { return capacity_; }

 private:
  const std::size_t capacity_;
  mutable std::mutex mutex_;
  std::deque<T> values_;
};

}  // namespace liveroute::runtime
