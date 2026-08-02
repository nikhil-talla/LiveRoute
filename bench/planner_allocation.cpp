#include "liveroute/benchmark/planner_allocation.hpp"

#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <limits>
#include <new>

namespace {

thread_local liveroute::benchmark::PlannerAllocationScope* active_scope{};

[[nodiscard]] void* allocate_storage(std::size_t bytes,
                                     std::size_t alignment) {
  const auto requested = bytes == 0 ? std::size_t{1} : bytes;
  void* result{};
  if (alignment <= alignof(std::max_align_t)) {
    result = std::malloc(requested);
  } else {
    const auto remainder = requested % alignment;
    if (remainder > 0 && requested >
                              std::numeric_limits<std::size_t>::max() -
                                  (alignment - remainder)) {
      throw std::bad_alloc{};
    }
    const auto aligned_size =
        requested + (remainder == 0 ? 0 : alignment - remainder);
    result = std::aligned_alloc(alignment, aligned_size);
  }
  if (result == nullptr) throw std::bad_alloc{};
  return result;
}

}  // namespace

namespace liveroute::benchmark {

PlannerAllocationScope::PlannerAllocationScope() noexcept {
  if (active_scope != nullptr) {
    active_scope->record_scope_overflow();
    valid_ = false;
    return;
  }
  active_scope = this;
}

PlannerAllocationScope::~PlannerAllocationScope() {
  if (active_scope == this) active_scope = nullptr;
}

PlannerAllocationSnapshot PlannerAllocationScope::snapshot() const noexcept {
  return {.calls = calls_,
          .bytes = bytes_,
          .scope_overflows = scope_overflows_,
          .valid = valid_ && scope_overflows_ == 0};
}

void PlannerAllocationScope::record_allocation(std::size_t bytes) noexcept {
  if (calls_ == std::numeric_limits<std::uint64_t>::max() ||
      bytes_ > std::numeric_limits<std::uint64_t>::max() - bytes) {
    record_scope_overflow();
    return;
  }
  ++calls_;
  bytes_ += bytes;
}

void PlannerAllocationScope::record_scope_overflow() noexcept {
  valid_ = false;
  if (scope_overflows_ != std::numeric_limits<std::uint64_t>::max()) {
    ++scope_overflows_;
  }
}

}  // namespace liveroute::benchmark

namespace liveroute::benchmark {

void* planner_allocate(std::size_t bytes, std::size_t alignment,
                       bool attribute) {
  void* result = allocate_storage(bytes, alignment);
  if (attribute && active_scope != nullptr) {
    active_scope->record_allocation(bytes);
  }
  return result;
}

}  // namespace liveroute::benchmark

void* operator new(std::size_t bytes) {
  return liveroute::benchmark::planner_allocate(
      bytes, alignof(std::max_align_t), true);
}

void* operator new[](std::size_t bytes) {
  return liveroute::benchmark::planner_allocate(
      bytes, alignof(std::max_align_t), true);
}

void* operator new(std::size_t bytes, const std::nothrow_t&) noexcept {
  try {
    return liveroute::benchmark::planner_allocate(
        bytes, alignof(std::max_align_t), true);
  } catch (...) {
    return nullptr;
  }
}

void* operator new[](std::size_t bytes, const std::nothrow_t&) noexcept {
  try {
    return liveroute::benchmark::planner_allocate(
        bytes, alignof(std::max_align_t), true);
  } catch (...) {
    return nullptr;
  }
}

void* operator new(std::size_t bytes, std::align_val_t alignment) {
  return liveroute::benchmark::planner_allocate(
      bytes, static_cast<std::size_t>(alignment), true);
}

void* operator new[](std::size_t bytes, std::align_val_t alignment) {
  return liveroute::benchmark::planner_allocate(
      bytes, static_cast<std::size_t>(alignment), true);
}

void* operator new(std::size_t bytes, std::align_val_t alignment,
                   const std::nothrow_t&) noexcept {
  try {
    return liveroute::benchmark::planner_allocate(
        bytes, static_cast<std::size_t>(alignment), true);
  } catch (...) {
    return nullptr;
  }
}

void* operator new[](std::size_t bytes, std::align_val_t alignment,
                     const std::nothrow_t&) noexcept {
  try {
    return liveroute::benchmark::planner_allocate(
        bytes, static_cast<std::size_t>(alignment), true);
  } catch (...) {
    return nullptr;
  }
}

void operator delete(void* pointer) noexcept { std::free(pointer); }
void operator delete[](void* pointer) noexcept { std::free(pointer); }
void operator delete(void* pointer, std::size_t) noexcept { std::free(pointer); }
void operator delete[](void* pointer, std::size_t) noexcept {
  std::free(pointer);
}
void operator delete(void* pointer, const std::nothrow_t&) noexcept {
  std::free(pointer);
}
void operator delete[](void* pointer, const std::nothrow_t&) noexcept {
  std::free(pointer);
}
void operator delete(void* pointer, std::align_val_t) noexcept {
  std::free(pointer);
}
void operator delete[](void* pointer, std::align_val_t) noexcept {
  std::free(pointer);
}
void operator delete(void* pointer, std::size_t,
                     std::align_val_t) noexcept {
  std::free(pointer);
}
void operator delete[](void* pointer, std::size_t,
                       std::align_val_t) noexcept {
  std::free(pointer);
}
