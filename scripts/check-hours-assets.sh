#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

python3 "$repo_root/scripts/check-hours-seed.py"
docker compose --project-directory "$repo_root" \
  --profile hours build hours-assets-check
docker compose --project-directory "$repo_root" \
  --profile hours run --rm hours-assets-check
