#include "liveroute/runtime/metrics.hpp"

#include <algorithm>
#include <array>
#include <limits>
#include <stdexcept>

namespace liveroute::runtime {
namespace {

constexpr std::array<std::uint64_t, kMetricHistogramBucketCount>
    kHistogramUpperBoundsMicroseconds{
        1,       5,       10,      25,      50,      100,     250,
        500,     1000,    2500,    5000,    10000,   25000,   50000,
        100000,  250000,  500000, 1000000, std::numeric_limits<std::uint64_t>::max()};

template <typename Enum>
[[nodiscard]] constexpr std::size_t index(Enum value) noexcept {
  return static_cast<std::size_t>(value);
}

[[nodiscard]] std::size_t histogram_bucket(std::uint64_t value) noexcept {
  const auto found = std::lower_bound(kHistogramUpperBoundsMicroseconds.begin(),
                                      kHistogramUpperBoundsMicroseconds.end(),
                                      value);
  return static_cast<std::size_t>(found -
                                  kHistogramUpperBoundsMicroseconds.begin());
}

}  // namespace

std::uint64_t HistogramSnapshot::percentile_microseconds(
    std::uint32_t percentile) const noexcept {
  if (count == 0 || percentile == 0 || percentile > 100) return 0;
  const auto quotient = count / 100;
  const auto remainder = count % 100;
  const auto scaled = quotient * percentile +
                      (remainder * percentile + 99) / 100;
  const auto target = std::max<std::uint64_t>(1, scaled);
  std::uint64_t cumulative = 0;
  for (std::size_t bucket = 0; bucket < bucket_counts.size(); ++bucket) {
    cumulative += bucket_counts[bucket];
    if (cumulative >= target) {
      return kHistogramUpperBoundsMicroseconds[bucket];
    }
  }
  return kHistogramUpperBoundsMicroseconds.back();
}

std::uint64_t MetricsSnapshot::counter(MetricCounter metric) const noexcept {
  return counters[index(metric)];
}

const HistogramSnapshot& MetricsSnapshot::histogram(
    MetricHistogram metric) const noexcept {
  return histograms[index(metric)];
}

std::uint64_t MetricsSnapshot::gauge(MetricGauge metric) const noexcept {
  return gauges[index(metric)];
}

void MetricsRegistry::increment(MetricCounter metric,
                                std::uint64_t amount) noexcept {
  counters_[index(metric)].fetch_add(amount, std::memory_order_relaxed);
}

void MetricsRegistry::observe_microseconds(MetricHistogram metric,
                                           std::uint64_t value) noexcept {
  auto& histogram = histograms_[index(metric)];
  histogram.count.fetch_add(1, std::memory_order_relaxed);
  histogram.sum_microseconds.fetch_add(value, std::memory_order_relaxed);
  auto current = histogram.max_microseconds.load(std::memory_order_relaxed);
  while (current < value &&
         !histogram.max_microseconds.compare_exchange_weak(
             current, value, std::memory_order_relaxed,
             std::memory_order_relaxed)) {
  }
  histogram.bucket_counts[histogram_bucket(value)].fetch_add(
      1, std::memory_order_relaxed);
}

void MetricsRegistry::set(MetricGauge metric, std::uint64_t value) noexcept {
  gauges_[index(metric)].store(value, std::memory_order_relaxed);
}

MetricsSnapshot MetricsRegistry::snapshot() const noexcept {
  MetricsSnapshot result;
  for (std::size_t metric = 0; metric < counters_.size(); ++metric) {
    result.counters[metric] =
        counters_[metric].load(std::memory_order_relaxed);
  }
  for (std::size_t metric = 0; metric < histograms_.size(); ++metric) {
    const auto& source = histograms_[metric];
    auto& target = result.histograms[metric];
    target.count = source.count.load(std::memory_order_relaxed);
    target.sum_microseconds =
        source.sum_microseconds.load(std::memory_order_relaxed);
    target.max_microseconds =
        source.max_microseconds.load(std::memory_order_relaxed);
    for (std::size_t bucket = 0; bucket < target.bucket_counts.size();
         ++bucket) {
      target.bucket_counts[bucket] =
          source.bucket_counts[bucket].load(std::memory_order_relaxed);
    }
  }
  for (std::size_t metric = 0; metric < gauges_.size(); ++metric) {
    result.gauges[metric] = gauges_[metric].load(std::memory_order_relaxed);
  }
  return result;
}

std::string_view MetricsRegistry::counter_name(MetricCounter metric) noexcept {
  switch (metric) {
    case MetricCounter::kAcceptedEvents: return "accepted_events";
    case MetricCounter::kDuplicateEvents: return "duplicate_events";
    case MetricCounter::kStaleEvents: return "stale_events";
    case MetricCounter::kDroppedCriticalEvents: return "dropped_critical_events";
    case MetricCounter::kDroppedHighEvents: return "dropped_high_events";
    case MetricCounter::kDroppedNormalEvents: return "dropped_normal_events";
    case MetricCounter::kDroppedAdvisoryEvents: return "dropped_advisory_events";
    case MetricCounter::kCoalescedUpdates: return "coalesced_updates";
    case MetricCounter::kReplanTriggers: return "replan_triggers";
    case MetricCounter::kReplanCancellations: return "replan_cancellations";
    case MetricCounter::kRejectedOverloadRequests: return "rejected_overload_requests";
    case MetricCounter::kDeadlineMisses: return "deadline_misses";
    case MetricCounter::kOsrmFailures: return "osrm_failures";
    case MetricCounter::kHoursProviderFailures: return "hours_provider_failures";
    case MetricCounter::kInfeasibleReplans: return "infeasible_replans";
    case MetricCounter::kCount: break;
  }
  return "unknown";
}

std::string_view MetricsRegistry::histogram_name(
    MetricHistogram metric) noexcept {
  switch (metric) {
    case MetricHistogram::kDeserialization: return "deserialization";
    case MetricHistogram::kQueueWait: return "queue_wait";
    case MetricHistogram::kEventApplication: return "event_application";
    case MetricHistogram::kOsrmRequest: return "osrm_request";
    case MetricHistogram::kHoursProviderRequest:
      return "hours_provider_request";
    case MetricHistogram::kMatrixConversion: return "matrix_conversion";
    case MetricHistogram::kPlanner: return "planner";
    case MetricHistogram::kSerialization: return "serialization";
    case MetricHistogram::kTotalRequest: return "total_request";
    case MetricHistogram::kCount: break;
  }
  return "unknown";
}

std::string_view MetricsRegistry::gauge_name(MetricGauge metric) noexcept {
  switch (metric) {
    case MetricGauge::kCriticalQueueDepth: return "critical_queue_depth";
    case MetricGauge::kHighQueueDepth: return "high_queue_depth";
    case MetricGauge::kNormalQueueDepth: return "normal_queue_depth";
    case MetricGauge::kAdvisoryQueueDepth: return "advisory_queue_depth";
    case MetricGauge::kCount: break;
  }
  return "unknown";
}

}  // namespace liveroute::runtime
