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

docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$repo_root,dst=/workspace" \
  --workdir /workspace \
  "bmigeri/devcon-cpp@$image_digest" bash -ceu '
    test "$(protoc --version)" = "libprotoc 31.1"
    test -x /opt/grpc/bin/grpc_cpp_plugin
    mkdir -p gen/cpp
    mapfile -t proto_files < <(find proto -type f -name "*.proto" -print | LC_ALL=C sort)
    /opt/grpc/bin/protoc -I proto \
      --cpp_out=gen/cpp \
      --grpc_out=gen/cpp \
      --plugin=protoc-gen-grpc=/opt/grpc/bin/grpc_cpp_plugin \
      "${proto_files[@]}"
  '
