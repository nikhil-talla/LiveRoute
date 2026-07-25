#include <cstdint>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>

#include "liveroute/providers/tzif.hpp"

namespace {

void append_u32(std::string& output, std::uint32_t value) {
  for (int shift = 24; shift >= 0; shift -= 8) {
    output.push_back(static_cast<char>((value >> shift) & 0xffU));
  }
}

void append_i64(std::string& output, std::int64_t value) {
  const auto encoded = static_cast<std::uint64_t>(value);
  for (int shift = 56; shift >= 0; shift -= 8) {
    output.push_back(static_cast<char>((encoded >> shift) & 0xffU));
  }
}

void append_header(std::string& output) {
  output.append("TZif3", 5);
  output.append(15, '\0');
  append_u32(output, 0);
  append_u32(output, 0);
  append_u32(output, 0);
  append_u32(output, 2);
  append_u32(output, 2);
  append_u32(output, 8);
}

void append_block(std::string& output, std::size_t width) {
  const auto append_timestamp = [&output, width](std::int64_t value) {
    if (width == 4) {
      append_u32(output, static_cast<std::uint32_t>(value));
    } else {
      append_i64(output, value);
    }
  };
  append_timestamp(1000);
  append_timestamp(2000);
  output.push_back('\x01');
  output.push_back('\x00');
  append_u32(output, 0);
  output.push_back('\0');
  output.push_back('\0');
  append_u32(output, 3600);
  output.push_back('\x01');
  output.push_back('\x04');
  output.append("UTC\0DST\0", 8);
}

std::filesystem::path write_fixture() {
  std::string fixture;
  append_header(fixture);
  append_block(fixture, 4);
  append_header(fixture);
  append_block(fixture, 8);
  fixture.append("\nSTD0DST,M3.2.0/2,M11.1.0/2\n");

  const auto path = std::filesystem::temp_directory_path() / "liveroute-tzif-test";
  std::ofstream output(path, std::ios::binary | std::ios::trunc);
  output.write(fixture.data(), static_cast<std::streamsize>(fixture.size()));
  return path;
}

}  // namespace

int main() {
  const auto fixture = write_fixture();
  try {
    const auto data = liveroute::providers::TzifData::load(fixture);
    std::filesystem::remove(fixture);
    const auto& discontinuities = data.explicit_discontinuities();
    const auto recurrence = data.discontinuities_for_years(2026, 2026);
    const auto offsets = data.available_offsets();
    using namespace std::chrono;
    const auto expected_fold_start = duration_cast<seconds>(
        sys_days{year{2026} / November / day{1}}.time_since_epoch()).count() +
        3600;
    const auto expected_fold_end = expected_fold_start + 3600;
    if (data.posix_footer() != "STD0DST,M3.2.0/2,M11.1.0/2" ||
        recurrence.size() != 2 ||
        recurrence[0].kind !=
            liveroute::providers::LocalTimeDiscontinuityKind::kGap ||
        recurrence[1].kind !=
            liveroute::providers::LocalTimeDiscontinuityKind::kFold ||
        recurrence[1].first_local_second != expected_fold_start ||
        recurrence[1].last_local_second != expected_fold_end ||
        data.offset_at_utc(500) != 0 || data.offset_at_utc(1500) != 3600 ||
        offsets != std::vector<std::int32_t>{0, 3600} ||
        discontinuities.size() != 2 ||
        discontinuities[0].kind !=
            liveroute::providers::LocalTimeDiscontinuityKind::kGap ||
        discontinuities[0].first_local_second != 1000 ||
        discontinuities[0].last_local_second != 4600 ||
        discontinuities[1].kind !=
            liveroute::providers::LocalTimeDiscontinuityKind::kFold ||
        discontinuities[1].first_local_second != 2000 ||
        discontinuities[1].last_local_second != 5600) {
      return 1;
    }
  } catch (const std::exception& error) {
    std::filesystem::remove(fixture);
    std::cerr << error.what() << '\n';
    return 1;
  }
  return 0;
}
