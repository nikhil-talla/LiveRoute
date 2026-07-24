#include <string_view>

int main() {
  constexpr std::string_view protocol_version = "liveroute.v1";
  return protocol_version == "liveroute.v1" ? 0 : 1;
}
