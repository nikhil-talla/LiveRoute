#include "liveroute/providers/tzif.hpp"

#include <algorithm>
#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <fstream>
#include <iterator>
#include <limits>
#include <stdexcept>
#include <string_view>
#include <utility>
#include <vector>

namespace liveroute::providers {
namespace {

struct HeaderCounts {
  std::uint32_t ttisgmtcnt;
  std::uint32_t ttisstdcnt;
  std::uint32_t leapcnt;
  std::uint32_t timecnt;
  std::uint32_t typecnt;
  std::uint32_t charcnt;
};

struct Block {
  std::vector<std::int64_t> transitions;
  std::vector<std::uint8_t> transition_types;
  std::vector<std::int32_t> utc_offsets;
};

struct PosixRule {
  int month;
  int week;
  int weekday;
  int transition_seconds;
  char time_basis;
};

struct PosixFooter {
  std::int32_t standard_offset;
  std::int32_t daylight_offset;
  PosixRule daylight_start;
  PosixRule standard_start;
  bool has_daylight_time;
};

[[nodiscard]] std::uint32_t read_u32(std::string_view bytes,
                                     std::size_t& position) {
  if (position > bytes.size() || bytes.size() - position < 4) {
    throw std::invalid_argument("truncated TZif integer");
  }
  const auto* input = reinterpret_cast<const unsigned char*>(bytes.data());
  const auto result = (static_cast<std::uint32_t>(input[position]) << 24U) |
                      (static_cast<std::uint32_t>(input[position + 1]) << 16U) |
                      (static_cast<std::uint32_t>(input[position + 2]) << 8U) |
                      static_cast<std::uint32_t>(input[position + 3]);
  position += 4;
  return result;
}

[[nodiscard]] std::int64_t read_signed(std::string_view bytes,
                                       std::size_t& position,
                                       std::size_t width) {
  if (width != 4 && width != 8) {
    throw std::invalid_argument("invalid TZif timestamp width");
  }
  if (position > bytes.size() || bytes.size() - position < width) {
    throw std::invalid_argument("truncated TZif timestamp");
  }
  const auto* input = reinterpret_cast<const unsigned char*>(bytes.data());
  std::uint64_t encoded = 0;
  for (std::size_t index = 0; index < width; ++index) {
    encoded = (encoded << 8U) | input[position + index];
  }
  position += width;
  if (width == 4) {
    return static_cast<std::int32_t>(static_cast<std::uint32_t>(encoded));
  }
  return static_cast<std::int64_t>(encoded);
}

[[nodiscard]] HeaderCounts read_header(std::string_view bytes,
                                       std::size_t& position,
                                       char& version) {
  if (position > bytes.size() || bytes.size() - position < 44) {
    throw std::invalid_argument("truncated TZif header");
  }
  if (bytes.substr(position, 4) != "TZif") {
    throw std::invalid_argument("invalid TZif magic");
  }
  version = bytes[position + 4];
  if (version != '2' && version != '3' && version != '4') {
    throw std::invalid_argument("TZif version must be v2, v3, or v4");
  }
  position += 20;
  return HeaderCounts{
      .ttisgmtcnt = read_u32(bytes, position),
      .ttisstdcnt = read_u32(bytes, position),
      .leapcnt = read_u32(bytes, position),
      .timecnt = read_u32(bytes, position),
      .typecnt = read_u32(bytes, position),
      .charcnt = read_u32(bytes, position),
  };
}

void advance(std::string_view bytes, std::size_t& position,
             std::size_t count) {
  if (position > bytes.size() || bytes.size() - position < count) {
    throw std::invalid_argument("truncated TZif data block");
  }
  position += count;
}

void skip_block(std::string_view bytes, std::size_t& position,
                const HeaderCounts& counts, std::size_t timestamp_width) {
  const auto check_multiply = [](std::uint32_t left, std::size_t right) {
    if (left > std::numeric_limits<std::size_t>::max() / right) {
      throw std::invalid_argument("TZif data block is too large");
    }
    return static_cast<std::size_t>(left) * right;
  };
  advance(bytes, position, check_multiply(counts.timecnt, timestamp_width));
  advance(bytes, position, counts.timecnt);
  advance(bytes, position, check_multiply(counts.typecnt, 6));
  advance(bytes, position, counts.charcnt);
  advance(bytes, position,
          check_multiply(counts.leapcnt, timestamp_width + 4));
  advance(bytes, position, counts.ttisstdcnt);
  advance(bytes, position, counts.ttisgmtcnt);
}

[[nodiscard]] Block read_block(std::string_view bytes, std::size_t& position,
                               const HeaderCounts& counts,
                               std::size_t timestamp_width) {
  if (counts.typecnt == 0) {
    throw std::invalid_argument("TZif file has no local-time types");
  }

  Block result;
  result.transitions.reserve(counts.timecnt);
  for (std::uint32_t index = 0; index < counts.timecnt; ++index) {
    const auto transition = read_signed(bytes, position, timestamp_width);
    if (!result.transitions.empty() && transition <= result.transitions.back()) {
      throw std::invalid_argument("TZif transitions are not strictly ordered");
    }
    result.transitions.push_back(transition);
  }

  result.transition_types.reserve(counts.timecnt);
  for (std::uint32_t index = 0; index < counts.timecnt; ++index) {
    if (position == bytes.size()) {
      throw std::invalid_argument("truncated TZif transition type");
    }
    const auto type = static_cast<std::uint8_t>(bytes[position++]);
    if (type >= counts.typecnt) {
      throw std::invalid_argument("TZif transition type is out of range");
    }
    result.transition_types.push_back(type);
  }

  result.utc_offsets.reserve(counts.typecnt);
  for (std::uint32_t index = 0; index < counts.typecnt; ++index) {
    result.utc_offsets.push_back(
        static_cast<std::int32_t>(read_signed(bytes, position, 4)));
    advance(bytes, position, 2);
  }
  advance(bytes, position, counts.charcnt);
  advance(bytes, position,
          static_cast<std::size_t>(counts.leapcnt) * (timestamp_width + 4));
  advance(bytes, position, counts.ttisstdcnt);
  advance(bytes, position, counts.ttisgmtcnt);
  return result;
}

[[nodiscard]] std::vector<LocalTimeDiscontinuity> discontinuities(
    const Block& block) {
  std::vector<LocalTimeDiscontinuity> result;
  if (block.transitions.empty()) {
    return result;
  }

  std::size_t active_type = 0;
  for (std::size_t index = 0; index < block.transitions.size(); ++index) {
    const auto next_type = block.transition_types[index];
    const auto before_offset = block.utc_offsets[active_type];
    const auto after_offset = block.utc_offsets[next_type];
    const auto transition = block.transitions[index];
    const auto before_local = transition + before_offset;
    const auto after_local = transition + after_offset;
    if (after_local > before_local) {
      result.push_back({.kind = LocalTimeDiscontinuityKind::kGap,
                        .first_local_second = before_local,
                        .last_local_second = after_local});
    } else if (after_local < before_local) {
      result.push_back({.kind = LocalTimeDiscontinuityKind::kFold,
                        .first_local_second = after_local,
                        .last_local_second = before_local});
    }
    active_type = next_type;
  }
  return result;
}

[[nodiscard]] int parse_number(std::string_view input, std::size_t& position,
                               int maximum, const char* description) {
  const auto start = position;
  int value = 0;
  while (position < input.size() && input[position] >= '0' &&
         input[position] <= '9') {
    if (value > (maximum - (input[position] - '0')) / 10) {
      throw std::invalid_argument(description);
    }
    value = value * 10 + (input[position++] - '0');
  }
  if (position == start) {
    throw std::invalid_argument(description);
  }
  return value;
}

void skip_abbreviation(std::string_view input, std::size_t& position) {
  if (position == input.size()) {
    throw std::invalid_argument("POSIX footer lacks abbreviation");
  }
  if (input[position] == '<') {
    const auto close = input.find('>', position + 1);
    if (close == std::string_view::npos || close == position + 1) {
      throw std::invalid_argument("invalid POSIX quoted abbreviation");
    }
    position = close + 1;
    return;
  }
  const auto start = position;
  while (position < input.size() && ((input[position] >= 'A' && input[position] <= 'Z') ||
                                     (input[position] >= 'a' && input[position] <= 'z'))) {
    ++position;
  }
  if (position - start < 3) {
    throw std::invalid_argument("invalid POSIX abbreviation");
  }
}

[[nodiscard]] std::int32_t parse_posix_offset(std::string_view input,
                                               std::size_t& position) {
  int sign = 1;
  if (position < input.size() && (input[position] == '+' || input[position] == '-')) {
    sign = input[position++] == '-' ? -1 : 1;
  }
  const int hours = parse_number(input, position, 167, "invalid POSIX offset");
  int minutes = 0;
  int seconds = 0;
  if (position < input.size() && input[position] == ':') {
    ++position;
    minutes = parse_number(input, position, 59, "invalid POSIX offset");
    if (position < input.size() && input[position] == ':') {
      ++position;
      seconds = parse_number(input, position, 59, "invalid POSIX offset");
    }
  }
  const auto seconds_east = -sign * (hours * 3600 + minutes * 60 + seconds);
  return static_cast<std::int32_t>(seconds_east);
}

[[nodiscard]] PosixRule parse_rule(std::string_view input, std::size_t& position) {
  if (position == input.size() || input[position++] != 'M') {
    throw std::invalid_argument("V1 requires POSIX M transition rules");
  }
  const int month = parse_number(input, position, 12, "invalid POSIX month");
  if (month == 0 || position == input.size() || input[position++] != '.') {
    throw std::invalid_argument("invalid POSIX M rule");
  }
  const int week = parse_number(input, position, 5, "invalid POSIX week");
  if (week == 0 || position == input.size() || input[position++] != '.') {
    throw std::invalid_argument("invalid POSIX M rule");
  }
  const int weekday = parse_number(input, position, 6, "invalid POSIX weekday");
  int transition_seconds = 2 * 3600;
  char time_basis = 'w';
  if (position < input.size() && input[position] == '/') {
    ++position;
    int sign = 1;
    if (position < input.size() && (input[position] == '+' || input[position] == '-')) {
      sign = input[position++] == '-' ? -1 : 1;
    }
    const int hours = parse_number(input, position, 167, "invalid POSIX rule time");
    int minutes = 0;
    int seconds = 0;
    if (position < input.size() && input[position] == ':') {
      ++position;
      minutes = parse_number(input, position, 59, "invalid POSIX rule time");
      if (position < input.size() && input[position] == ':') {
        ++position;
        seconds = parse_number(input, position, 59, "invalid POSIX rule time");
      }
    }
    transition_seconds = sign * (hours * 3600 + minutes * 60 + seconds);
    if (position < input.size() &&
        (input[position] == 'w' || input[position] == 's' || input[position] == 'u' ||
         input[position] == 'g' || input[position] == 'z')) {
      time_basis = input[position++];
    }
  }
  return {month, week, weekday, transition_seconds, time_basis};
}

[[nodiscard]] PosixFooter parse_footer(std::string_view input) {
  std::size_t position = 0;
  skip_abbreviation(input, position);
  const auto standard_offset = parse_posix_offset(input, position);
  if (position == input.size()) {
    return {standard_offset, standard_offset, {}, {}, false};
  }
  skip_abbreviation(input, position);
  std::int32_t daylight_offset = static_cast<std::int32_t>(standard_offset + 3600);
  if (position < input.size() && input[position] != ',') {
    daylight_offset = parse_posix_offset(input, position);
  }
  if (position == input.size() || input[position++] != ',') {
    throw std::invalid_argument("POSIX daylight footer lacks start rule");
  }
  const auto start = parse_rule(input, position);
  if (position == input.size() || input[position++] != ',') {
    throw std::invalid_argument("POSIX daylight footer lacks end rule");
  }
  const auto end = parse_rule(input, position);
  if (position != input.size()) {
    throw std::invalid_argument("trailing POSIX footer data");
  }
  return {standard_offset, daylight_offset, start, end, true};
}

[[nodiscard]] std::int64_t local_seconds(int year, const PosixRule& rule) {
  using namespace std::chrono;
  const auto first = sys_days{std::chrono::year{year} /
                              std::chrono::month{static_cast<unsigned>(rule.month)} /
                              std::chrono::day{1}};
  const auto first_weekday = weekday{first}.c_encoding();
  int day = 1 + (rule.weekday - static_cast<int>(first_weekday) + 7) % 7 +
            (rule.week - 1) * 7;
  const year_month_day_last last_day_of_month{
      std::chrono::year{year} /
      std::chrono::month{static_cast<unsigned>(rule.month)} / last};
  const auto month_end = unsigned{last_day_of_month.day()};
  if (day > static_cast<int>(month_end)) {
    day -= 7;
  }
  const auto date = sys_days{std::chrono::year{year} /
                             std::chrono::month{static_cast<unsigned>(rule.month)} /
                             std::chrono::day{static_cast<unsigned>(day)}};
  return duration_cast<seconds>(date.time_since_epoch()).count() +
         rule.transition_seconds;
}

[[nodiscard]] std::int64_t transition_utc(int year, const PosixRule& rule,
                                           std::int32_t wall_offset,
                                           std::int32_t standard_offset) {
  const auto local = local_seconds(year, rule);
  switch (rule.time_basis) {
    case 'u':
    case 'g':
    case 'z':
      return local;
    case 's':
      return local - standard_offset;
    case 'w':
      return local - wall_offset;
  }
  throw std::invalid_argument("invalid POSIX transition basis");
}

[[nodiscard]] std::vector<LocalTimeDiscontinuity> recurrence_discontinuities(
    const PosixFooter& footer, int first_year, int last_year,
    std::int64_t after_utc) {
  std::vector<LocalTimeDiscontinuity> result;
  if (!footer.has_daylight_time) {
    return result;
  }
  for (int year = first_year; year <= last_year; ++year) {
    const auto start = transition_utc(year, footer.daylight_start,
                                      footer.standard_offset, footer.standard_offset);
    const auto end = transition_utc(year, footer.standard_start,
                                    footer.daylight_offset, footer.standard_offset);
    if (start > after_utc) {
      const auto local = start + footer.standard_offset;
      result.push_back({LocalTimeDiscontinuityKind::kGap, local,
                        local + (footer.daylight_offset - footer.standard_offset)});
    }
    if (end > after_utc) {
      const auto before_local = end + footer.daylight_offset;
      const auto after_local = end + footer.standard_offset;
      result.push_back({LocalTimeDiscontinuityKind::kFold,
                        std::min(before_local, after_local),
                        std::max(before_local, after_local)});
    }
  }
  return result;
}

}  // namespace

TzifData TzifData::load(const std::filesystem::path& path) {
  std::ifstream input(path, std::ios::binary);
  if (!input) {
    throw std::invalid_argument("cannot open TZif zone file");
  }
  const std::string bytes{std::istreambuf_iterator<char>{input}, {}};
  std::size_t position = 0;
  char first_version = '\0';
  const auto first_counts = read_header(bytes, position, first_version);
  skip_block(bytes, position, first_counts, 4);

  char second_version = '\0';
  const auto second_counts = read_header(bytes, position, second_version);
  const auto block = read_block(bytes, position, second_counts, 8);
  if (position == bytes.size() || bytes[position++] != '\n') {
    throw std::invalid_argument("TZif POSIX footer is missing");
  }
  const auto footer_end = bytes.find('\n', position);
  if (footer_end == std::string::npos || footer_end + 1 != bytes.size()) {
    throw std::invalid_argument("TZif POSIX footer is malformed");
  }

  TzifData result;
  result.explicit_discontinuities_ = discontinuities(block);
  result.posix_footer_ = bytes.substr(position, footer_end - position);
  result.last_explicit_transition_utc_ =
      block.transitions.empty() ? std::numeric_limits<std::int64_t>::min()
                                : block.transitions.back();
  result.transitions_ = block.transitions;
  result.transition_types_ = block.transition_types;
  result.utc_offsets_ = block.utc_offsets;
  return result;
}

std::vector<LocalTimeDiscontinuity> TzifData::discontinuities_for_years(
    int first_year, int last_year) const {
  if (first_year < 1970 || last_year < first_year || last_year > 9999) {
    throw std::invalid_argument("invalid TZif validation year range");
  }
  using namespace std::chrono;
  const auto first_local = duration_cast<seconds>(
      sys_days{year{first_year} / month{1} / day{1}}.time_since_epoch()).count();
  const auto last_local = duration_cast<seconds>(
      sys_days{year{last_year + 1} / month{1} / day{1}}.time_since_epoch()).count();
  auto result = explicit_discontinuities_;
  const auto footer = parse_footer(posix_footer_);
  auto recurrence = recurrence_discontinuities(footer, first_year, last_year,
                                               last_explicit_transition_utc_);
  result.insert(result.end(), std::make_move_iterator(recurrence.begin()),
                std::make_move_iterator(recurrence.end()));
  result.erase(std::remove_if(result.begin(), result.end(),
                              [first_local, last_local](const auto& value) {
                                return value.last_local_second <= first_local ||
                                       value.first_local_second >= last_local;
                              }),
               result.end());
  std::sort(result.begin(), result.end(), [](const auto& left, const auto& right) {
    return left.first_local_second < right.first_local_second;
  });
  return result;
}

std::int32_t TzifData::offset_at_utc(std::int64_t unix_seconds) const {
  if (utc_offsets_.empty()) {
    throw std::logic_error("TZif data has no offsets");
  }
  if (unix_seconds <= last_explicit_transition_utc_ || transitions_.empty()) {
    const auto iterator = std::upper_bound(transitions_.begin(), transitions_.end(), unix_seconds);
    if (iterator == transitions_.begin()) return utc_offsets_.front();
    const auto index = static_cast<std::size_t>(iterator - transitions_.begin() - 1);
    return utc_offsets_[transition_types_[index]];
  }
  const auto footer = parse_footer(posix_footer_);
  if (!footer.has_daylight_time) return footer.standard_offset;
  using namespace std::chrono;
  const auto date = year_month_day{floor<days>(sys_seconds{seconds{unix_seconds}})};
  const int year_value = static_cast<int>(date.year());
  const auto start = transition_utc(year_value, footer.daylight_start,
                                    footer.standard_offset, footer.standard_offset);
  const auto end = transition_utc(year_value, footer.standard_start,
                                  footer.daylight_offset, footer.standard_offset);
  const bool daylight = start < end ? unix_seconds >= start && unix_seconds < end
                                    : unix_seconds >= start || unix_seconds < end;
  return daylight ? footer.daylight_offset : footer.standard_offset;
}

std::vector<std::int32_t> TzifData::available_offsets() const {
  auto result = utc_offsets_;
  const auto footer = parse_footer(posix_footer_);
  result.push_back(footer.standard_offset);
  if (footer.has_daylight_time) result.push_back(footer.daylight_offset);
  std::sort(result.begin(), result.end());
  result.erase(std::unique(result.begin(), result.end()), result.end());
  return result;
}

}  // namespace liveroute::providers
