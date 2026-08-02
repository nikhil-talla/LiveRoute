#pragma once

#include <cstddef>
#include <cstdint>

namespace liveroute::benchmark {

struct PlannerAllocationSnapshot {
  std::uint64_t calls{};
  std::uint64_t bytes{};
  std::uint64_t scope_overflows{};
  bool valid{true};
};

// Benchmark-only attribution boundary. The implementation is linked only
// into benchmark/test executables; serving binaries do not replace global
// allocation functions.
class PlannerAllocationScope {
 public:
  PlannerAllocationScope() noexcept;
  ~PlannerAllocationScope();

  PlannerAllocationScope(const PlannerAllocationScope&) = delete;
  PlannerAllocationScope& operator=(const PlannerAllocationScope&) = delete;

  [[nodiscard]] PlannerAllocationSnapshot snapshot() const noexcept;

 private:
  void record_allocation(std::size_t bytes) noexcept;
  void record_scope_overflow() noexcept;

  std::uint64_t calls_{};
  std::uint64_t bytes_{};
  std::uint64_t scope_overflows_{};
  bool valid_{true};

  friend void* planner_allocate(std::size_t, std::size_t, bool);
};

}  // namespace liveroute::benchmark
