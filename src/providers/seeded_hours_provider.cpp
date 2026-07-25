#include "liveroute/providers/seeded_hours_provider.hpp"

#include <algorithm>
#include <chrono>
#include <fstream>
#include <limits>
#include <stdexcept>

namespace liveroute::providers {
namespace {

[[nodiscard]] std::int64_t local_seconds(domain::LocalDate date,
                                         std::uint32_t seconds) {
  return std::chrono::duration_cast<std::chrono::seconds>(
             date.as_sys_days().time_since_epoch()).count() + seconds;
}

[[nodiscard]] domain::LocalDate add_days(domain::LocalDate date, int days) {
  const auto value = std::chrono::year_month_day{date.as_sys_days() +
                                                  std::chrono::days{days}};
  const auto result = domain::LocalDate::create(static_cast<int>(value.year()),
                                                unsigned{value.month()}, unsigned{value.day()});
  if (!result) throw std::invalid_argument("seed interval exceeds V1 date range");
  return *result;
}

[[nodiscard]] std::vector<std::int64_t> utc_candidates(
    const TzifData& zone, std::int64_t local, std::optional<std::int32_t> required) {
  std::vector<std::int64_t> result;
  for (const auto offset : zone.available_offsets()) {
    if (required && offset != *required) continue;
    const auto utc = local - offset;
    if (zone.offset_at_utc(utc) == offset) result.push_back(utc);
  }
  std::sort(result.begin(), result.end());
  result.erase(std::unique(result.begin(), result.end()), result.end());
  return result;
}

[[nodiscard]] std::size_t weekday_index(domain::LocalDate date) {
  return (std::chrono::weekday{date.as_sys_days()}.c_encoding() + 6U) % 7U;
}

[[nodiscard]] const std::vector<SeededHoursInterval>& intervals_for(
    const SeededHoursPlace& place, domain::LocalDate date) {
  const auto exception = std::lower_bound(
      place.exceptions.begin(), place.exceptions.end(), date,
      [](const SeededHoursException& value, domain::LocalDate key) {
        return value.local_date < key;
      });
  if (exception != place.exceptions.end() && exception->local_date == date) {
    return exception->intervals;
  }
  return place.weekly[weekday_index(date)];
}

[[nodiscard]] const SeededHoursException* exception_for(
    const SeededHoursPlace& place, domain::LocalDate date) {
  const auto exception = std::lower_bound(
      place.exceptions.begin(), place.exceptions.end(), date,
      [](const SeededHoursException& value, domain::LocalDate key) {
        return value.local_date < key;
      });
  if (exception != place.exceptions.end() && exception->local_date == date) {
    return &*exception;
  }
  return nullptr;
}

[[nodiscard]] std::optional<domain::LocalDate> try_add_days(
    domain::LocalDate date, int days) {
  const auto value = std::chrono::year_month_day{date.as_sys_days() +
                                                  std::chrono::days{days}};
  return domain::LocalDate::create(static_cast<int>(value.year()),
                                   unsigned{value.month()}, unsigned{value.day()});
}

void validate_exception(const SeededHoursException& exception, const TzifData& zone) {
  for (const auto& interval : exception.intervals) {
    const auto open = utc_candidates(
        zone, local_seconds(exception.local_date, interval.opens_at_local_seconds),
        interval.opens_utc_offset_seconds);
    const auto close = utc_candidates(
        zone, local_seconds(add_days(exception.local_date, interval.closes_day_offset),
        interval.closes_at_local_seconds), interval.closes_utc_offset_seconds);
    if (open.size() != 1 || close.size() != 1 || open.front() >= close.front()) {
      throw std::invalid_argument("seed exception endpoint is invalid in TZif zone");
    }
  }
}

void validate_recurring(const SeededHoursPlace& place, const TzifData& zone) {
  const auto first_opening_date = domain::LocalDate::create(1970, 1, 1);
  const auto last_opening_date = domain::LocalDate::create(9999, 12, 30);
  if (!first_opening_date || !last_opening_date) {
    throw std::logic_error("V1 hours validation date bounds are invalid");
  }
  for (const auto& discontinuity : zone.discontinuities_for_years(1970, 9999)) {
    const auto date_time = std::chrono::sys_seconds{
        std::chrono::seconds{discontinuity.first_local_second}};
    const auto date_value = std::chrono::year_month_day{
        std::chrono::floor<std::chrono::days>(date_time)};
    const auto date = domain::LocalDate::create(static_cast<int>(date_value.year()),
                                                 unsigned{date_value.month()}, unsigned{date_value.day()});
    if (!date) throw std::invalid_argument("TZif transition is outside V1 dates");
    for (const int opening_shift : {0, -1}) {
      const auto opening_date = try_add_days(*date, opening_shift);
      if (!opening_date || *opening_date < *first_opening_date ||
          *opening_date > *last_opening_date ||
          exception_for(place, *opening_date) != nullptr) {
        continue;
      }
      for (const auto& interval : intervals_for(place, *opening_date)) {
        const auto open = local_seconds(*opening_date, interval.opens_at_local_seconds);
        const auto close = local_seconds(add_days(*opening_date, interval.closes_day_offset),
                                         interval.closes_at_local_seconds);
        const auto intersects = [&discontinuity](std::int64_t endpoint) {
          return endpoint >= discontinuity.first_local_second &&
                 endpoint < discontinuity.last_local_second;
        };
        if (intersects(open) || intersects(close)) {
          const auto endpoint = intersects(open) ? open : close;
          throw std::invalid_argument(
              "seed recurring endpoint intersects TZif gap/fold for " +
              place.place_id.value + " at local second " + std::to_string(endpoint) +
              " in [" + std::to_string(discontinuity.first_local_second) + ", " +
              std::to_string(discontinuity.last_local_second) + ")");
        }
      }
    }
  }
}

void validate_us_time_zone(const std::filesystem::path& zoneinfo_path,
                           const std::string& time_zone_name) {
  std::ifstream input(zoneinfo_path / "zone1970.tab");
  if (!input) throw std::invalid_argument("cannot open pinned zone1970.tab");
  std::string line;
  while (std::getline(input, line)) {
    if (line.empty() || line.front() == '#') continue;
    const auto first_tab = line.find('\t');
    if (first_tab == std::string::npos) continue;
    const auto second_tab = line.find('\t', first_tab + 1);
    if (second_tab == std::string::npos) continue;
    const auto third_tab = line.find('\t', second_tab + 1);
    const auto zone = line.substr(second_tab + 1,
                                  third_tab == std::string::npos
                                      ? std::string::npos
                                      : third_tab - second_tab - 1);
    if (zone != time_zone_name) continue;
    const auto countries = line.substr(0, first_tab);
    std::size_t start = 0;
    while (start <= countries.size()) {
      const auto end = countries.find(',', start);
      if (countries.substr(start, end - start) == "US") return;
      if (end == std::string::npos) break;
      start = end + 1;
    }
    break;
  }
  throw std::invalid_argument("seed time zone is not a pinned United States zone");
}

}  // namespace

SeededHoursProvider::SeededHoursProvider(std::string tzdata_release,
                                         std::vector<Entry> entries)
    : tzdata_release_(std::move(tzdata_release)), entries_(std::move(entries)) {}

SeededHoursProvider SeededHoursProvider::load(SeededHoursProviderConfig config) {
  const auto seed = SeededHoursSeed::load(config.seed_file_path, config.seed_file_sha256,
                                          config.tzdata_release);
  std::vector<Entry> entries;
  std::vector<std::pair<std::string, std::shared_ptr<const TzifData>>> zones;
  entries.reserve(seed.places().size());
  for (const auto& place : seed.places()) {
    if (place.time_zone_name.empty() || place.time_zone_name.front() == '/' ||
        place.time_zone_name.find("..") != std::string::npos) {
      throw std::invalid_argument("seed time zone path is invalid");
    }
    validate_us_time_zone(config.tzdata_zoneinfo_path, place.time_zone_name);
    auto zone = std::find_if(zones.begin(), zones.end(), [&place](const auto& value) {
      return value.first == place.time_zone_name;
    });
    if (zone == zones.end()) {
      zone = zones.emplace(
          zones.end(), place.time_zone_name,
          std::make_shared<TzifData>(
              TzifData::load(config.tzdata_zoneinfo_path / place.time_zone_name)));
    }
    validate_recurring(place, *zone->second);
    for (const auto& exception : place.exceptions) {
      validate_exception(exception, *zone->second);
    }
    entries.push_back({place, zone->second});
  }
  return SeededHoursProvider{std::move(config.tzdata_release), std::move(entries)};
}

domain::HoursLookupResult SeededHoursProvider::get_hours(
    domain::PlaceId place_id, domain::LocalDateRange date_range,
    domain::Deadline deadline, std::stop_token stop_token) {
  if (stop_token.stop_requested()) return domain::HoursLookupResult{domain::HoursProviderError::kCancelled};
  if (std::chrono::steady_clock::now() >= deadline) return domain::HoursLookupResult{domain::HoursProviderError::kDeadlineExceeded};
  const auto entry = std::find_if(entries_.begin(), entries_.end(), [&](const Entry& value) {
    return value.place.place_id == place_id;
  });
  if (entry == entries_.end()) return domain::HoursLookupResult{domain::HoursProviderError::kNotFound};
  std::vector<domain::TimeWindow> windows;
  for (auto day = date_range.start_date_inclusive(); day < date_range.end_date_exclusive(); day = add_days(day, 1)) {
    if (stop_token.stop_requested()) {
      return domain::HoursLookupResult{domain::HoursProviderError::kCancelled};
    }
    if (std::chrono::steady_clock::now() >= deadline) {
      return domain::HoursLookupResult{domain::HoursProviderError::kDeadlineExceeded};
    }
    for (const auto& interval : intervals_for(entry->place, day)) {
      const auto opens = utc_candidates(*entry->zone, local_seconds(day, interval.opens_at_local_seconds),
                                        interval.opens_utc_offset_seconds);
      const auto closes = utc_candidates(*entry->zone,
          local_seconds(add_days(day, interval.closes_day_offset), interval.closes_at_local_seconds),
          interval.closes_utc_offset_seconds);
      if (opens.size() != 1 || closes.size() != 1 || opens.front() >= closes.front()) {
        return domain::HoursLookupResult{domain::HoursProviderError::kInvalidSource};
      }
      if (opens.front() > std::numeric_limits<std::int64_t>::max() / 1000 ||
          closes.front() > std::numeric_limits<std::int64_t>::max() / 1000) {
        return domain::HoursLookupResult{domain::HoursProviderError::kInvalidSource};
      }
      windows.push_back({domain::UnixTimeMilliseconds{opens.front() * 1000},
                         domain::UnixTimeMilliseconds{closes.front() * 1000}});
    }
  }
  std::sort(windows.begin(), windows.end(), [](const auto& left, const auto& right) {
    return left.opens_at < right.opens_at;
  });
  std::vector<domain::TimeWindow> normalized;
  for (const auto& window : windows) {
    if (!normalized.empty() && window.opens_at <= normalized.back().closes_at) {
      if (window.closes_at > normalized.back().closes_at) normalized.back().closes_at = window.closes_at;
    } else {
      normalized.push_back(window);
    }
  }
  return domain::HoursLookupResult{domain::HoursInfo{.place_id = entry->place.place_id,
                             .time_zone_name = entry->place.time_zone_name,
                             .covered_range = date_range,
                             .open_windows = std::move(normalized),
                             .source_version = entry->place.source_version,
                             .tzdata_release = tzdata_release_}};
}

}  // namespace liveroute::providers
