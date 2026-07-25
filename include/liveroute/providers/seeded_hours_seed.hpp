#pragma once

#include <array>
#include <filesystem>
#include <optional>
#include <string>
#include <vector>

#include "liveroute/domain/hours.hpp"

namespace liveroute::providers {

struct SeededHoursInterval {
  std::uint32_t opens_at_local_seconds;
  std::uint32_t closes_at_local_seconds;
  std::uint8_t closes_day_offset;
  std::optional<std::int32_t> opens_utc_offset_seconds;
  std::optional<std::int32_t> closes_utc_offset_seconds;
};

struct SeededHoursException {
  domain::LocalDate local_date;
  std::vector<SeededHoursInterval> intervals;
};

struct SeededHoursPlace {
  domain::PlaceId place_id;
  std::string time_zone_name;
  std::string source_version;
  std::array<std::vector<SeededHoursInterval>, 7> weekly;
  std::vector<SeededHoursException> exceptions;
};

// Immutable parsed metadata from the configured V1 seed. Interval expansion is
// deliberately left to SeededHoursProvider, which adds TZif conversion.
class SeededHoursSeed {
 public:
  [[nodiscard]] static SeededHoursSeed load(
      const std::filesystem::path& seed_path,
      std::string expected_sha256,
      std::string expected_tzdata_release);

  [[nodiscard]] const std::vector<SeededHoursPlace>& places() const noexcept {
    return places_;
  }

 private:
  std::vector<SeededHoursPlace> places_;
};

}  // namespace liveroute::providers
