#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image_name="liveroute-go-proto-toolchain:check"
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT

grep --fixed-strings --quiet \
  'golang@sha256:3f6236bd765f898a2a3c2946112b04097814c4529d44534674700cd07b9c6b4c' \
  "$repo_root/docker/go-proto/Dockerfile"
grep --fixed-strings --quiet \
  'bmigeri/devcon-cpp@sha256:369f7744d6f9632b1c8142981f01c7b4c98db51b0096686dbd25f9ebb9eaa6f4' \
  "$repo_root/docker/go-proto/Dockerfile"

docker build --file "$repo_root/docker/go-proto/Dockerfile" \
  --tag "$image_name" "$repo_root"
docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$repo_root,dst=/workspace,readonly" \
  --mount "type=bind,src=$temporary,dst=/output" \
  --workdir /workspace "$image_name" bash -ceu '
    mapfile -t proto_files < <(find proto -type f -name "*.proto" -print | LC_ALL=C sort)
    protoc -I proto \
      --go_out=/output --go_opt=module=github.com/liveroute/liveroute/backend \
      --go-grpc_out=/output --go-grpc_opt=module=github.com/liveroute/liveroute/backend \
      "${proto_files[@]}"
  '
diff --recursive --unified "$repo_root/backend/gen" "$temporary/gen"

echo "Checked-in Go Protobuf/gRPC bindings match the pinned toolchain."
