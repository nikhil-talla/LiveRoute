cmake_minimum_required(VERSION 3.25)

project(LiveRoute VERSION 0.1.0 LANGUAGES CXX)

option(LIVEROUTE_WARNINGS_AS_ERRORS "Treat project warnings as errors" ON)
option(LIVEROUTE_ENABLE_PINNED_TZDATA_TESTS
  "Enable tests that require the verified tzdata container asset" OFF)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

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
  src/providers/sha256.cpp
  src/providers/seeded_hours_seed.cpp
  src/providers/seeded_hours_provider.cpp
  src/providers/strict_json.cpp
  src/providers/tzif.cpp
  src/domain/travel_time_matrix.cpp
  src/planner/beam_search.cpp
  src/routing/travel_time_provider.cpp
  src/runtime/trip_runtime_versions.cpp
)
target_include_directories(liveroute_domain PUBLIC include)
liveroute_configure_target(liveroute_domain)

add_executable(liveroute_planner_service src/server/main.cpp)
target_link_libraries(liveroute_planner_service PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_planner_service)

enable_testing()

add_executable(liveroute_smoke_test tests/smoke_test.cpp)
liveroute_configure_target(liveroute_smoke_test)
add_test(NAME liveroute_smoke_test COMMAND liveroute_smoke_test)

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

add_executable(liveroute_smoke_bench bench/smoke_bench.cpp)
liveroute_configure_target(liveroute_smoke_bench)
