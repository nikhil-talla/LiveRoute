#!/bin/sh

set -eu

lock_path=/opt/liveroute/share/timezone-boundaries/timezone-boundaries.lock
geojson_path="$(awk '/^  container_geojson_path: / { print $2; exit }' "$lock_path")"
expected_sha256="$(awk '/^  extracted_geojson_sha256: / { print $2; exit }' "$lock_path")"
expected_size="$(awk '/^  extracted_geojson_size_bytes: / { print $2; exit }' "$lock_path")"

test -f "$geojson_path"
test "$(stat --format='%s' "$geojson_path")" = "$expected_size"
test "$(sha256sum "$geojson_path" | awk '{ print $1 }')" = "$expected_sha256"
test "$(head -c 1 "$geojson_path")" = '{'

echo "Timezone-boundary asset verified: release 2026c and extracted GeoJSON SHA-256."
