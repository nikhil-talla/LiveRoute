#include <iostream>
#include <string_view>

namespace {

constexpr std::string_view kServiceName = "liveroute-planner";

}  // namespace

int main(int argc, char* argv[]) {
  if (argc == 2 && std::string_view{argv[1]} == "--self-check") {
    std::cout << kServiceName << " build skeleton is healthy\n";
    return 0;
  }

  std::cerr << kServiceName
            << " has no serving transport yet; use --self-check during the "
               "container-skeleton gate.\n";
  return 1;
}
