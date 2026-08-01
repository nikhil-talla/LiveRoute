#!/usr/bin/env bash

set -euo pipefail

readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly source_file="$root/data/osrm/source/rhode-island-260701.osm.pbf"
readonly expected_size=51668445
readonly expected_sha256=375eb017159102cff19032d5e679a061726d4c9c69851871cf03a1a893ee4b40

test -f "$source_file"
test "$(stat --format=%s "$source_file")" = "$expected_size"
test "$(sha256sum "$source_file" | awk '{ print $1 }')" = "$expected_sha256"

(
  cd "$root/config/osrm/profiles"
  sha256sum --check ../profiles.sha256
)

docker compose --project-directory "$root" --profile osrm config --quiet
