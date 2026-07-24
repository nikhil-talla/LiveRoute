cmake_minimum_required(VERSION 3.25)

project(LiveRoute VERSION 0.1.0 LANGUAGES CXX)

option(LIVEROUTE_WARNINGS_AS_ERRORS "Treat project warnings as errors" ON)

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
  src/domain/hours.cpp
  src/domain/travel_time_matrix.cpp
  src/routing/travel_time_provider.cpp
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

add_executable(liveroute_hours_test tests/hours_test.cpp)
target_link_libraries(liveroute_hours_test PRIVATE liveroute_domain)
liveroute_configure_target(liveroute_hours_test)
add_test(NAME liveroute_hours_test COMMAND liveroute_hours_test)

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

add_executable(liveroute_priority_lanes_test tests/priority_lanes_test.cpp)
target_include_directories(liveroute_priority_lanes_test PRIVATE include)
liveroute_configure_target(liveroute_priority_lanes_test)
add_test(NAME liveroute_priority_lanes_test COMMAND liveroute_priority_lanes_test)

add_executable(liveroute_sharded_executor_test tests/sharded_executor_test.cpp)
target_include_directories(liveroute_sharded_executor_test PRIVATE include)
liveroute_configure_target(liveroute_sharded_executor_test)
add_test(NAME liveroute_sharded_executor_test COMMAND liveroute_sharded_executor_test)

add_executable(liveroute_smoke_bench bench/smoke_bench.cpp)
liveroute_configure_target(liveroute_smoke_bench)
