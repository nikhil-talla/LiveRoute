#pragma once

#include <string>
#include <string_view>

namespace liveroute::providers {

[[nodiscard]] std::string sha256_hex(std::string_view input);

}  // namespace liveroute::providers
