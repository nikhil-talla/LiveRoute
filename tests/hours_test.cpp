#include "liveroute/domain/hours.hpp"

#include <chrono>
#include <optional>
#include <stdexcept>
#include <stop_token>
#include <vector>

namespace {

using liveroute::domain::FixedHoursProvider;
using liveroute::domain::HoursInfo;
using liveroute::domain::HoursProviderError;
using liveroute::domain::LocalDate;
using liveroute::domain::LocalDateRange;
using liveroute::domain::PlaceId;
using liveroute::domain::TimeWindow;
using liveroute::domain::UnixTimeMilliseconds;

std::optional<LocalDateRange> test_range() {
  const auto start = LocalDate::create(2026, 7, 1);
  const auto end = LocalDate::create(2026, 7, 3);
  if (!start.has_value() || !end.has_value()) {
    return std::nullopt;
  }
  return LocalDateRange::create(*start, *end);
}

HoursInfo valid_hours(LocalDateRange range) {
  return HoursInfo{
      .place_id = PlaceId{"place-1"},
      .time_zone_name = "America/New_York",
      .covered_range = range,
      .open_windows = {
          TimeWindow{UnixTimeMilliseconds{100}, UnixTimeMilliseconds{200}},
          TimeWindow{UnixTimeMilliseconds{300}, UnixTimeMilliseconds{400}},
      },
      .source_version = "seed-v1:test",
      .tzdata_release = "2026c",
  };
}

bool rejects_invalid_range() {
  const auto date = LocalDate::create(2026, 7, 1);
  const auto later_date = LocalDate::create(2026, 8, 3);
  if (!date.has_value() || !later_date.has_value()) {
    return false;
  }
  return !LocalDateRange::create(*date, *date).has_value() &&
         !LocalDateRange::create(*date, *later_date).has_value();
}

bool rejects_touching_windows(LocalDateRange range) {
  auto hours = valid_hours(range);
  hours.open_windows = {
      TimeWindow{UnixTimeMilliseconds{100}, UnixTimeMilliseconds{200}},
      TimeWindow{UnixTimeMilliseconds{200}, UnixTimeMilliseconds{300}},
  };
  return !hours.is_valid();
}

bool rejects_invalid_fixed_hours(LocalDateRange range) {
  auto hours = valid_hours(range);
  hours.source_version.clear();
  try {
    [[maybe_unused]] FixedHoursProvider provider({std::move(hours)});
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

}  // namespace

int main() {
  const auto range = test_range();
  const auto leap_day = LocalDate::create(2028, 2, 29);
  const auto invalid_day = LocalDate::create(2027, 2, 29);
  if (!range.has_value() || !leap_day.has_value() || invalid_day.has_value() ||
      !rejects_invalid_range() || !rejects_touching_windows(*range) ||
      !rejects_invalid_fixed_hours(*range)) {
    return 1;
  }

  FixedHoursProvider provider({valid_hours(*range)});
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds{1};
  const auto found = provider.get_hours(PlaceId{"place-1"}, *range, deadline, {});
  const auto missing =
      provider.get_hours(PlaceId{"missing"}, *range, deadline, {});
  std::stop_source stopped;
  stopped.request_stop();
  const auto cancelled =
      provider.get_hours(PlaceId{"place-1"}, *range, deadline, stopped.get_token());
  const auto expired = provider.get_hours(
      PlaceId{"place-1"}, *range,
      std::chrono::steady_clock::now() - std::chrono::seconds{1}, {});

  if (!found.has_hours() || !found.hours_info().is_valid() ||
      missing.has_hours() || missing.error() != HoursProviderError::kNotFound ||
      cancelled.has_hours() ||
      cancelled.error() != HoursProviderError::kCancelled || expired.has_hours() ||
      expired.error() != HoursProviderError::kDeadlineExceeded) {
    return 1;
  }

  return 0;
}
