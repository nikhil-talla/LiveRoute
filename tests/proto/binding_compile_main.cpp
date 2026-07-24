#include "liveroute/v1/planner.grpc.pb.h"

int main() {
  liveroute::v1::PlannerStreamRequest request;
  request.set_request_id("00000000-0000-0000-0000-000000000000");
  return request.request_id().empty() ? 1 : 0;
}
