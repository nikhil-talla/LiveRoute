#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <vector>

namespace liveroute::domain {

struct RouteEstimate {
  std::chrono::seconds duration{};
  std::uint32_t distance_meters{};
  bool reachable{};

  [[nodiscard]] constexpr bool is_valid() const noexcept {
    return duration >= std::chrono::seconds::zero();
  }
};

class TravelTimeMatrix {
 public:
  TravelTimeMatrix(std::size_t location_count,
                   std::vector<RouteEstimate> estimates);

  [[nodiscard]] std::size_t location_count() const noexcept {
    return location_count_;
  }

  [[nodiscard]] const RouteEstimate& at(std::size_t origin,
                                        std::size_t destination) const;

 private:
  std::size_t location_count_;
  std::vector<RouteEstimate> estimates_;
};

}  // namespace liveroute::domain
