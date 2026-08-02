#!/usr/bin/env bash
set -euo pipefail

image="${LIVEROUTE_PLANNER_IMAGE:-liveroute-planner-service:phase19-stage}"
output_dir="${LIVEROUTE_PLANNER_LAYOUT_ARTIFACT_DIR:-artifacts/benchmarks/phase19-planner-layout}"

usage() {
  echo "usage: $0 [--image IMAGE] [--output-dir DIRECTORY]" >&2
  exit 2
}

while (($# > 0)); do
  case "$1" in
    --image)
      (($# >= 2)) || usage
      image="$2"
      shift 2
      ;;
    --output-dir)
      (($# >= 2)) || usage
      output_dir="$2"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}

image_digest="$(docker image inspect "$image" --format '{{.Id}}')"
case "$image_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *)
    echo "image does not expose a sha256 identity: $image" >&2
    exit 1
    ;;
esac

mkdir -p "$output_dir/aos-baseline" "$output_dir/soa-candidate"
chmod 0777 "$output_dir/aos-baseline" "$output_dir/soa-candidate"

for variant in aos-baseline soa-candidate; do
  for run in 1 2 3 4 5; do
    echo "planner-layout-timing-v1 variant=$variant independent_run=$run image=$image_digest" >&2
    docker run --rm \
      --entrypoint /workspace/build/liveroute_planner_allocation_bench \
      --env "LIVEROUTE_BENCHMARK_CONTAINER_IMAGE_DIGEST=$image_digest" \
      --volume "$(realpath "$output_dir/$variant"):/artifacts" \
      "$image" \
      --benchmark=layout-timing \
      --output-dir=/artifacts \
      --variant="$variant"
  done
done
