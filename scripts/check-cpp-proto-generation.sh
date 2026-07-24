#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image_digest=$(awk '
  /^  cpp_grpc_toolchain:$/ { in_toolchain = 1; next }
  in_toolchain && /^    digest: / { print $2; exit }
  in_toolchain && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")

if [[ ! $image_digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "missing pinned C++ gRPC toolchain digest in config/tool-images.lock" >&2
  exit 1
fi

temporary_output=$(mktemp -d)
cleanup() {
  rm -rf "$temporary_output"
}
trap cleanup EXIT

docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$repo_root/proto,dst=/input/proto,readonly" \
  --mount "type=bind,src=$temporary_output,dst=/output" \
  "bmigeri/devcon-cpp@$image_digest" bash -ceu '
    test "$(protoc --version)" = "libprotoc 31.1"
    test "$(pkg-config --modversion protobuf)" = "31.1.0"
    test "$(pkg-config --modversion grpc++)" = "1.78.1"
    test -x /opt/grpc/bin/grpc_cpp_plugin
    mapfile -t proto_files < <(find /input/proto -type f -name "*.proto" -print | LC_ALL=C sort)
    /opt/grpc/bin/protoc -I /input/proto \
      --cpp_out=/output \
      --grpc_out=/output \
      --plugin=protoc-gen-grpc=/opt/grpc/bin/grpc_cpp_plugin \
      "${proto_files[@]}"
  '

if ! diff --recursive --brief "$repo_root/gen/cpp" "$temporary_output"; then
  echo "checked-in C++ Protobuf/gRPC bindings are stale; run scripts/generate-cpp-proto.sh" >&2
  exit 1
fi

mkdir -p "$temporary_output/build"
docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$repo_root,dst=/workspace,readonly" \
  --mount "type=bind,src=$temporary_output/build,dst=/build" \
  "bmigeri/devcon-cpp@$image_digest" bash -ceu '
    cmake -S /workspace/tests/proto -B /build -G Ninja \
      -DGENERATED_CPP_DIR=/workspace/gen/cpp \
      -DCMAKE_PREFIX_PATH=/opt/grpc
    cmake --build /build
    /build/liveroute_generated_proto_link_check
  '

echo "Checked-in C++ Protobuf/gRPC bindings match the pinned toolchain."
