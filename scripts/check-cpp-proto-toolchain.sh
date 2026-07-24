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

toolchain_image="bmigeri/devcon-cpp@$image_digest"
docker run --rm --platform linux/amd64 "$toolchain_image" bash -ceu '
  test "$(protoc --version)" = "libprotoc 31.1"
  test "$(pkg-config --modversion protobuf)" = "31.1.0"
  test "$(pkg-config --modversion grpc++)" = "1.78.1"
  test -x /opt/grpc/bin/grpc_cpp_plugin
'

echo "Pinned C++ Protobuf/gRPC toolchain checks passed."
