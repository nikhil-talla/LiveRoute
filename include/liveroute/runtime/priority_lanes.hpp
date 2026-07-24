#pragma once

#include <array>
#include <cstddef>
#include <deque>
#include <mutex>
#include <optional>
#include <stdexcept>
#include <utility>

#include "liveroute/domain/types.hpp"

namespace liveroute::runtime {

struct PriorityLaneCapacities {
  std::size_t critical{};
  std::size_t high{};
  std::size_t normal{};
  std::size_t advisory{};

  [[nodiscard]] constexpr bool is_valid() const noexcept {
    return critical != 0 && high != 0 && normal != 0 && advisory != 0;
  }
};

template <typename T>
class BoundedPriorityLanes {
 public:
  BoundedPriorityLanes(PriorityLaneCapacities capacities,
                       std::size_t max_preferred_pops_before_normal)
      : capacities_(capacities),
        max_preferred_pops_before_normal_(max_preferred_pops_before_normal) {
    if (!capacities_.is_valid() || max_preferred_pops_before_normal_ == 0) {
      throw std::invalid_argument("invalid bounded priority lane configuration");
    }
  }

  BoundedPriorityLanes(const BoundedPriorityLanes&) = delete;
  BoundedPriorityLanes& operator=(const BoundedPriorityLanes&) = delete;

  [[nodiscard]] bool try_push(domain::EventPriority priority, T value) {
    std::scoped_lock lock(mutex_);
    auto& lane = lanes_[index_for(priority)];
    if (lane.size() == capacity_for(priority)) {
      return false;
    }
    lane.push_back(std::move(value));
    return true;
  }

  [[nodiscard]] std::optional<T> try_pop() {
    std::scoped_lock lock(mutex_);
    if (lanes_[index_for(domain::EventPriority::kNormal)].size() != 0 &&
        preferred_pops_ >= max_preferred_pops_before_normal_) {
      return pop(domain::EventPriority::kNormal);
    }

    for (const auto priority : {domain::EventPriority::kCritical,
                                domain::EventPriority::kHigh,
                                domain::EventPriority::kNormal,
                                domain::EventPriority::kAdvisory}) {
      if (!lanes_[index_for(priority)].empty()) {
        return pop(priority);
      }
    }
    return std::nullopt;
  }

  [[nodiscard]] std::size_t size() const {
    std::scoped_lock lock(mutex_);
    std::size_t total = 0;
    for (const auto& lane : lanes_) {
      total += lane.size();
    }
    return total;
  }

  [[nodiscard]] std::size_t size(domain::EventPriority priority) const {
    std::scoped_lock lock(mutex_);
    return lanes_[index_for(priority)].size();
  }

 private:
  [[nodiscard]] static constexpr std::size_t index_for(
      domain::EventPriority priority) noexcept {
    return static_cast<std::size_t>(priority);
  }

  [[nodiscard]] constexpr std::size_t capacity_for(
      domain::EventPriority priority) const noexcept {
    switch (priority) {
      case domain::EventPriority::kCritical:
        return capacities_.critical;
      case domain::EventPriority::kHigh:
        return capacities_.high;
      case domain::EventPriority::kNormal:
        return capacities_.normal;
      case domain::EventPriority::kAdvisory:
        return capacities_.advisory;
    }
    return 0;
  }

  [[nodiscard]] std::optional<T> pop(domain::EventPriority priority) {
    auto& lane = lanes_[index_for(priority)];
    T value = std::move(lane.front());
    lane.pop_front();
    if (priority == domain::EventPriority::kCritical ||
        priority == domain::EventPriority::kHigh) {
      if (preferred_pops_ < max_preferred_pops_before_normal_) {
        ++preferred_pops_;
      }
    } else {
      preferred_pops_ = 0;
    }
    return value;
  }

  const PriorityLaneCapacities capacities_;
  const std::size_t max_preferred_pops_before_normal_;
  mutable std::mutex mutex_;
  std::array<std::deque<T>, 4> lanes_;
  std::size_t preferred_pops_{};
};

}  // namespace liveroute::runtime
