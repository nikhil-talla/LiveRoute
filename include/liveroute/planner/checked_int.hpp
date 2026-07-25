#pragma once

#include <cstdint>
#include <limits>
#include <optional>

namespace liveroute::planner {

[[nodiscard]] constexpr std::optional<std::int64_t> checked_add(
    std::int64_t left, std::int64_t right) noexcept {
  if ((right > 0 && left > std::numeric_limits<std::int64_t>::max() - right) ||
      (right < 0 && left < std::numeric_limits<std::int64_t>::min() - right)) {
    return std::nullopt;
  }
  return left + right;
}

[[nodiscard]] constexpr std::optional<std::int64_t> checked_subtract(
    std::int64_t left, std::int64_t right) noexcept {
  if (right == std::numeric_limits<std::int64_t>::min()) {
    return left < 0 ? std::optional<std::int64_t>{left - right} : std::nullopt;
  }
  return checked_add(left, -right);
}

[[nodiscard]] constexpr std::optional<std::int64_t> checked_milliseconds(
    std::uint32_t seconds) noexcept {
  constexpr std::int64_t kMillisecondsPerSecond = 1000;
  if (seconds > static_cast<std::uint64_t>(
                    std::numeric_limits<std::int64_t>::max() / kMillisecondsPerSecond)) {
    return std::nullopt;
  }
  return static_cast<std::int64_t>(seconds) * kMillisecondsPerSecond;
}

[[nodiscard]] constexpr std::optional<std::int64_t> checked_absolute_difference(
    std::int64_t left, std::int64_t right) noexcept {
  const auto difference = checked_subtract(left, right);
  if (!difference || *difference == std::numeric_limits<std::int64_t>::min()) {
    return std::nullopt;
  }
  return *difference < 0 ? -*difference : *difference;
}

}  // namespace liveroute::planner
