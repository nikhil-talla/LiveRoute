#!/usr/bin/env bash
set -euo pipefail

image="${LIVEROUTE_PLANNER_IMAGE:-liveroute-planner-service:phase20-stage}"
output_dir="${LIVEROUTE_PLANNER_TAIL_ARTIFACT_DIR:-artifacts/benchmarks/phase20-planner-tail}"
combined_mask="${LIVEROUTE_PLANNER_TAIL_COMBINED_MASK:-7}"

usage() {
  echo "usage: $0 [--image IMAGE] [--output-dir DIRECTORY] [--combined-mask 1..7]" >&2
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
    --combined-mask)
      (($# >= 2)) || usage
      combined_mask="$2"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

case "$combined_mask" in
  1|2|3|4|5|6|7) ;;
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

variants=(
  tail-baseline
  validated-input
  lower-bound-scratch
  partial-beam-selection
  combined-candidate
)

for variant in "${variants[@]}"; do
  mkdir -p "$output_dir/$variant"
  chmod 0777 "$output_dir/$variant"
  for run in 1 2 3 4 5; do
    echo "planner-tail-v1 variant=$variant independent_run=$run image=$image_digest" >&2
    arguments=(
      --benchmark=tail
      --output-dir=/artifacts
      "--variant=$variant"
    )
    if [[ "$variant" == "combined-candidate" ]]; then
      arguments+=("--tail-mask=$combined_mask")
    fi
    docker run --rm \
      --entrypoint /workspace/build/liveroute_planner_allocation_bench \
      --env "LIVEROUTE_BENCHMARK_CONTAINER_IMAGE_DIGEST=$image_digest" \
      --volume "$(realpath "$output_dir/$variant"):/artifacts" \
      "$image" \
      "${arguments[@]}"
  done
done

mapfile -t artifacts < <(find "$output_dir" -type f -name 'planner-tail-v1-*.json' -print | sort)
if ((${#artifacts[@]} != 125)); then
  echo "expected exactly 125 raw artifacts, found ${#artifacts[@]}" >&2
  exit 1
fi

python3 scripts/aggregate-benchmarks.py \
  --output "$output_dir/planner-tail-v1-aggregate.json" \
  "${artifacts[@]}"
