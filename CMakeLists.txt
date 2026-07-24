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

add_executable(liveroute_planner_service src/server/main.cpp)
liveroute_configure_target(liveroute_planner_service)

enable_testing()

add_executable(liveroute_smoke_test tests/smoke_test.cpp)
liveroute_configure_target(liveroute_smoke_test)
add_test(NAME liveroute_smoke_test COMMAND liveroute_smoke_test)

add_executable(liveroute_smoke_bench bench/smoke_bench.cpp)
liveroute_configure_target(liveroute_smoke_bench)
