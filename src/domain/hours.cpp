#include "liveroute/domain/hours.hpp"

#include <chrono>
#include <stdexcept>
#include <utility>

namespace liveroute::domain {

std::optional<LocalDate> LocalDate::create(int year, unsigned month,
                                           unsigned day) noexcept {
  if (year < 1970 || year > 9999) {
    return std::nullopt;
  }

  const std::chrono::year_month_day value{
      std::chrono::year{year} / std::chrono::month{month} /
      std::chrono::day{day}};
  if (!value.ok()) {
    return std::nullopt;
  }
  return LocalDate{year, month, day};
}

std::chrono::sys_days LocalDate::as_sys_days() const noexcept {
  return std::chrono::sys_days{std::chrono::year{year_} /
                               std::chrono::month{month_} /
                               std::chrono::day{day_}};
}

std::optional<LocalDateRange> LocalDateRange::create(
    LocalDate start_date_inclusive, LocalDate end_date_exclusive) noexcept {
  const auto days = end_date_exclusive.as_sys_days() -
                    start_date_inclusive.as_sys_days();
  if (days <= std::chrono::days::zero() || days > std::chrono::days{32}) {
    return std::nullopt;
  }
  return LocalDateRange{start_date_inclusive, end_date_exclusive};
}

bool HoursInfo::is_valid() const noexcept {
  if (place_id.value.empty() || time_zone_name.empty() ||
      source_version.empty() || tzdata_release.empty()) {
    return false;
  }

  std::optional<UnixTimeMilliseconds> previous_close;
  for (const auto& window : open_windows) {
    if (!window.is_valid() ||
        (previous_close.has_value() && window.opens_at <= *previous_close)) {
      return false;
    }
    previous_close = window.closes_at;
  }
  return true;
}

HoursLookupResult::HoursLookupResult(HoursInfo hours_info)
    : value_(std::move(hours_info)) {}

HoursLookupResult::HoursLookupResult(HoursProviderError error) noexcept
    : value_(error) {}

bool HoursLookupResult::has_hours() const noexcept {
  return std::holds_alternative<HoursInfo>(value_);
}

const HoursInfo& HoursLookupResult::hours_info() const {
  if (!has_hours()) {
    throw std::logic_error("hours lookup did not return hours");
  }
  return std::get<HoursInfo>(value_);
}

HoursProviderError HoursLookupResult::error() const {
  if (has_hours()) {
    throw std::logic_error("hours lookup did not return an error");
  }
  return std::get<HoursProviderError>(value_);
}

FixedHoursProvider::FixedHoursProvider(std::vector<HoursInfo> hours)
    : hours_(std::move(hours)) {
  for (const auto& hours_info : hours_) {
    if (!hours_info.is_valid()) {
      throw std::invalid_argument("fixed hours provider requires valid hours");
    }
  }
}

HoursLookupResult FixedHoursProvider::get_hours(PlaceId place_id,
                                                 LocalDateRange date_range,
                                                 Deadline deadline,
                                                 std::stop_token stop_token) {
  if (stop_token.stop_requested()) {
    return HoursLookupResult{HoursProviderError::kCancelled};
  }
  if (std::chrono::steady_clock::now() >= deadline) {
    return HoursLookupResult{HoursProviderError::kDeadlineExceeded};
  }

  for (const auto& hours_info : hours_) {
    if (hours_info.place_id == place_id &&
        hours_info.covered_range == date_range) {
      return HoursLookupResult{hours_info};
    }
  }
  return HoursLookupResult{HoursProviderError::kNotFound};
}

}  // namespace liveroute::domain
