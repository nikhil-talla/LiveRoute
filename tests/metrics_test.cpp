#include "liveroute/runtime/metrics.hpp"

#include <cstdlib>
#include <cstdint>
#include <thread>
#include <vector>

using liveroute::runtime::MetricCounter;
using liveroute::runtime::MetricGauge;
using liveroute::runtime::MetricHistogram;
using liveroute::runtime::MetricsRegistry;

int main() {
  const auto require = [](bool condition) {
    if (!condition) std::abort();
  };
  MetricsRegistry metrics;
  metrics.increment(MetricCounter::kAcceptedEvents, 2);
  metrics.increment(MetricCounter::kAcceptedEvents);
  metrics.observe_microseconds(MetricHistogram::kPlanner, 1);
  metrics.observe_microseconds(MetricHistogram::kPlanner, 10);
  metrics.observe_microseconds(MetricHistogram::kPlanner, 1000);
  metrics.observe_microseconds(MetricHistogram::kPlanner, 50000);
  metrics.set(MetricGauge::kNormalQueueDepth, 7);

  const auto snapshot = metrics.snapshot();
  require(snapshot.counter(MetricCounter::kAcceptedEvents) == 3);
  require(snapshot.gauge(MetricGauge::kNormalQueueDepth) == 7);
  const auto& planner = snapshot.histogram(MetricHistogram::kPlanner);
  require(planner.count == 4);
  require(planner.sum_microseconds == 51011);
  require(planner.max_microseconds == 50000);
  require(planner.percentile_microseconds(50) == 10);
  require(planner.percentile_microseconds(95) == 50000);
  require(planner.percentile_microseconds(99) == 50000);
  require(MetricsRegistry::counter_name(MetricCounter::kAcceptedEvents) ==
          "accepted_events");
  require(MetricsRegistry::histogram_name(MetricHistogram::kPlanner) ==
          "planner");

  MetricsRegistry concurrent;
  constexpr std::size_t kThreads = 4;
  constexpr std::size_t kIterations = 1000;
  std::vector<std::thread> workers;
  workers.reserve(kThreads);
  for (std::size_t thread = 0; thread < kThreads; ++thread) {
    workers.emplace_back([&concurrent] {
      for (std::size_t iteration = 0; iteration < kIterations; ++iteration) {
        concurrent.increment(MetricCounter::kAcceptedEvents);
        concurrent.observe_microseconds(MetricHistogram::kQueueWait, 25);
      }
    });
  }
  for (auto& worker : workers) worker.join();
  const auto concurrent_snapshot = concurrent.snapshot();
  require(concurrent_snapshot.counter(MetricCounter::kAcceptedEvents) ==
          kThreads * kIterations);
  require(concurrent_snapshot.histogram(MetricHistogram::kQueueWait).count ==
          kThreads * kIterations);
  return 0;
}
