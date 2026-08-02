#include "liveroute/routing/osrm_response.hpp"

#include <charconv>
#include <cmath>
#include <cstdint>
#include <limits>
#include <map>
#include <optional>
#include <stdexcept>
#include <string>
#include <utility>
#include <variant>
#include <vector>

namespace liveroute::routing {
namespace {

struct JsonValue {
  using Array = std::vector<JsonValue>;
  using Object = std::map<std::string, JsonValue, std::less<>>;
  using Value = std::variant<std::nullptr_t, bool, double, std::string, Array,
                             Object>;
  Value value;
};

class JsonParser {
 public:
  explicit JsonParser(std::string_view input) : input_(input) {}

  JsonValue parse() {
    skip_space();
    auto value = parse_value();
    skip_space();
    if (position_ != input_.size()) fail();
    return value;
  }

 private:
  [[noreturn]] static void fail() { throw std::invalid_argument("invalid JSON"); }

  void skip_space() {
    while (position_ < input_.size() &&
           (input_[position_] == ' ' || input_[position_] == '\n' ||
            input_[position_] == '\r' || input_[position_] == '\t')) {
      ++position_;
    }
  }

  bool consume(std::string_view token) {
    if (input_.substr(position_, token.size()) != token) return false;
    position_ += token.size();
    return true;
  }

  JsonValue parse_value() {
    if (position_ == input_.size()) fail();
    switch (input_[position_]) {
      case '{': return JsonValue{parse_object()};
      case '[': return JsonValue{parse_array()};
      case '"': return JsonValue{parse_string()};
      case 'n':
        if (!consume("null")) fail();
        return JsonValue{nullptr};
      case 't':
        if (!consume("true")) fail();
        return JsonValue{true};
      case 'f':
        if (!consume("false")) fail();
        return JsonValue{false};
      default: return JsonValue{parse_number()};
    }
  }

  JsonValue::Object parse_object() {
    ++position_;
    skip_space();
    JsonValue::Object object;
    if (position_ < input_.size() && input_[position_] == '}') {
      ++position_;
      return object;
    }
    while (true) {
      if (position_ == input_.size() || input_[position_] != '"') fail();
      auto key = parse_string();
      skip_space();
      if (position_ == input_.size() || input_[position_++] != ':') fail();
      skip_space();
      auto [iterator, inserted] = object.emplace(std::move(key), parse_value());
      static_cast<void>(iterator);
      if (!inserted) fail();
      skip_space();
      if (position_ == input_.size()) fail();
      const auto delimiter = input_[position_++];
      if (delimiter == '}') return object;
      if (delimiter != ',') fail();
      skip_space();
    }
  }

  JsonValue::Array parse_array() {
    ++position_;
    skip_space();
    JsonValue::Array array;
    if (position_ < input_.size() && input_[position_] == ']') {
      ++position_;
      return array;
    }
    while (true) {
      array.push_back(parse_value());
      skip_space();
      if (position_ == input_.size()) fail();
      const auto delimiter = input_[position_++];
      if (delimiter == ']') return array;
      if (delimiter != ',') fail();
      skip_space();
    }
  }

  static void append_utf8(std::string& output, std::uint32_t code_point) {
    if (code_point <= 0x7fU) {
      output.push_back(static_cast<char>(code_point));
    } else if (code_point <= 0x7ffU) {
      output.push_back(static_cast<char>(0xc0U | (code_point >> 6U)));
      output.push_back(static_cast<char>(0x80U | (code_point & 0x3fU)));
    } else if (code_point <= 0xffffU) {
      output.push_back(static_cast<char>(0xe0U | (code_point >> 12U)));
      output.push_back(static_cast<char>(0x80U | ((code_point >> 6U) & 0x3fU)));
      output.push_back(static_cast<char>(0x80U | (code_point & 0x3fU)));
    } else {
      output.push_back(static_cast<char>(0xf0U | (code_point >> 18U)));
      output.push_back(static_cast<char>(0x80U | ((code_point >> 12U) & 0x3fU)));
      output.push_back(static_cast<char>(0x80U | ((code_point >> 6U) & 0x3fU)));
      output.push_back(static_cast<char>(0x80U | (code_point & 0x3fU)));
    }
  }

  std::uint32_t parse_hex_quad() {
    if (input_.size() - position_ < 4) fail();
    std::uint32_t value = 0;
    for (int index = 0; index < 4; ++index) {
      const auto character = input_[position_++];
      value <<= 4U;
      if (character >= '0' && character <= '9') {
        value |= static_cast<std::uint32_t>(character - '0');
      } else if (character >= 'a' && character <= 'f') {
        value |= static_cast<std::uint32_t>(character - 'a' + 10);
      } else if (character >= 'A' && character <= 'F') {
        value |= static_cast<std::uint32_t>(character - 'A' + 10);
      } else {
        fail();
      }
    }
    return value;
  }

  std::string parse_string() {
    ++position_;
    std::string value;
    while (position_ < input_.size()) {
      const auto character = static_cast<unsigned char>(input_[position_++]);
      if (character == '"') return value;
      if (character < 0x20U) fail();
      if (character != '\\') {
        value.push_back(static_cast<char>(character));
        continue;
      }
      if (position_ == input_.size()) fail();
      switch (input_[position_++]) {
        case '"': value.push_back('"'); break;
        case '\\': value.push_back('\\'); break;
        case '/': value.push_back('/'); break;
        case 'b': value.push_back('\b'); break;
        case 'f': value.push_back('\f'); break;
        case 'n': value.push_back('\n'); break;
        case 'r': value.push_back('\r'); break;
        case 't': value.push_back('\t'); break;
        case 'u': {
          auto code_point = parse_hex_quad();
          if (code_point >= 0xd800U && code_point <= 0xdbffU) {
            if (input_.size() - position_ < 6 || input_[position_++] != '\\' ||
                input_[position_++] != 'u') fail();
            const auto low = parse_hex_quad();
            if (low < 0xdc00U || low > 0xdfffU) fail();
            code_point = 0x10000U + ((code_point - 0xd800U) << 10U) +
                         (low - 0xdc00U);
          } else if (code_point >= 0xdc00U && code_point <= 0xdfffU) {
            fail();
          }
          append_utf8(value, code_point);
          break;
        }
        default: fail();
      }
    }
    fail();
  }

  double parse_number() {
    const auto start = position_;
    if (input_[position_] == '-') ++position_;
    if (position_ == input_.size()) fail();
    if (input_[position_] == '0') {
      ++position_;
      if (position_ < input_.size() && input_[position_] >= '0' &&
          input_[position_] <= '9') fail();
    } else {
      if (input_[position_] < '1' || input_[position_] > '9') fail();
      while (position_ < input_.size() && input_[position_] >= '0' &&
             input_[position_] <= '9') ++position_;
    }
    if (position_ < input_.size() && input_[position_] == '.') {
      ++position_;
      const auto digits = position_;
      while (position_ < input_.size() && input_[position_] >= '0' &&
             input_[position_] <= '9') ++position_;
      if (digits == position_) fail();
    }
    if (position_ < input_.size() &&
        (input_[position_] == 'e' || input_[position_] == 'E')) {
      ++position_;
      if (position_ < input_.size() &&
          (input_[position_] == '+' || input_[position_] == '-')) ++position_;
      const auto digits = position_;
      while (position_ < input_.size() && input_[position_] >= '0' &&
             input_[position_] <= '9') ++position_;
      if (digits == position_) fail();
    }
    double value{};
    const auto [pointer, error] = std::from_chars(
        input_.data() + start, input_.data() + position_, value,
        std::chars_format::general);
    if (error != std::errc{} || pointer != input_.data() + position_ ||
        !std::isfinite(value)) fail();
    return value;
  }

  std::string_view input_;
  std::size_t position_{};
};

const JsonValue::Object* object(const JsonValue& value) {
  return std::get_if<JsonValue::Object>(&value.value);
}

const std::string* string_member(const JsonValue::Object& value,
                                 std::string_view key) {
  const auto iterator = value.find(key);
  if (iterator == value.end()) return nullptr;
  return std::get_if<std::string>(&iterator->second.value);
}

using OptionalNumberMatrix = std::vector<std::vector<std::optional<double>>>;

std::optional<OptionalNumberMatrix> matrix_member(
    const JsonValue::Object& value, std::string_view key,
    std::size_t expected_row_count, std::size_t expected_column_count) {
  const auto iterator = value.find(key);
  if (iterator == value.end()) return std::nullopt;
  const auto* rows = std::get_if<JsonValue::Array>(&iterator->second.value);
  if (rows == nullptr || rows->size() != expected_row_count) return std::nullopt;
  OptionalNumberMatrix result;
  result.reserve(rows->size());
  for (const auto& row_value : *rows) {
    const auto* row = std::get_if<JsonValue::Array>(&row_value.value);
    if (row == nullptr || row->size() != expected_column_count) {
      return std::nullopt;
    }
    auto& output_row = result.emplace_back();
    output_row.reserve(row->size());
    for (const auto& cell : *row) {
      if (std::holds_alternative<std::nullptr_t>(cell.value)) {
        output_row.emplace_back(std::nullopt);
      } else if (const auto* number = std::get_if<double>(&cell.value)) {
        output_row.emplace_back(*number);
      } else {
        return std::nullopt;
      }
    }
  }
  return result;
}

TravelTimeLookupResult provider_error(TravelTimeProviderError error) {
  return TravelTimeLookupResult{error};
}

}  // namespace

const domain::RouteEstimate& OsrmTableGrid::at(std::size_t row,
                                                std::size_t column) const {
  if (row >= row_count || column >= column_count) {
    throw std::out_of_range("OSRM table grid index out of range");
  }
  return estimates[row * column_count + column];
}

OsrmTableGridResult parse_osrm_table_response_grid(
    std::string_view response, std::size_t expected_row_count,
    std::size_t expected_column_count) {
  if (expected_row_count == 0 || expected_column_count == 0 ||
      expected_row_count > std::numeric_limits<std::size_t>::max() /
                               expected_column_count) {
    return TravelTimeProviderError::kProviderUnavailable;
  }
  try {
    const auto root = JsonParser{response}.parse();
    const auto* root_object = object(root);
    if (root_object == nullptr) {
      return TravelTimeProviderError::kProviderUnavailable;
    }
    const auto* code = string_member(*root_object, "code");
    if (code == nullptr) {
      return TravelTimeProviderError::kProviderUnavailable;
    }
    const auto entry_count = expected_row_count * expected_column_count;
    if (*code == "NoTable") {
      std::vector<domain::RouteEstimate> estimates(
          entry_count, domain::RouteEstimate{.duration = {},
                                              .distance_meters = 0,
                                              .reachable = false});
      if (expected_row_count == expected_column_count) {
        for (std::size_t index = 0; index < expected_row_count; ++index) {
          estimates[index * expected_column_count + index].reachable = true;
        }
      }
      return OsrmTableGrid{expected_row_count, expected_column_count,
                           std::move(estimates), true};
    }
    if (*code == "NoSegment") {
      return TravelTimeProviderError::kProviderUnavailable;
    }
    if (*code == "TooBig") {
      return TravelTimeProviderError::kMatrixTooLarge;
    }
    if (*code == "InvalidUrl" || *code == "InvalidService" ||
        *code == "InvalidVersion" || *code == "InvalidOptions" ||
        *code == "InvalidQuery" || *code == "InvalidValue" ||
        *code == "NotImplemented") {
      return TravelTimeProviderError::kInternal;
    }
    if (*code != "Ok") {
      return TravelTimeProviderError::kInternal;
    }
    const auto durations = matrix_member(*root_object, "durations",
                                         expected_row_count,
                                         expected_column_count);
    const auto distances = matrix_member(*root_object, "distances",
                                         expected_row_count,
                                         expected_column_count);
    if (!durations.has_value() || !distances.has_value()) {
      return TravelTimeProviderError::kProviderUnavailable;
    }
    std::vector<domain::RouteEstimate> estimates;
    estimates.reserve(entry_count);
    constexpr auto max_uint32 =
        static_cast<double>(std::numeric_limits<std::uint32_t>::max());
    for (std::size_t row = 0; row < expected_row_count; ++row) {
      for (std::size_t column = 0; column < expected_column_count; ++column) {
        const auto duration = (*durations)[row][column];
        const auto distance = (*distances)[row][column];
        if (duration.has_value() != distance.has_value()) {
          return TravelTimeProviderError::kProviderUnavailable;
        }
        if (!duration.has_value()) {
          estimates.push_back({std::chrono::seconds::zero(), 0, false});
          continue;
        }
        if (*duration < 0.0 || *distance < 0.0 ||
            std::ceil(*duration) > max_uint32 ||
            std::ceil(*distance) > max_uint32) {
          return TravelTimeProviderError::kProviderUnavailable;
        }
        estimates.push_back({
            std::chrono::seconds{static_cast<std::uint32_t>(std::ceil(*duration))},
            static_cast<std::uint32_t>(std::ceil(*distance)), true});
      }
    }
    return OsrmTableGrid{expected_row_count, expected_column_count,
                         std::move(estimates), false};
  } catch (const std::exception&) {
    return TravelTimeProviderError::kProviderUnavailable;
  }
}

TravelTimeLookupResult parse_osrm_table_response(
    std::string_view response, std::size_t expected_location_count) {
  const auto parsed = parse_osrm_table_response_grid(
      response, expected_location_count, expected_location_count);
  if (const auto* error = std::get_if<TravelTimeProviderError>(&parsed)) {
    return provider_error(*error);
  }
  const auto& grid = std::get<OsrmTableGrid>(parsed);
  return TravelTimeLookupResult{domain::TravelTimeMatrix{
      grid.row_count, grid.estimates}};
}

bool is_recognized_osrm_error_response(std::string_view response) {
  try {
    const auto root = JsonParser{response}.parse();
    const auto* root_object = object(root);
    if (root_object == nullptr) return false;
    const auto* code = string_member(*root_object, "code");
    if (code == nullptr) return false;
    return *code == "NoTable" || *code == "NoSegment" || *code == "TooBig" ||
           *code == "InvalidUrl" || *code == "InvalidService" ||
           *code == "InvalidVersion" || *code == "InvalidOptions" ||
           *code == "InvalidQuery" || *code == "InvalidValue" ||
           *code == "NotImplemented";
  } catch (const std::exception&) {
    return false;
  }
}

}  // namespace liveroute::routing
