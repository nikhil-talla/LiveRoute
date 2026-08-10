#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image_digest=$(awk '
  /^  frontend_toolchain:$/ { active = 1; next }
  active && /^    digest: / { print $2; exit }
  active && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")

if [[ ! $image_digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "missing pinned frontend toolchain digest" >&2
  exit 1
fi

grep --fixed-strings --quiet "node@$image_digest" \
  "$repo_root/docker/frontend/Dockerfile"

image="liveroute-frontend-test:local"
docker build \
  --file "$repo_root/docker/frontend/Dockerfile" \
  --target test \
  --tag "$image" \
  "$repo_root"
docker run --rm "$image"

echo "Frontend generated-contract, lint, format, test, build, and audit checks passed."
