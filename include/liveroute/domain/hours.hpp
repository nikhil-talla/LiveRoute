#pragma once

#include "liveroute/domain/types.hpp"

#include <chrono>
#include <compare>
#include <cstdint>
#include <optional>
#include <stop_token>
#include <string>
#include <variant>
#include <vector>

namespace liveroute::domain {

class LocalDate {
 public:
  [[nodiscard]] static std::optional<LocalDate> create(int year,
                                                        unsigned month,
                                                        unsigned day) noexcept;

  [[nodiscard]] int year() const noexcept { return year_; }
  [[nodiscard]] unsigned month() const noexcept { return month_; }
  [[nodiscard]] unsigned day() const noexcept { return day_; }
  [[nodiscard]] std::chrono::sys_days as_sys_days() const noexcept;

  constexpr auto operator<=>(const LocalDate&) const = default;

 private:
  constexpr LocalDate(int year, unsigned month, unsigned day) noexcept
      : year_(year), month_(month), day_(day) {}

  int year_;
  unsigned month_;
  unsigned day_;
};

class LocalDateRange {
 public:
  [[nodiscard]] static std::optional<LocalDateRange> create(
      LocalDate start_date_inclusive, LocalDate end_date_exclusive) noexcept;

  [[nodiscard]] LocalDate start_date_inclusive() const noexcept {
    return start_date_inclusive_;
  }

  [[nodiscard]] LocalDate end_date_exclusive() const noexcept {
    return end_date_exclusive_;
  }

  constexpr auto operator<=>(const LocalDateRange&) const = default;

 private:
  constexpr LocalDateRange(LocalDate start_date_inclusive,
                           LocalDate end_date_exclusive) noexcept
      : start_date_inclusive_(start_date_inclusive),
        end_date_exclusive_(end_date_exclusive) {}

  LocalDate start_date_inclusive_;
  LocalDate end_date_exclusive_;
};

struct HoursInfo {
  PlaceId place_id;
  std::string time_zone_name;
  LocalDateRange covered_range;
  std::vector<TimeWindow> open_windows;
  std::string source_version;
  std::string tzdata_release;

  [[nodiscard]] bool is_valid() const noexcept;
};

enum class HoursProviderError : std::uint8_t {
  kNotFound,
  kInvalidSource,
  kDeadlineExceeded,
  kCancelled,
  kUnavailable,
};

class HoursLookupResult {
 public:
  explicit HoursLookupResult(HoursInfo hours_info);
  explicit HoursLookupResult(HoursProviderError error) noexcept;

  [[nodiscard]] bool has_hours() const noexcept;
  [[nodiscard]] const HoursInfo& hours_info() const;
  [[nodiscard]] HoursProviderError error() const;

 private:
  std::variant<HoursInfo, HoursProviderError> value_;
};

class PlaceHoursProvider {
 public:
  virtual ~PlaceHoursProvider() = default;

  virtual HoursLookupResult get_hours(PlaceId place_id,
                                      LocalDateRange date_range,
                                      Deadline deadline,
                                      std::stop_token stop_token) = 0;
};

class FixedHoursProvider final : public PlaceHoursProvider {
 public:
  explicit FixedHoursProvider(std::vector<HoursInfo> hours);

  HoursLookupResult get_hours(PlaceId place_id, LocalDateRange date_range,
                              Deadline deadline,
                              std::stop_token stop_token) override;

 private:
  std::vector<HoursInfo> hours_;
};

}  // namespace liveroute::domain
