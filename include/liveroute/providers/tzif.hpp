#pragma once

#include <compare>
#include <cstdint>
#include <filesystem>
#include <string>
#include <vector>

namespace liveroute::providers {

enum class LocalTimeDiscontinuityKind : std::uint8_t {
  kGap,
  kFold,
};

// Local civil seconds use the Unix epoch as their calendar origin without an
// offset applied. A gap/fold is the half-open local interval [first, last).
struct LocalTimeDiscontinuity {
  LocalTimeDiscontinuityKind kind;
  std::int64_t first_local_second;
  std::int64_t last_local_second;

  constexpr auto operator<=>(const LocalTimeDiscontinuity&) const = default;
};

// Reads a compiled TZif v2/v3/v4 zone file. It exposes explicit transition
// discontinuities and retains the POSIX footer for the recurrence expansion
// performed by the seeded-hours adapter.
class TzifData {
 public:
  [[nodiscard]] static TzifData load(const std::filesystem::path& path);

  [[nodiscard]] const std::vector<LocalTimeDiscontinuity>&
  explicit_discontinuities() const noexcept {
    return explicit_discontinuities_;
  }

  [[nodiscard]] const std::string& posix_footer() const noexcept {
    return posix_footer_;
  }

  [[nodiscard]] std::vector<LocalTimeDiscontinuity>
  discontinuities_for_years(int first_year, int last_year) const;

  [[nodiscard]] std::int32_t offset_at_utc(std::int64_t unix_seconds) const;
  [[nodiscard]] std::vector<std::int32_t> available_offsets() const;

 private:
  std::vector<LocalTimeDiscontinuity> explicit_discontinuities_;
  std::string posix_footer_;
  std::int64_t last_explicit_transition_utc_;
  std::vector<std::int64_t> transitions_;
  std::vector<std::uint8_t> transition_types_;
  std::vector<std::int32_t> utc_offsets_;
};

}  // namespace liveroute::providers
