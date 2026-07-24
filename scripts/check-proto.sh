#!/usr/bin/env bash

set -euo pipefail

buf_image="bufbuild/buf@sha256:65bd496a89c762ad7151ca9e7d885a45dacb3671a8e8ec39738b9f844d3405ea"
docker_args=(--rm --user "$(id -u):$(id -g)" --env HOME=/tmp)

docker run "${docker_args[@]}" --volume "$PWD:/workspace:ro" --workdir /workspace "$buf_image" lint
docker run "${docker_args[@]}" --volume "$PWD:/workspace" --workdir /workspace "$buf_image" \
  build --output proto/baseline/liveroute-v1.binpb
docker run "${docker_args[@]}" --volume "$PWD:/workspace:ro" --workdir /workspace "$buf_image" \
  breaking --against proto/baseline/liveroute-v1.binpb
