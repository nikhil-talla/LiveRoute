cmake_minimum_required(VERSION 3.25)

project(LiveRouteGeneratedProtoCheck LANGUAGES CXX)

if(NOT DEFINED GENERATED_CPP_DIR)
  message(FATAL_ERROR "GENERATED_CPP_DIR must point to checked-in C++ bindings")
endif()

find_package(absl CONFIG REQUIRED)

set(liveroute_protobuf_static_dependencies
  absl::algorithm
  absl::base
  absl::bind_front
  absl::bits
  absl::btree
  absl::cleanup
  absl::cord
  absl::core_headers
  absl::debugging
  absl::die_if_null
  absl::dynamic_annotations
  absl::flags
  absl::flat_hash_map
  absl::flat_hash_set
  absl::function_ref
  absl::hash
  absl::layout
  absl::log
  absl::log_globals
  absl::log_initialize
  absl::log_severity
  absl::memory
  absl::node_hash_map
  absl::node_hash_set
  absl::optional
  absl::random_distributions
  absl::random_random
  absl::span
  absl::status
  absl::statusor
  absl::strings
  absl::synchronization
  absl::time
  absl::type_traits
  absl::utility
  gRPC::utf8_range_lib
)

add_library(protobuf::libprotobuf STATIC IMPORTED GLOBAL)
set_target_properties(protobuf::libprotobuf PROPERTIES
  IMPORTED_LOCATION "/opt/grpc/lib/libprotobuf.a"
  INTERFACE_INCLUDE_DIRECTORIES "/opt/grpc/include"
  INTERFACE_LINK_LIBRARIES "${liveroute_protobuf_static_dependencies}"
)

add_library(protobuf::libprotoc STATIC IMPORTED GLOBAL)
set_target_properties(protobuf::libprotoc PROPERTIES
  IMPORTED_LOCATION "/opt/grpc/lib/libprotoc.a"
  INTERFACE_INCLUDE_DIRECTORIES "/opt/grpc/include"
)

find_package(gRPC CONFIG REQUIRED)

add_library(liveroute_generated_proto STATIC
  "${GENERATED_CPP_DIR}/liveroute/v1/planner.pb.cc"
  "${GENERATED_CPP_DIR}/liveroute/v1/planner.grpc.pb.cc"
)
target_include_directories(liveroute_generated_proto PUBLIC "${GENERATED_CPP_DIR}")
target_link_libraries(liveroute_generated_proto PUBLIC gRPC::grpc++)

add_executable(liveroute_generated_proto_link_check binding_compile_main.cpp)
target_link_libraries(liveroute_generated_proto_link_check
  PRIVATE liveroute_generated_proto
)
