#!/usr/bin/env bash
set -euo pipefail

image="${LIVEROUTE_PLANNER_IMAGE:-liveroute-planner-service:allocation-stage}"
output_dir="${LIVEROUTE_PLANNER_ARTIFACT_DIR:-artifacts/benchmarks}"
variant="${LIVEROUTE_PLANNER_VARIANT:-baseline}"

usage() {
  echo "usage: $0 [--image IMAGE] [--output-dir DIRECTORY] [--variant baseline|candidate]" >&2
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
    --variant)
      (($# >= 2)) || usage
      variant="$2"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

case "$variant" in
  baseline|candidate) ;;
  *) usage ;;
esac

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

mkdir -p "$output_dir"
if [[ ! -w "$output_dir" ]]; then
  echo "artifact directory is not writable: $output_dir" >&2
  exit 1
fi

for run in 1 2 3 4 5; do
  echo "planner-allocation-v1 variant=$variant independent_run=$run image=$image_digest" >&2
  docker run --rm \
    --entrypoint /workspace/build/liveroute_planner_allocation_bench \
    --env "LIVEROUTE_BENCHMARK_CONTAINER_IMAGE_DIGEST=$image_digest" \
    --volume "$(realpath "$output_dir"):/artifacts" \
    "$image" \
    --output-dir=/artifacts \
    --variant="$variant"
done
