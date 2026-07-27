#include <chrono>
#include <iostream>
#include <memory>
#include <string>

#include <grpcpp/create_channel.h>
#include <grpcpp/security/credentials.h>

#include "liveroute/transport/grpc_planner_service.hpp"
#include "liveroute/v1/planner.grpc.pb.h"

int main(int argc, char* argv[]) {
  const std::string target =
      argc == 2 ? argv[1] : "127.0.0.1:50051";
  auto channel = ::grpc::CreateChannel(
      target, ::grpc::InsecureChannelCredentials());
  if (!channel->WaitForConnected(
          std::chrono::system_clock::now() +
          std::chrono::seconds{10})) {
    std::cerr << "planner connection timed out\n";
    return 1;
  }
  auto stub =
      ::liveroute::v1::LiveRoutePlanner::NewStub(std::move(channel));
  ::grpc::ClientContext context;
  auto stream = stub->PlanTrips(&context);

  ::liveroute::v1::PlannerStreamRequest request;
  request.set_request_id("00000000-0000-0000-0000-000000000001");
  auto* open = request.mutable_open_stream();
  open->set_backend_instance_id("liveroute-cli");
  open->set_protocol_version("liveroute.v1");
  for (const auto& capability :
       liveroute::transport::required_v1_capabilities()) {
    open->add_capabilities(capability);
  }
  if (!stream->Write(request)) {
    std::cerr << "failed to write OpenStream\n";
    return 1;
  }
  ::liveroute::v1::PlannerStreamResponse response;
  if (!stream->Read(&response) || !response.has_stream_ready() ||
      response.stream_ready().status() !=
          ::liveroute::v1::STATUS_CODE_OK) {
    std::cerr << "planner did not accept the stream\n";
    return 1;
  }

  request.Clear();
  request.set_request_id("00000000-0000-0000-0000-000000000002");
  auto* ping = request.mutable_ping();
  ping->set_nonce("cli");
  ping->set_sent_at_unix_ms(
      std::chrono::duration_cast<std::chrono::milliseconds>(
          std::chrono::system_clock::now().time_since_epoch())
          .count());
  if (!stream->Write(request) || !stream->Read(&response) ||
      !response.has_pong() || response.pong().nonce() != "cli") {
    std::cerr << "planner ping failed\n";
    return 1;
  }
  stream->WritesDone();
  const auto status = stream->Finish();
  if (!status.ok()) {
    std::cerr << "stream finished with " << status.error_message()
              << '\n';
    return 1;
  }
  std::cout << "liveroute.v1 stream ready; ping succeeded\n";
  return 0;
}
