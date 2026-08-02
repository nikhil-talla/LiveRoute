#pragma once

#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <string_view>

namespace liveroute::runtime {

enum class MetricCounter : std::uint8_t {
  kAcceptedEvents,
  kDuplicateEvents,
  kStaleEvents,
  kDroppedCriticalEvents,
  kDroppedHighEvents,
  kDroppedNormalEvents,
  kDroppedAdvisoryEvents,
  kCoalescedUpdates,
  kReplanTriggers,
  kReplanCancellations,
  kRejectedOverloadRequests,
  kDeadlineMisses,
  kOsrmFailures,
  kHoursProviderFailures,
  kInfeasibleReplans,
  kCount,
};

enum class MetricHistogram : std::uint8_t {
  kDeserialization,
  kQueueWait,
  kEventApplication,
  kOsrmRequest,
  kHoursProviderRequest,
  kMatrixConversion,
  kPlanner,
  kSerialization,
  kTotalRequest,
  kCount,
};

enum class MetricGauge : std::uint8_t {
  kCriticalQueueDepth,
  kHighQueueDepth,
  kNormalQueueDepth,
  kAdvisoryQueueDepth,
  kCount,
};

inline constexpr std::size_t kMetricHistogramBucketCount = 19;

struct HistogramSnapshot {
  std::uint64_t count{};
  std::uint64_t sum_microseconds{};
  std::uint64_t max_microseconds{};
  std::array<std::uint64_t, kMetricHistogramBucketCount> bucket_counts{};

  [[nodiscard]] std::uint64_t percentile_microseconds(
      std::uint32_t percentile) const noexcept;
};

struct MetricsSnapshot {
  std::array<std::uint64_t,
             static_cast<std::size_t>(MetricCounter::kCount)>
      counters{};
  std::array<HistogramSnapshot,
             static_cast<std::size_t>(MetricHistogram::kCount)>
      histograms{};
  std::array<std::uint64_t, static_cast<std::size_t>(MetricGauge::kCount)>
      gauges{};

  [[nodiscard]] std::uint64_t counter(MetricCounter metric) const noexcept;
  [[nodiscard]] const HistogramSnapshot& histogram(
      MetricHistogram metric) const noexcept;
  [[nodiscard]] std::uint64_t gauge(MetricGauge metric) const noexcept;
};

class MetricsRegistry {
 public:
  MetricsRegistry() = default;
  MetricsRegistry(const MetricsRegistry&) = delete;
  MetricsRegistry& operator=(const MetricsRegistry&) = delete;

  void increment(MetricCounter metric,
                 std::uint64_t amount = 1) noexcept;
  void observe_microseconds(MetricHistogram metric,
                            std::uint64_t value) noexcept;
  void set(MetricGauge metric, std::uint64_t value) noexcept;

  [[nodiscard]] MetricsSnapshot snapshot() const noexcept;

  [[nodiscard]] static std::string_view counter_name(
      MetricCounter metric) noexcept;
  [[nodiscard]] static std::string_view histogram_name(
      MetricHistogram metric) noexcept;
  [[nodiscard]] static std::string_view gauge_name(
      MetricGauge metric) noexcept;

 private:
  struct Histogram {
    std::atomic<std::uint64_t> count{};
    std::atomic<std::uint64_t> sum_microseconds{};
    std::atomic<std::uint64_t> max_microseconds{};
    std::array<std::atomic<std::uint64_t>, kMetricHistogramBucketCount>
        bucket_counts{};
  };

  std::array<std::atomic<std::uint64_t>,
             static_cast<std::size_t>(MetricCounter::kCount)>
      counters_{};
  std::array<Histogram, static_cast<std::size_t>(MetricHistogram::kCount)>
      histograms_{};
  std::array<std::atomic<std::uint64_t>,
             static_cast<std::size_t>(MetricGauge::kCount)>
      gauges_{};
};

}  // namespace liveroute::runtime
