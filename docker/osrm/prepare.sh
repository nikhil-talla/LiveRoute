#!/usr/bin/env bash

set -euo pipefail

readonly source_file=/data/source/rhode-island-260701.osm.pbf
readonly expected_size=51668445
readonly expected_sha256=375eb017159102cff19032d5e679a061726d4c9c69851871cf03a1a893ee4b40
readonly build_id=geofabrik-rhode-island-260701-osrm-v26.5.0-mld-v1

case "${LIVEROUTE_OSRM_PROFILE:-}" in
  car|foot) ;;
  *)
    echo "LIVEROUTE_OSRM_PROFILE must be car or foot" >&2
    exit 2
    ;;
esac

readonly output_dir="/data/generated/$LIVEROUTE_OSRM_PROFILE"
readonly output_base="$output_dir/rhode-island.osrm"
readonly stamp="$output_dir/.liveroute-osrm-build"
readonly expected_stamp="$build_id:$LIVEROUTE_OSRM_PROFILE"

test -f "$source_file"
test "$(stat --format=%s "$source_file")" = "$expected_size"
test "$(sha256sum "$source_file" | awk '{ print $1 }')" = "$expected_sha256"

mkdir --parents "$output_dir"
if test -f "$stamp" \
    && test "$(cat "$stamp")" = "$expected_stamp" \
    && test -f "$output_base.partition" \
    && test -f "$output_base.cell_metrics"; then
  exit 0
fi

find "$output_dir" -maxdepth 1 -type f \
  \( -name 'rhode-island.osrm*' -o -name '.liveroute-osrm-build' \) \
  -delete

cd /opt/liveroute/profiles
osrm-extract \
  --threads 1 \
  --profile "/opt/liveroute/profiles/$LIVEROUTE_OSRM_PROFILE.lua" \
  --output "$output_base" \
  "$source_file"
osrm-partition --threads 1 "$output_base"
osrm-customize --threads 1 "$output_base"
printf '%s\n' "$expected_stamp" > "$stamp"
