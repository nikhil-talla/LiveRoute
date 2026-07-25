#include <chrono>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <iterator>
#include <stdexcept>
#include <string>
#include <vector>

#include "liveroute/providers/seeded_hours_provider.hpp"
#include "liveroute/providers/sha256.hpp"

namespace {

std::string seed_json(const std::string& time_zone_name,
                      const std::string& exceptions) {
  constexpr const char* interval =
      "[{\"opens_at_local\":\"00:00:00\",\"closes_at_local\":\"00:00:00\","
      "\"closes_day_offset\":1}]";
  return "{\"schema_version\":1,\"tzdata_release\":\"2026c\",\"places\":[{"
         "\"place_id\":\"fixture-place\",\"time_zone_name\":\"" +
         time_zone_name + "\",\"weekly\":{\"monday\":" + interval +
         ",\"tuesday\":" + interval + ",\"wednesday\":" + interval +
         ",\"thursday\":" + interval + ",\"friday\":" + interval +
         ",\"saturday\":" + interval + ",\"sunday\":" + interval +
         "},\"exceptions\":" + exceptions + "}]}";
}

std::filesystem::path write_seed(const std::string& name, const std::string& bytes) {
  const auto path = std::filesystem::temp_directory_path() / name;
  std::ofstream output(path, std::ios::binary | std::ios::trunc);
  output << bytes;
  return path;
}

liveroute::providers::SeededHoursProvider load_provider(
    const std::filesystem::path& path, const char* zoneinfo_path) {
  const auto bytes = [&path] {
    std::ifstream input(path, std::ios::binary);
    return std::string{std::istreambuf_iterator<char>{input}, {}};
  }();
  return liveroute::providers::SeededHoursProvider::load({
      .seed_file_path = path,
      .seed_file_sha256 = liveroute::providers::sha256_hex(bytes),
      .tzdata_release = "2026c",
      .tzdata_zoneinfo_path = zoneinfo_path,
  });
}

bool rejects_seed(const std::filesystem::path& path, const char* zoneinfo_path) {
  try {
    static_cast<void>(load_provider(path, zoneinfo_path));
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

}  // namespace

int main() {
  const char* zoneinfo_path = std::getenv("LIVEROUTE_TZDATA_ZONEINFO_PATH");
  if (zoneinfo_path == nullptr) {
    std::cerr << "LIVEROUTE_TZDATA_ZONEINFO_PATH is required\n";
    return 1;
  }
  const auto start = liveroute::domain::LocalDate::create(2026, 7, 1);
  const auto end = liveroute::domain::LocalDate::create(2026, 7, 3);
  if (!start || !end) return 1;
  const auto range = liveroute::domain::LocalDateRange::create(*start, *end);
  if (!range) return 1;
  const auto boundary_start = liveroute::domain::LocalDate::create(9999, 12, 30);
  const auto boundary_end = liveroute::domain::LocalDate::create(9999, 12, 31);
  if (!boundary_start || !boundary_end) return 1;
  const auto boundary_range = liveroute::domain::LocalDateRange::create(
      *boundary_start, *boundary_end);
  if (!boundary_range) return 1;
  std::vector<std::filesystem::path> temporary_seeds;
  try {
    auto provider = liveroute::providers::SeededHoursProvider::load({
        .seed_file_path = std::filesystem::path{"data/hours/liveroute-v1-hours-seed.json"},
        .seed_file_sha256 = "3582832ac46c1b841d684bcf6ae9751830d819b8ee14f88bcaac7c541b25e0d7",
        .tzdata_release = "2026c",
        .tzdata_zoneinfo_path = zoneinfo_path,
    });
    const auto result = provider.get_hours(
        liveroute::domain::PlaceId{"fixture-place"}, *range,
        std::chrono::steady_clock::now() + std::chrono::seconds{2}, {});
    if (!result.has_hours() || !result.hours_info().is_valid() ||
        result.hours_info().open_windows.size() != 1 ||
        result.hours_info().tzdata_release != "2026c") {
      return 1;
    }
    const auto boundary_result = provider.get_hours(
        liveroute::domain::PlaceId{"fixture-place"}, *boundary_range,
        std::chrono::steady_clock::now() + std::chrono::seconds{2}, {});
    if (!boundary_result.has_hours() || !boundary_result.hours_info().is_valid() ||
        boundary_result.hours_info().open_windows.size() != 1) {
      return 1;
    }

    const auto fold_seed = write_seed(
        "liveroute-valid-fold-hours.json",
        seed_json("America/New_York",
                  "[{\"local_date\":\"2026-11-01\",\"intervals\":[{"
                  "\"opens_at_local\":\"01:30:00\",\"closes_at_local\":\"02:30:00\","
                  "\"closes_day_offset\":0,\"opens_utc_offset_seconds\":-14400}]}]"));
    temporary_seeds.push_back(fold_seed);
    auto fold_provider = load_provider(fold_seed, zoneinfo_path);
    const auto fold_start = liveroute::domain::LocalDate::create(2026, 11, 1);
    const auto fold_end = liveroute::domain::LocalDate::create(2026, 11, 2);
    if (!fold_start || !fold_end) return 1;
    const auto fold_range = liveroute::domain::LocalDateRange::create(*fold_start, *fold_end);
    if (!fold_range) return 1;
    const auto fold_result = fold_provider.get_hours(
        liveroute::domain::PlaceId{"fixture-place"}, *fold_range,
        std::chrono::steady_clock::now() + std::chrono::seconds{2}, {});
    if (!fold_result.has_hours() || fold_result.hours_info().open_windows.size() != 1) {
      return 1;
    }
    using namespace std::chrono;
    const auto expected_fold_open = duration_cast<milliseconds>(
        (sys_days{year{2026} / November / day{1}} + hours{5} + minutes{30})
            .time_since_epoch()).count();
    const auto expected_fold_close = duration_cast<milliseconds>(
        (sys_days{year{2026} / November / day{1}} + hours{7} + minutes{30})
            .time_since_epoch()).count();
    if (fold_result.hours_info().open_windows.front().opens_at.value() !=
            expected_fold_open ||
        fold_result.hours_info().open_windows.front().closes_at.value() !=
            expected_fold_close) {
      return 1;
    }

    const auto invalid_offset_seed = write_seed(
        "liveroute-invalid-offset-hours.json",
        seed_json("America/New_York",
                  "[{\"local_date\":\"2026-11-01\",\"intervals\":[{"
                  "\"opens_at_local\":\"01:30:00\",\"closes_at_local\":\"02:30:00\","
                  "\"closes_day_offset\":0,\"opens_utc_offset_seconds\":-14400,"
                  "\"closes_utc_offset_seconds\":-14400}]}]"));
    temporary_seeds.push_back(invalid_offset_seed);
    if (!rejects_seed(invalid_offset_seed, zoneinfo_path)) return 1;

    const auto non_us_seed = write_seed("liveroute-non-us-hours.json",
                                        seed_json("Europe/London", "[]"));
    temporary_seeds.push_back(non_us_seed);
    if (!rejects_seed(non_us_seed, zoneinfo_path)) return 1;
  } catch (const std::exception& error) {
    for (const auto& path : temporary_seeds) std::filesystem::remove(path);
    std::cerr << error.what() << '\n';
    return 1;
  }
  for (const auto& path : temporary_seeds) std::filesystem::remove(path);
  return 0;
}
