#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
suffix=$$
network_name="liveroute-stream-test-$suffix"
planner_name="liveroute-stream-planner-$suffix"
planner_image="liveroute-stream-planner:$suffix"
backend_image="liveroute-stream-backend:$suffix"

cleanup() {
  docker rm --force "$planner_name" >/dev/null 2>&1 || true
  docker network rm "$network_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build --file "$repo_root/docker/cpp/Dockerfile" \
  --tag "$planner_image" "$repo_root"
docker build --file "$repo_root/docker/backend/Dockerfile" --target test \
  --tag "$backend_image" "$repo_root"
docker network create "$network_name" >/dev/null
docker run --detach --name "$planner_name" --network "$network_name" \
  --network-alias planner "$planner_image" --listen=0.0.0.0:50051 >/dev/null

docker run --rm --network "$network_name" \
  --env LIVEROUTE_TEST_PLANNER_TARGET=planner:50051 \
  "$backend_image" \
  go test -count=1 -run '^TestPinnedPlannerStreamIntegration$' \
  ./internal/plannertransport

echo "Pinned Go-to-C++ planner stream negotiation and correlation check passed."
