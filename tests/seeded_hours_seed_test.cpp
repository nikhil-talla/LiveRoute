#include <filesystem>
#include <iostream>

#include "liveroute/providers/seeded_hours_seed.hpp"

int main() {
  try {
    const auto seed = liveroute::providers::SeededHoursSeed::load(
        std::filesystem::path{"data/hours/liveroute-v1-hours-seed.json"},
        "3582832ac46c1b841d684bcf6ae9751830d819b8ee14f88bcaac7c541b25e0d7",
        "2026c");
    if (seed.places().size() != 1 || seed.places().front().place_id.value != "fixture-place" ||
        seed.places().front().source_version.rfind("seed-v1:", 0) != 0) return 1;
  } catch (const std::exception& error) {
    std::cerr << error.what() << '\n';
    return 1;
  }
  return 0;
}
