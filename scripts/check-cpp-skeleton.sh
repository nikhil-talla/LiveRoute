#!/usr/bin/env bash

set -euo pipefail

build_dir="${1:-build/cpp-skeleton}"
cmake -S . -B "$build_dir" -DCMAKE_BUILD_TYPE=Debug
cmake --build "$build_dir"
ctest --test-dir "$build_dir" --output-on-failure
"$build_dir/liveroute_smoke_bench"
