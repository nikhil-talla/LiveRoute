#include "liveroute/benchmark/planner_allocation.hpp"

#include <cstddef>
#include <new>
#include <thread>

using liveroute::benchmark::PlannerAllocationScope;

int main() {
  PlannerAllocationScope scope;
  auto* scalar = ::operator new(17);
  auto* array = ::operator new[](19);
  auto* nothrow_scalar = ::operator new(23, std::nothrow);
  auto* aligned = ::operator new(31, std::align_val_t{64});
  auto* aligned_array = ::operator new[](37, std::align_val_t{64},
                                         std::nothrow);
  ::operator delete(scalar);
  ::operator delete[](array);
  ::operator delete(nothrow_scalar, std::nothrow);
  ::operator delete(aligned, std::align_val_t{64});
  ::operator delete[](aligned_array, std::align_val_t{64});

  std::thread unrelated([] {
    auto* value = ::operator new(41);
    ::operator delete(value);
  });
  unrelated.join();
  const auto before_nested = scope.snapshot();
  if (!before_nested.valid || before_nested.calls < 5 ||
      before_nested.bytes < 127) {
    return 1;
  }

  {
    PlannerAllocationScope nested;
    if (nested.snapshot().valid) return 1;
  }
  const auto after_nested = scope.snapshot();
  if (after_nested.valid || after_nested.scope_overflows != 1) return 1;
  return 0;
}
