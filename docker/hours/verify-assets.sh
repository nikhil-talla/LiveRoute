#!/bin/sh

set -eu

seed_lock=/opt/liveroute/share/hours/seed.lock
tzdata_lock=/opt/liveroute/share/tzdata/tzdata.lock

seed_path="$(awk '/^  container_path: / { print $2; exit }' "$seed_lock")"
expected_seed_sha256="$(awk '/^  sha256: / { print $2; exit }' "$seed_lock")"
tzdata_release="$(awk '/^release: / { print $2; exit }' "$tzdata_lock")"
zoneinfo_path="/opt/liveroute/share/tzdata/$tzdata_release/zoneinfo"

test -f "$seed_path"
test -d "$zoneinfo_path"
test "$(cat "/opt/liveroute/share/tzdata/$tzdata_release/release")" = "$tzdata_release"
test "$(sha256sum "$seed_path" | awk '{ print $1 }')" = "$expected_seed_sha256"
test -f "$zoneinfo_path/America/New_York"
test -f "$zoneinfo_path/zone1970.tab"

awk -F '	' '
  $1 ~ /(^|,)US(,|$)/ && $3 == "America/New_York" { found = 1 }
  END { exit !found }
' "$zoneinfo_path/zone1970.tab"

TZDIR="$zoneinfo_path" zdump America/New_York >/dev/null

echo "Seeded-hours assets verified: seed SHA-256 and IANA tzdata $tzdata_release."
