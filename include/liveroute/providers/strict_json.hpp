#pragma once

#include <cstdint>
#include <map>
#include <string>
#include <string_view>
#include <variant>
#include <vector>

namespace liveroute::providers {

// The hours seed uses this deliberately small JSON subset: objects, arrays,
// strings, and signed safe integers. It rejects duplicate members and floats
// so canonical output is deterministic without a third-party parser.
class StrictJson {
 public:
  using Array = std::vector<StrictJson>;
  using Object = std::map<std::string, StrictJson, std::less<>>;

  [[nodiscard]] static StrictJson parse(std::string_view input);

  [[nodiscard]] bool is_integer() const noexcept;
  [[nodiscard]] bool is_string() const noexcept;
  [[nodiscard]] bool is_array() const noexcept;
  [[nodiscard]] bool is_object() const noexcept;
  [[nodiscard]] std::int64_t integer() const;
  [[nodiscard]] const std::string& string() const;
  [[nodiscard]] const Array& array() const;
  [[nodiscard]] const Object& object() const;
  [[nodiscard]] std::string canonicalize() const;

  // These constructors exist for the parser and provider-layer validators;
  // callers still obtain parsed external data through parse().
  explicit StrictJson(std::int64_t value);
  explicit StrictJson(std::string value);
  explicit StrictJson(Array value);
  explicit StrictJson(Object value);

 private:
  std::variant<std::int64_t, std::string, Array, Object> value_;
};

}  // namespace liveroute::providers
