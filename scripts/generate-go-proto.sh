#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image_name="liveroute-go-proto-toolchain:local"

docker build --file "$repo_root/docker/go-proto/Dockerfile" \
  --tag "$image_name" "$repo_root"
docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$repo_root,dst=/workspace" \
  --workdir /workspace "$image_name" bash -ceu '
    test "$(protoc --version)" = "libprotoc 31.1"
    test "$(protoc-gen-go --version)" = "protoc-gen-go v1.36.6"
    test "$(protoc-gen-go-grpc --version)" = "protoc-gen-go-grpc 1.5.1"
    mapfile -t proto_files < <(find proto -type f -name "*.proto" -print | LC_ALL=C sort)
    protoc -I proto \
      --go_out=backend --go_opt=module=github.com/liveroute/liveroute/backend \
      --go-grpc_out=backend --go-grpc_opt=module=github.com/liveroute/liveroute/backend \
      "${proto_files[@]}"
  '
