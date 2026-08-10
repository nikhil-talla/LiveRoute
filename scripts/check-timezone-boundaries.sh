#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
base_digest=$(awk '
  /^  cpp_skeleton_builder:$/ { active = 1; next }
  active && /^    digest: / { print $2; exit }
  active && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")

if [[ ! $base_digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "missing pinned timezone-boundary base image digest" >&2
  exit 1
fi

dockerfile="$repo_root/docker/timezone-boundaries/Dockerfile"
if [[ $(grep --fixed-strings --count "debian@$base_digest" "$dockerfile") -ne 2 ]]; then
  echo "timezone-boundary build and runtime stages must use the pinned base image" >&2
  exit 1
fi

image="liveroute-timezone-boundaries-check:local"
docker build --file "$dockerfile" --tag "$image" "$repo_root"
docker run --rm "$image"
