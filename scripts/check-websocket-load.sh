#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
token_file=${LIVEROUTE_DEV_TOKEN_FILE:-}

if [[ -z "$token_file" || ! -f "$token_file" ]]; then
  echo "LIVEROUTE_DEV_TOKEN_FILE must name the regular 43-character development-token file." >&2
  exit 2
fi
token_file=$(readlink -f "$token_file")
if [[ $(wc -c <"$token_file") -ne 43 ]]; then
  echo "LIVEROUTE_DEV_TOKEN_FILE must contain exactly 43 characters." >&2
  exit 2
fi

export LIVEROUTE_DEV_TOKEN_FILE=$token_file
if ! curl --fail --silent --show-error \
    http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
  docker compose --progress quiet --project-directory "$repo_root" \
    --profile backend up --detach --wait
fi

ready=false
for _ in $(seq 1 60); do
  if curl --fail --silent --show-error \
      http://127.0.0.1:8080/readyz >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo "LiveRoute backend did not become ready within 60 seconds." >&2
  exit 3
fi

backend_container=$(docker compose --project-directory "$repo_root" \
  --profile backend ps --quiet backend)
if [[ -z "$backend_container" ]]; then
  echo "LiveRoute backend container is not running." >&2
  exit 3
fi

image="liveroute-websocket-load:check"
docker build --quiet --file "$repo_root/docker/cpp/Dockerfile" \
  --tag "$image" "$repo_root"
docker run --rm --network "container:$backend_container" \
  --mount "type=bind,source=$token_file,target=/run/secrets/liveroute_dev_token,readonly" \
  --entrypoint /workspace/build/liveroute_websocket_loadgen \
  "$image" \
  --target ws://127.0.0.1:8080/ws \
  --token-file /run/secrets/liveroute_dev_token \
  --self-check

echo "Pinned WebSocket load path check passed."
