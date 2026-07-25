#include <iostream>
#include <stdexcept>

#include "liveroute/providers/strict_json.hpp"

int main() {
  const auto parsed = liveroute::providers::StrictJson::parse(
      "{\"z\":1,\"a\":[\"x\",\"\\u00e9\"],\"n\":-2}");
  if (parsed.canonicalize() != "{\"a\":[\"x\",\"é\"],\"n\":-2,\"z\":1}") {
    return 1;
  }
  try {
    [[maybe_unused]] const auto duplicate =
        liveroute::providers::StrictJson::parse("{\"a\":1,\"a\":2}");
    return 1;
  } catch (const std::invalid_argument&) {
  }
  try {
    [[maybe_unused]] const auto surrogate =
        liveroute::providers::StrictJson::parse("\"\\ud800\"");
    return 1;
  } catch (const std::invalid_argument&) {
  }
  return 0;
}
