#!/usr/bin/env bash

set -euo pipefail

case "${LIVEROUTE_OSRM_PROFILE:-}" in
  car) profile=driving ;;
  foot) profile=walking ;;
  *) exit 2 ;;
esac

readonly path="/table/v1/$profile/-71.4128,41.8240;-71.4150,41.8300?annotations=duration,distance"
exec 3<>/dev/tcp/127.0.0.1/5000
printf 'GET %s HTTP/1.0\r\nHost: localhost\r\n\r\n' "$path" >&3
response=$(cat <&3)
grep --quiet '"code":"Ok"' <<<"$response"
grep --quiet '"durations"' <<<"$response"
grep --quiet '"distances"' <<<"$response"
