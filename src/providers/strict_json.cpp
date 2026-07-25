#include "liveroute/providers/strict_json.hpp"

#include <charconv>
#include <cstddef>
#include <stdexcept>
#include <utility>

namespace liveroute::providers {
namespace {

class Parser {
 public:
  explicit Parser(std::string_view input) : input_(input) {}

  StrictJson parse() {
    skip_space();
    auto value = parse_value();
    skip_space();
    if (position_ != input_.size()) {
      fail("trailing JSON data");
    }
    return value;
  }

 private:
  [[noreturn]] void fail(const char* message) const {
    throw std::invalid_argument(message);
  }

  void skip_space() {
    while (position_ < input_.size() &&
           (input_[position_] == ' ' || input_[position_] == '\n' ||
            input_[position_] == '\r' || input_[position_] == '\t')) {
      ++position_;
    }
  }

  StrictJson parse_value() {
    if (position_ == input_.size()) {
      fail("missing JSON value");
    }
    switch (input_[position_]) {
      case '{':
        return StrictJson{parse_object()};
      case '[':
        return StrictJson{parse_array()};
      case '"':
        return StrictJson{parse_string()};
      default:
        return StrictJson{parse_integer()};
    }
  }

  StrictJson::Object parse_object() {
    ++position_;
    skip_space();
    StrictJson::Object object;
    if (position_ < input_.size() && input_[position_] == '}') {
      ++position_;
      return object;
    }
    while (true) {
      if (position_ == input_.size() || input_[position_] != '"') {
        fail("JSON object key must be a string");
      }
      auto key = parse_string();
      skip_space();
      if (position_ == input_.size() || input_[position_++] != ':') {
        fail("JSON object key lacks colon");
      }
      skip_space();
      auto [iterator, inserted] = object.emplace(std::move(key), parse_value());
      static_cast<void>(iterator);
      if (!inserted) {
        fail("duplicate JSON object key");
      }
      skip_space();
      if (position_ == input_.size()) {
        fail("unterminated JSON object");
      }
      const auto delimiter = input_[position_++];
      if (delimiter == '}') {
        return object;
      }
      if (delimiter != ',') {
        fail("invalid JSON object delimiter");
      }
      skip_space();
    }
  }

  StrictJson::Array parse_array() {
    ++position_;
    skip_space();
    StrictJson::Array array;
    if (position_ < input_.size() && input_[position_] == ']') {
      ++position_;
      return array;
    }
    while (true) {
      array.push_back(parse_value());
      skip_space();
      if (position_ == input_.size()) {
        fail("unterminated JSON array");
      }
      const auto delimiter = input_[position_++];
      if (delimiter == ']') {
        return array;
      }
      if (delimiter != ',') {
        fail("invalid JSON array delimiter");
      }
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

  [[nodiscard]] std::uint32_t parse_hex_quad() {
    if (input_.size() - position_ < 4) {
      fail("truncated JSON Unicode escape");
    }
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
        fail("invalid JSON Unicode escape");
      }
    }
    return value;
  }

  [[nodiscard]] std::string parse_string() {
    ++position_;
    std::string value;
    while (position_ < input_.size()) {
      const auto character = static_cast<unsigned char>(input_[position_++]);
      if (character == '"') {
        return value;
      }
      if (character < 0x20U) {
        fail("unescaped JSON control character");
      }
      if (character != '\\') {
        value.push_back(static_cast<char>(character));
        continue;
      }
      if (position_ == input_.size()) {
        fail("truncated JSON escape");
      }
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
                input_[position_++] != 'u') {
              fail("unpaired JSON high surrogate");
            }
            const auto low = parse_hex_quad();
            if (low < 0xdc00U || low > 0xdfffU) {
              fail("unpaired JSON high surrogate");
            }
            code_point = 0x10000U + ((code_point - 0xd800U) << 10U) +
                         (low - 0xdc00U);
          } else if (code_point >= 0xdc00U && code_point <= 0xdfffU) {
            fail("unpaired JSON low surrogate");
          }
          append_utf8(value, code_point);
          break;
        }
        default: fail("invalid JSON escape");
      }
    }
    fail("unterminated JSON string");
  }

  [[nodiscard]] std::int64_t parse_integer() {
    const auto start = position_;
    if (input_[position_] == '-') {
      ++position_;
    }
    if (position_ == input_.size() || input_[position_] < '0' ||
        input_[position_] > '9') {
      fail("JSON value must be an integer");
    }
    if (input_[position_] == '0') {
      ++position_;
      if (position_ < input_.size() && input_[position_] >= '0' &&
          input_[position_] <= '9') {
        fail("JSON integer has a leading zero");
      }
    } else {
      while (position_ < input_.size() && input_[position_] >= '0' &&
             input_[position_] <= '9') {
        ++position_;
      }
    }
    if (position_ < input_.size() && (input_[position_] == '.' ||
                                      input_[position_] == 'e' ||
                                      input_[position_] == 'E')) {
      fail("JSON value must be an integer");
    }
    std::int64_t value{};
    const auto [pointer, error] = std::from_chars(input_.data() + start,
                                                   input_.data() + position_, value);
    if (error != std::errc{} || pointer != input_.data() + position_) {
      fail("JSON integer is out of range");
    }
    return value;
  }

  std::string_view input_;
  std::size_t position_{};
};

void append_canonical_string(std::string& output, std::string_view input) {
  output.push_back('"');
  constexpr char hex[] = "0123456789abcdef";
  for (const auto character : input) {
    const auto byte = static_cast<unsigned char>(character);
    switch (byte) {
      case '"': output.append("\\\""); break;
      case '\\': output.append("\\\\"); break;
      case '\b': output.append("\\b"); break;
      case '\f': output.append("\\f"); break;
      case '\n': output.append("\\n"); break;
      case '\r': output.append("\\r"); break;
      case '\t': output.append("\\t"); break;
      default:
        if (byte < 0x20U) {
          output.append("\\u00");
          output.push_back(hex[byte >> 4U]);
          output.push_back(hex[byte & 0x0fU]);
        } else {
          output.push_back(static_cast<char>(byte));
        }
    }
  }
  output.push_back('"');
}

void append_canonical(std::string& output, const StrictJson& value) {
  if (value.is_integer()) {
    output.append(std::to_string(value.integer()));
  } else if (value.is_string()) {
    append_canonical_string(output, value.string());
  } else if (value.is_array()) {
    output.push_back('[');
    bool first = true;
    for (const auto& entry : value.array()) {
      if (!first) output.push_back(',');
      first = false;
      append_canonical(output, entry);
    }
    output.push_back(']');
  } else {
    output.push_back('{');
    bool first = true;
    for (const auto& [key, entry] : value.object()) {
      if (!first) output.push_back(',');
      first = false;
      append_canonical_string(output, key);
      output.push_back(':');
      append_canonical(output, entry);
    }
    output.push_back('}');
  }
}

}  // namespace

StrictJson::StrictJson(std::int64_t value) : value_(value) {}
StrictJson::StrictJson(std::string value) : value_(std::move(value)) {}
StrictJson::StrictJson(Array value) : value_(std::move(value)) {}
StrictJson::StrictJson(Object value) : value_(std::move(value)) {}

StrictJson StrictJson::parse(std::string_view input) { return Parser{input}.parse(); }
bool StrictJson::is_integer() const noexcept { return std::holds_alternative<std::int64_t>(value_); }
bool StrictJson::is_string() const noexcept { return std::holds_alternative<std::string>(value_); }
bool StrictJson::is_array() const noexcept { return std::holds_alternative<Array>(value_); }
bool StrictJson::is_object() const noexcept { return std::holds_alternative<Object>(value_); }
std::int64_t StrictJson::integer() const { return std::get<std::int64_t>(value_); }
const std::string& StrictJson::string() const { return std::get<std::string>(value_); }
const StrictJson::Array& StrictJson::array() const { return std::get<Array>(value_); }
const StrictJson::Object& StrictJson::object() const { return std::get<Object>(value_); }
std::string StrictJson::canonicalize() const { std::string output; append_canonical(output, *this); return output; }

}  // namespace liveroute::providers
