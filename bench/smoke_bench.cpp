#include <atomic>
#include <chrono>
#include <cstdint>
#include <iostream>

int main() {
  constexpr std::uint32_t kIterations = 100'000;
  std::atomic<std::uint64_t> accumulator = 0;

  const auto started = std::chrono::steady_clock::now();
  for (std::uint32_t iteration = 0; iteration < kIterations; ++iteration) {
    accumulator.fetch_add(iteration, std::memory_order_relaxed);
  }
  const auto elapsed = std::chrono::steady_clock::now() - started;

  std::cout << "iterations=" << kIterations
            << " elapsed_ns="
            << std::chrono::duration_cast<std::chrono::nanoseconds>(elapsed)
                   .count()
            << " accumulator=" << accumulator.load(std::memory_order_relaxed)
            << '\n';
  return 0;
}
