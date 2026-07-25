#include "liveroute/providers/seeded_hours_seed.hpp"

#include <cstdint>
#include <fstream>
#include <iterator>
#include <stdexcept>
#include <string_view>

#include "liveroute/domain/hours.hpp"
#include "liveroute/providers/sha256.hpp"
#include "liveroute/providers/strict_json.hpp"

namespace liveroute::providers {
namespace {

using Object = StrictJson::Object;

[[nodiscard]] const StrictJson& require_member(const Object& object,
                                                std::string_view key) {
  const auto iterator = object.find(key);
  if (iterator == object.end()) {
    throw std::invalid_argument("hours seed is missing required member");
  }
  return iterator->second;
}

void require_exact_members(const Object& object,
                           std::initializer_list<std::string_view> keys) {
  if (object.size() != keys.size()) {
    throw std::invalid_argument("hours seed has unexpected object members");
  }
  for (const auto key : keys) {
    static_cast<void>(require_member(object, key));
  }
}

[[nodiscard]] const Object& require_object(const StrictJson& value) {
  if (!value.is_object()) throw std::invalid_argument("hours seed value must be object");
  return value.object();
}

[[nodiscard]] const StrictJson::Array& require_array(const StrictJson& value) {
  if (!value.is_array()) throw std::invalid_argument("hours seed value must be array");
  return value.array();
}

[[nodiscard]] const std::string& require_string(const StrictJson& value) {
  if (!value.is_string()) throw std::invalid_argument("hours seed value must be string");
  return value.string();
}

[[nodiscard]] std::int64_t require_integer(const StrictJson& value) {
  if (!value.is_integer()) throw std::invalid_argument("hours seed value must be integer");
  return value.integer();
}

void validate_time(std::string_view time) {
  if (time.size() != 8 || time[2] != ':' || time[5] != ':' ||
      time[0] < '0' || time[0] > '2' || time[1] < '0' || time[1] > '9' ||
      time[3] < '0' || time[3] > '5' || time[4] < '0' || time[4] > '9' ||
      time[6] < '0' || time[6] > '5' || time[7] < '0' || time[7] > '9' ||
      (time[0] == '2' && time[1] > '3')) {
    throw std::invalid_argument("hours seed local time is invalid");
  }
}

[[nodiscard]] std::vector<SeededHoursInterval> parse_intervals(
    const StrictJson::Array& intervals, bool exception) {
  std::string previous_open;
  std::int64_t previous_close = -1;
  std::vector<SeededHoursInterval> result;
  result.reserve(intervals.size());
  for (const auto& entry : intervals) {
    const auto& object = require_object(entry);
    if (exception) {
      if (object.size() < 3 || object.size() > 5) {
        throw std::invalid_argument("hours seed exception interval members are invalid");
      }
      for (const auto& [key, ignored] : object) {
        static_cast<void>(ignored);
        if (key != "opens_at_local" && key != "closes_at_local" &&
            key != "closes_day_offset" && key != "opens_utc_offset_seconds" &&
            key != "closes_utc_offset_seconds") {
          throw std::invalid_argument("hours seed exception interval member is unknown");
        }
      }
    } else {
      require_exact_members(object, {"opens_at_local", "closes_at_local", "closes_day_offset"});
    }
    const auto& open = require_string(require_member(object, "opens_at_local"));
    const auto& close = require_string(require_member(object, "closes_at_local"));
    validate_time(open); validate_time(close);
    const auto offset = require_integer(require_member(object, "closes_day_offset"));
    if (offset < 0 || offset > 1) throw std::invalid_argument("hours seed close offset is invalid");
    const auto seconds = [](std::string_view value) {
      return ((value[0] - '0') * 10 + value[1] - '0') * 3600 +
             ((value[3] - '0') * 10 + value[4] - '0') * 60 +
             (value[6] - '0') * 10 + value[7] - '0';
    };
    const auto open_seconds = seconds(open);
    const auto close_seconds = seconds(close) + offset * 86400;
    if (close_seconds <= open_seconds || close_seconds - open_seconds > 86400 ||
        (!previous_open.empty() && open <= previous_open) ||
        (previous_close >= 0 && open_seconds < previous_close)) {
      throw std::invalid_argument("hours seed intervals are not normalized");
    }
    previous_open = open;
    previous_close = close_seconds;
    const auto read_optional_offset = [&object](std::string_view key) {
      const auto iterator = object.find(key);
      if (iterator == object.end()) return std::optional<std::int32_t>{};
      const auto value = require_integer(iterator->second);
      if (value < -64800 || value > 64800) {
        throw std::invalid_argument("hours seed UTC offset is invalid");
      }
      return std::optional<std::int32_t>{static_cast<std::int32_t>(value)};
    };
    result.push_back({.opens_at_local_seconds = static_cast<std::uint32_t>(open_seconds),
                      .closes_at_local_seconds = static_cast<std::uint32_t>(close_seconds - offset * 86400),
                      .closes_day_offset = static_cast<std::uint8_t>(offset),
                      .opens_utc_offset_seconds = read_optional_offset("opens_utc_offset_seconds"),
                      .closes_utc_offset_seconds = read_optional_offset("closes_utc_offset_seconds")});
  }
  return result;
}

[[nodiscard]] SeededHoursPlace parse_place(const StrictJson& value,
                                           std::string_view tzdata_release) {
  const auto& place = require_object(value);
  require_exact_members(place, {"place_id", "time_zone_name", "weekly", "exceptions"});
  if (require_string(require_member(place, "place_id")).empty() ||
      require_string(require_member(place, "time_zone_name")).empty()) {
    throw std::invalid_argument("hours seed place identity is empty");
  }
  const auto& weekly = require_object(require_member(place, "weekly"));
  constexpr std::array<std::string_view, 7> weekdays = {
      "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"};
  if (weekly.size() != weekdays.size()) throw std::invalid_argument("hours seed weekly shape is invalid");
  SeededHoursPlace result{.place_id = domain::PlaceId{require_string(require_member(place, "place_id"))},
                          .time_zone_name = require_string(require_member(place, "time_zone_name")),
                          .source_version = "seed-v1:" + sha256_hex(
                              "liveroute-hours-seed-v1\n" + std::string{tzdata_release} + "\n" +
                              value.canonicalize()),
                          .weekly = {},
                          .exceptions = {}};
  for (std::size_t index = 0; index < weekdays.size(); ++index) {
    result.weekly[index] = parse_intervals(require_array(require_member(weekly, weekdays[index])), false);
  }
  const auto& exceptions = require_array(require_member(place, "exceptions"));
  std::string previous_date;
  for (const auto& exception : exceptions) {
    const auto& object = require_object(exception);
    require_exact_members(object, {"local_date", "intervals"});
    const auto& date = require_string(require_member(object, "local_date"));
    if (date.size() != 10 || date[4] != '-' || date[7] != '-' ||
        date[0] < '0' || date[0] > '9' || date[1] < '0' || date[1] > '9' ||
        date[2] < '0' || date[2] > '9' || date[3] < '0' || date[3] > '9' ||
        date[5] < '0' || date[5] > '9' || date[6] < '0' || date[6] > '9' ||
        date[8] < '0' || date[8] > '9' || date[9] < '0' || date[9] > '9' ||
        !domain::LocalDate::create(
            (date[0] - '0') * 1000 + (date[1] - '0') * 100 +
                (date[2] - '0') * 10 + date[3] - '0',
            static_cast<unsigned>((date[5] - '0') * 10 + date[6] - '0'),
            static_cast<unsigned>((date[8] - '0') * 10 + date[9] - '0'))
             .has_value() ||
        date <= previous_date) {
      throw std::invalid_argument("hours seed exceptions are not sorted");
    }
    previous_date = date;
    const auto parsed_date = domain::LocalDate::create(
        (date[0] - '0') * 1000 + (date[1] - '0') * 100 +
            (date[2] - '0') * 10 + date[3] - '0',
        static_cast<unsigned>((date[5] - '0') * 10 + date[6] - '0'),
        static_cast<unsigned>((date[8] - '0') * 10 + date[9] - '0'));
    result.exceptions.push_back({.local_date = *parsed_date,
                                 .intervals = parse_intervals(
                                     require_array(require_member(object, "intervals")), true)});
  }
  return result;
}

}  // namespace

SeededHoursSeed SeededHoursSeed::load(const std::filesystem::path& seed_path,
                                      std::string expected_sha256,
                                      std::string expected_tzdata_release) {
  std::ifstream input(seed_path, std::ios::binary);
  if (!input) throw std::invalid_argument("cannot open hours seed");
  const std::string bytes{std::istreambuf_iterator<char>{input}, {}};
  if (sha256_hex(bytes) != expected_sha256) {
    throw std::invalid_argument("hours seed digest does not match configuration");
  }
  const auto root = StrictJson::parse(bytes);
  const auto& object = require_object(root);
  require_exact_members(object, {"schema_version", "tzdata_release", "places"});
  if (require_integer(require_member(object, "schema_version")) != 1 ||
      require_string(require_member(object, "tzdata_release")) != expected_tzdata_release) {
    throw std::invalid_argument("hours seed version does not match configuration");
  }
  SeededHoursSeed result;
  std::string previous_place;
  for (const auto& place : require_array(require_member(object, "places"))) {
    const auto parsed_place = parse_place(place, expected_tzdata_release);
    const auto& place_id = parsed_place.place_id.value;
    if (!previous_place.empty() && place_id <= previous_place) {
      throw std::invalid_argument("hours seed places are not strictly sorted");
    }
    previous_place = place_id;
    result.places_.push_back(parsed_place);
  }
  if (result.places_.empty()) throw std::invalid_argument("hours seed has no places");
  return result;
}

}  // namespace liveroute::providers
