cmake_minimum_required(VERSION 3.25)

project(LiveRoute VERSION 0.1.0 LANGUAGES CXX)

option(LIVEROUTE_WARNINGS_AS_ERRORS "Treat project warnings as errors" ON)
option(LIVEROUTE_ENABLE_PINNED_TZDATA_TESTS
  "Enable tests that require the verified tzdata container asset" OFF)
option(LIVEROUTE_ENABLE_TSAN "Build and link C++ targets with ThreadSanitizer" OFF)
option(LIVEROUTE_ENABLE_GRPC
  "Build the pinned Protobuf/gRPC transport and server" OFF)
option(LIVEROUTE_ENABLE_OSRM
  "Build the libcurl-multi OSRM travel-time provider" OFF)
option(LIVEROUTE_ENABLE_OSRM_INTEGRATION_TESTS
  "Register tests that require the local car/foot OSRM services" OFF)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(LIVEROUTE_ENABLE_TSAN)
  if(NOT CMAKE_CXX_COMPILER_ID MATCHES "GNU|Clang")
    message(FATAL_ERROR "ThreadSanitizer requires GCC or Clang")
  endif()
  # GCC's TSan shadow mapping is incompatible with PIE under the Docker
  # Desktop/WSL address layout used by the container-first test workflow.
  add_compile_options(-fsanitize=thread -fno-omit-frame-pointer -fno-pie)
  add_link_options(-fsanitize=thread -no-pie)
endif()

function(liveroute_configure_target target)
  target_compile_options(${target} PRIVATE
    $<$<CXX_COMPILER_ID:GNU,Clang,AppleClang>:-Wall -Wextra -Wpedantic>
  )

  if(LIVEROUTE_WARNINGS_AS_ERRORS)
    target_compile_options(${target} PRIVATE
      $<$<CXX_COMPILER_ID:GNU,Clang,AppleClang>:-Werror>
    )
  endif()
endfunction()

add_library(liveroute_domain
  src/domain/activity.cpp
  src/domain/hours.cpp
  src/domain/current_plan.cpp
  src/domain/plan_proposal.cpp
  src/domain/trip_event.cpp
  src/domain/trip_state.cpp
  src/providers/sha256.cpp
  src/providers/seeded_hours_seed.cpp
  src/providers/seeded_hours_provider.cpp
  src/providers/strict_json.cpp
  src/providers/tzif.cpp
  src/domain/travel_time_matrix.cpp
  src/planner/beam_search.cpp
  src/planner/candidate_itinerary.cpp
  src/planner/planning_input_assembler.cpp
  src/planner/proposal_assembler.cpp
  src/planner/replan_attempt.cpp
  src/planner/result_metadata.cpp
  src/routing/osrm_response.cpp
  src/routing/travel_time_provider.cpp
  src/runtime/concurrent_trip_runtime.cpp
  src/runtime/planning_commit.cpp
  src/runtime/trip_runtime_versions.cpp
  src/runtime/event_coordinator.cpp
)
target_include_directories(liveroute_domain PUBLIC include)
liveroute_configure_target(liveroute_domain)

if(LIVEROUTE_ENABLE_OSRM)
  find_package(CURL REQUIRED)
  target_sources(liveroute_domain PRIVATE
    src/routing/osrm_travel_time_provider.cpp
  )
  target_link_libraries(liveroute_domain PUBLIC CURL::libcurl)
  target_compile_definitions(liveroute_domain PUBLIC LIVEROUTE_ENABLE_OSRM=1)

  add_executable(liveroute_osrm_provider_integration_test
    tests/osrm_provider_integration_test.cpp
  )
  target_link_libraries(liveroute_osrm_provider_integration_test
    PRIVATE liveroute_domain
  )
  liveroute_configure_target(liveroute_osrm_provider_integration_test)
  if(LIVEROUTE_ENABLE_OSRM_INTEGRATION_TESTS)
    add_test(NAME liveroute_osrm_provider_integration_test
      COMMAND liveroute_osrm_provider_integration_test
    )
  endif()
endif()

add_library(liveroute_load_model src/load/workload.cpp)
target_include_directories(liveroute_load_model PUBLIC include)
liveroute_configure_target(liveroute_load_model)

add_executable(liveroute_loadgen src/load/main.cpp)
target_link_libraries(liveroute_loadgen
  PRIVATE liveroute_domain liveroute_load_model
)
liveroute_configure_target(liveroute_loadgen)

enable_testing()
add_test(NAME liveroute_loadgen_self_check
  COMMAND liveroute_loadgen --self-check
)

if(LIVEROUTE_ENABLE_GRPC)
  find_package(absl CONFIG REQUIRED)

  set(liveroute_protobuf_static_dependencies
    absl::algorithm absl::base absl::bind_front absl::bits absl::btree
    absl::cleanup absl::cord absl::core_headers absl::debugging
    absl::die_if_null absl::dynamic_annotations absl::flags
    absl::flat_hash_map absl::flat_hash_set absl::function_ref absl::hash
    absl::layout absl::log absl::log_globals absl::log_initialize
    absl::log_internal_check_op absl::log_severity absl::memory
    absl::node_hash_map absl::node_hash_set
    absl::optional absl::random_distributions absl::random_random absl::span
    absl::status absl::statusor absl::strings absl::synchronization absl::time
    absl::type_traits absl::utility gRPC::utf8_range_lib
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

  add_library(liveroute_proto STATIC
    gen/cpp/liveroute/v1/planner.pb.cc
    gen/cpp/liveroute/v1/planner.grpc.pb.cc
  )
  target_include_directories(liveroute_proto PUBLIC gen/cpp)
  target_link_libraries(liveroute_proto PUBLIC gRPC::grpc++)
  liveroute_configure_target(liveroute_proto)

  add_library(liveroute_transport
    src/transport/proto_conversion.cpp
    src/transport/grpc_planner_service.cpp
  )
  target_include_directories(liveroute_transport PUBLIC include)
  target_link_libraries(liveroute_transport
    PUBLIC liveroute_domain liveroute_proto gRPC::grpc++
  )
  liveroute_configure_target(liveroute_transport)

  add_executable(liveroute_planner_cli src/client/main.cpp)
  target_link_libraries(liveroute_planner_cli PRIVATE liveroute_transport)
  liveroute_configure_target(liveroute_planner_cli)

  add_library(liveroute_grpc_load_runner src/load/grpc_runner.cpp)
  target_include_directories(liveroute_grpc_load_runner PUBLIC include)
  target_link_libraries(liveroute_grpc_load_runner
    PUBLIC liveroute_load_model liveroute_transport
  )
  liveroute_configure_target(liveroute_grpc_load_runner)

  add_executable(liveroute_grpc_loadgen src/load/grpc_main.cpp)
  target_link_libraries(liveroute_grpc_loadgen
    PRIVATE liveroute_grpc_load_runner
  )
  liveroute_configure_target(liveroute_grpc_loadgen)

  add_executable(liveroute_grpc_planner_service_test
    tests/grpc_planner_service_test.cpp
  )
  target_link_libraries(liveroute_grpc_planner_service_test
    PRIVATE liveroute_transport
  )
  liveroute_configure_target(liveroute_grpc_planner_service_test)
  add_test(NAME liveroute_grpc_planner_service_test
    COMMAND liveroute_grpc_planner_service_test
  )

  add_executable(liveroute_grpc_load_runner_test
    tests/grpc_load_runner_test.cpp
  )
  target_link_libraries(liveroute_grpc_load_runner_test
    PRIVATE liveroute_grpc_load_runner
  )
  liveroute_configure_target(liveroute_grpc_load_runner_test)
  add_test(NAME liveroute_grpc_load_runner_test
    COMMAND liveroute_grpc_load_runner_test
  )

  add_executable(liveroute_proto_serialization_bench
    bench/proto_serialization_bench.cpp
  )
  target_link_libraries(liveroute_proto_serialization_bench
    PRIVATE liveroute_proto
  )
  liveroute_configure_target(liveroute_proto_serialization_bench)
endif()

add_executable(liveroute_planner_service src/server/main.cpp)
target_link_libraries(liveroute_planner_service PRIVATE liveroute_domain)
if(LIVEROUTE_ENABLE_GRPC)
  target_compile_definitions(liveroute_planner_service
    PRIVATE LIVEROUTE_ENABLE_GRPC=1)
  target_link_libraries(liveroute_planner_service PRIVATE liveroute_transport)
endif()
liveroute_configure_target(liveroute_planner_service)

add_executable(liveroute_smoke_test tests/smoke_test.cpp)
liveroute_configure_target(liveroute_smoke_test)
add_test(NAME liveroute_smoke_test COMMAND liveroute_smoke_test)

add_executable(liveroute_workload_test tests/workload_test.cpp)
target_link_libraries(liveroute_workload_test PRIVATE liveroute_load_model)
liveroute_configure_target(liveroute_workload_test)
add_test(NAME liveroute_workload_test COMMAND liveroute_workload_test)

add_executable(liveroute_domain_matrix_test tests/domain_matrix_test.cpp)
target_link_libraries(liveroute_domain_matrix_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_domain_matrix_test)
add_test(NAME liveroute_domain_matrix_test COMMAND liveroute_domain_matrix_test)

add_executable(liveroute_activity_test tests/activity_test.cpp)
target_link_libraries(liveroute_activity_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_activity_test)
add_test(NAME liveroute_activity_test COMMAND liveroute_activity_test)

add_executable(liveroute_candidate_score_test tests/candidate_score_test.cpp)
target_include_directories(liveroute_candidate_score_test PRIVATE include)
liveroute_configure_target(liveroute_candidate_score_test)
add_test(NAME liveroute_candidate_score_test COMMAND liveroute_candidate_score_test)

add_executable(liveroute_beam_search_input_test tests/beam_search_input_test.cpp)
target_link_libraries(liveroute_beam_search_input_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_beam_search_input_test)
add_test(NAME liveroute_beam_search_input_test COMMAND liveroute_beam_search_input_test)

add_executable(liveroute_planning_input_assembler_test
  tests/planning_input_assembler_test.cpp
)
target_link_libraries(liveroute_planning_input_assembler_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_planning_input_assembler_test)
add_test(NAME liveroute_planning_input_assembler_test
  COMMAND liveroute_planning_input_assembler_test
)

add_executable(liveroute_candidate_generation_test
  tests/candidate_generation_test.cpp
)
target_link_libraries(liveroute_candidate_generation_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_candidate_generation_test)
add_test(NAME liveroute_candidate_generation_test
  COMMAND liveroute_candidate_generation_test
)

add_executable(liveroute_beam_search_scoring_test
  tests/beam_search_scoring_test.cpp
)
target_link_libraries(liveroute_beam_search_scoring_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_beam_search_scoring_test)
add_test(NAME liveroute_beam_search_scoring_test
  COMMAND liveroute_beam_search_scoring_test
)

add_executable(liveroute_beam_search_traversal_test
  tests/beam_search_traversal_test.cpp
)
target_link_libraries(liveroute_beam_search_traversal_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_beam_search_traversal_test)
add_test(NAME liveroute_beam_search_traversal_test
  COMMAND liveroute_beam_search_traversal_test
)

add_executable(liveroute_candidate_itinerary_test
  tests/candidate_itinerary_test.cpp
)
target_link_libraries(liveroute_candidate_itinerary_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_candidate_itinerary_test)
add_test(NAME liveroute_candidate_itinerary_test
  COMMAND liveroute_candidate_itinerary_test
)

add_executable(liveroute_checked_int_test tests/checked_int_test.cpp)
target_include_directories(liveroute_checked_int_test PRIVATE include)
liveroute_configure_target(liveroute_checked_int_test)
add_test(NAME liveroute_checked_int_test COMMAND liveroute_checked_int_test)

add_executable(liveroute_hours_test tests/hours_test.cpp)
target_link_libraries(liveroute_hours_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_hours_test)
add_test(NAME liveroute_hours_test COMMAND liveroute_hours_test)

add_executable(liveroute_current_plan_test tests/current_plan_test.cpp)
target_link_libraries(liveroute_current_plan_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_current_plan_test)
add_test(NAME liveroute_current_plan_test COMMAND liveroute_current_plan_test)

add_executable(liveroute_plan_proposal_test tests/plan_proposal_test.cpp)
target_link_libraries(liveroute_plan_proposal_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_plan_proposal_test)
add_test(NAME liveroute_plan_proposal_test COMMAND liveroute_plan_proposal_test)

add_executable(liveroute_trip_event_test tests/trip_event_test.cpp)
target_link_libraries(liveroute_trip_event_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_trip_event_test)
add_test(NAME liveroute_trip_event_test COMMAND liveroute_trip_event_test)

add_executable(liveroute_trip_state_test tests/trip_state_test.cpp)
target_link_libraries(liveroute_trip_state_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_trip_state_test)
add_test(NAME liveroute_trip_state_test COMMAND liveroute_trip_state_test)

add_executable(liveroute_result_metadata_test tests/result_metadata_test.cpp)
target_link_libraries(liveroute_result_metadata_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_result_metadata_test)
add_test(NAME liveroute_result_metadata_test COMMAND liveroute_result_metadata_test)

add_executable(liveroute_proposal_assembler_test
  tests/proposal_assembler_test.cpp
)
target_link_libraries(liveroute_proposal_assembler_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_proposal_assembler_test)
add_test(NAME liveroute_proposal_assembler_test
  COMMAND liveroute_proposal_assembler_test
)

add_executable(liveroute_replan_attempt_test tests/replan_attempt_test.cpp)
target_link_libraries(liveroute_replan_attempt_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_replan_attempt_test)
add_test(NAME liveroute_replan_attempt_test COMMAND liveroute_replan_attempt_test)

add_executable(liveroute_tzif_test tests/tzif_test.cpp)
target_link_libraries(liveroute_tzif_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_tzif_test)
add_test(NAME liveroute_tzif_test COMMAND liveroute_tzif_test)

add_executable(liveroute_strict_json_test tests/strict_json_test.cpp)
target_link_libraries(liveroute_strict_json_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_strict_json_test)
add_test(NAME liveroute_strict_json_test COMMAND liveroute_strict_json_test)

add_executable(liveroute_sha256_test tests/sha256_test.cpp)
target_link_libraries(liveroute_sha256_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_sha256_test)
add_test(NAME liveroute_sha256_test COMMAND liveroute_sha256_test)

add_executable(liveroute_seeded_hours_seed_test tests/seeded_hours_seed_test.cpp)
target_link_libraries(liveroute_seeded_hours_seed_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_seeded_hours_seed_test)
add_test(NAME liveroute_seeded_hours_seed_test COMMAND liveroute_seeded_hours_seed_test)
set_tests_properties(liveroute_seeded_hours_seed_test PROPERTIES
  WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}"
)

if(LIVEROUTE_ENABLE_PINNED_TZDATA_TESTS)
  add_executable(liveroute_seeded_hours_provider_test
    tests/seeded_hours_provider_test.cpp
  )
  target_link_libraries(liveroute_seeded_hours_provider_test PRIVATE liveroute_domain)
  liveroute_configure_target(liveroute_seeded_hours_provider_test)
  add_test(NAME liveroute_seeded_hours_provider_test
    COMMAND liveroute_seeded_hours_provider_test
  )
  set_tests_properties(liveroute_seeded_hours_provider_test PROPERTIES
    WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}"
    ENVIRONMENT "LIVEROUTE_TZDATA_ZONEINFO_PATH=/opt/liveroute/share/tzdata/2026c/zoneinfo"
  )
endif()

add_executable(liveroute_travel_time_provider_test
  tests/travel_time_provider_test.cpp
)
target_link_libraries(liveroute_travel_time_provider_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_travel_time_provider_test)
add_test(NAME liveroute_travel_time_provider_test
  COMMAND liveroute_travel_time_provider_test
)

add_executable(liveroute_osrm_response_test tests/osrm_response_test.cpp)
target_link_libraries(liveroute_osrm_response_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_osrm_response_test)
add_test(NAME liveroute_osrm_response_test COMMAND liveroute_osrm_response_test)

add_executable(liveroute_bounded_queue_test tests/bounded_queue_test.cpp)
target_include_directories(liveroute_bounded_queue_test PRIVATE include)
liveroute_configure_target(liveroute_bounded_queue_test)
add_test(NAME liveroute_bounded_queue_test COMMAND liveroute_bounded_queue_test)

add_executable(liveroute_bounded_executor_test tests/bounded_executor_test.cpp)
target_include_directories(liveroute_bounded_executor_test PRIVATE include)
liveroute_configure_target(liveroute_bounded_executor_test)
add_test(NAME liveroute_bounded_executor_test COMMAND liveroute_bounded_executor_test)

add_executable(liveroute_priority_lanes_test tests/priority_lanes_test.cpp)
target_include_directories(liveroute_priority_lanes_test PRIVATE include)
liveroute_configure_target(liveroute_priority_lanes_test)
add_test(NAME liveroute_priority_lanes_test COMMAND liveroute_priority_lanes_test)

add_executable(liveroute_sharded_executor_test tests/sharded_executor_test.cpp)
target_include_directories(liveroute_sharded_executor_test PRIVATE include)
liveroute_configure_target(liveroute_sharded_executor_test)
add_test(NAME liveroute_sharded_executor_test COMMAND liveroute_sharded_executor_test)

add_executable(liveroute_trip_runtime_versions_test
  tests/trip_runtime_versions_test.cpp
)
target_link_libraries(liveroute_trip_runtime_versions_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_trip_runtime_versions_test)
add_test(NAME liveroute_trip_runtime_versions_test
  COMMAND liveroute_trip_runtime_versions_test
)

add_executable(liveroute_event_coordinator_test
  tests/event_coordinator_test.cpp
)
target_link_libraries(liveroute_event_coordinator_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_event_coordinator_test)
add_test(NAME liveroute_event_coordinator_test
  COMMAND liveroute_event_coordinator_test
)

add_executable(liveroute_concurrent_trip_runtime_test
  tests/concurrent_trip_runtime_test.cpp
)
target_link_libraries(liveroute_concurrent_trip_runtime_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_concurrent_trip_runtime_test)
add_test(NAME liveroute_concurrent_trip_runtime_test
  COMMAND liveroute_concurrent_trip_runtime_test
)

add_executable(liveroute_planning_commit_test
  tests/planning_commit_test.cpp
)
target_link_libraries(liveroute_planning_commit_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_planning_commit_test)
add_test(NAME liveroute_planning_commit_test COMMAND liveroute_planning_commit_test)

add_executable(liveroute_smoke_bench bench/smoke_bench.cpp)
liveroute_configure_target(liveroute_smoke_bench)

add_executable(liveroute_sharded_runtime_bench
  bench/sharded_runtime_bench.cpp
)
target_link_libraries(liveroute_sharded_runtime_bench
  PRIVATE liveroute_domain
)
liveroute_configure_target(liveroute_sharded_runtime_bench)
