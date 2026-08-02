#include "liveroute/load/websocket_runner.hpp"

int main() {
  return liveroute::load::websocket_transport_available() ? 0 : 1;
}
