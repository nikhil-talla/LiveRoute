#pragma once

#include <filesystem>
#include <memory>
#include <string>
#include <vector>

#include "liveroute/domain/hours.hpp"
#include "liveroute/providers/seeded_hours_seed.hpp"
#include "liveroute/providers/tzif.hpp"

namespace liveroute::providers {

struct SeededHoursProviderConfig {
  std::filesystem::path seed_file_path;
  std::string seed_file_sha256;
  std::string tzdata_release;
  std::filesystem::path tzdata_zoneinfo_path;
};

class SeededHoursProvider final : public domain::PlaceHoursProvider {
 public:
  [[nodiscard]] static SeededHoursProvider load(SeededHoursProviderConfig config);

  domain::HoursLookupResult get_hours(domain::PlaceId place_id,
                                      domain::LocalDateRange date_range,
                                      domain::Deadline deadline,
                                      std::stop_token stop_token) override;

 private:
  struct Entry {
    SeededHoursPlace place;
    std::shared_ptr<const TzifData> zone;
  };

  SeededHoursProvider(std::string tzdata_release, std::vector<Entry> entries);

  std::string tzdata_release_;
  std::vector<Entry> entries_;
};

}  // namespace liveroute::providers
