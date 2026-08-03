# Benchmarking Methodology

## Principles

Benchmarks run in containers and state exactly what they measure. Planner-only
measurements exclude route-service, database, browser-connection, and internal
network time. Runtime reports keep queueing, route lookup, planning, and
serialization as separate stages.

Raw artifacts validate against
`schema/benchmark/liveroute-benchmark-v1.schema.json`; merged reports validate
against `schema/benchmark/liveroute-benchmark-aggregate-v1.schema.json`.
Aggregation partitions by exact benchmark name and canonical dimensions, sums
counts and histogram buckets, and derives percentiles after merging. It never
averages per-process percentiles.

## Build the pinned measurement image

```bash
docker build --platform linux/amd64 \
  --file docker/cpp/Dockerfile \
  --tag liveroute-planner-service:measurement .
docker image inspect liveroute-planner-service:measurement --format '{{.Id}}'
```

Record the resulting image ID with every run. Do not compare results whose
compiler, build type, architecture, fixture, search budgets, provider dataset,
cache policy, or planner policy dimensions differ.

## Smoke and runtime load checks

```bash
docker run --rm \
  --entrypoint /workspace/build/liveroute_smoke_bench \
  liveroute-planner-service:measurement

for profile in many-trips hot-trip bursty-gps provider-timeout; do
  docker run --rm \
    --entrypoint /workspace/build/liveroute_loadgen \
    liveroute-planner-service:measurement \
    --profile "$profile" --seed 1 --events 1000
done
```

The load generator reports queue/admission outcomes and per-stage p50/p95/p99.
Provider-timeout is an injected failure profile. A runtime load result is not a
browser-to-proposal benchmark.

The live WebSocket path requires the local backend and token described in the
README:

```bash
LIVEROUTE_DEV_TOKEN_FILE=/absolute/path/to/token \
  bash scripts/check-websocket-load.sh
```

## Allocation suite

The allocation suite performs five independent processes and emits one raw
artifact per suffix in each process:

```bash
bash scripts/check-planner-allocation.sh \
  --image liveroute-planner-service:measurement \
  --variant candidate \
  --output-dir artifacts/benchmarks/local-allocation
```

`baseline` and `candidate` label compatible source/image variants; changing the
label does not recreate an earlier build. Reproducing the recorded allocation
comparison requires the exact images and artifacts identified in
`plans/summaries/optimization-evidence-ledger.md`.

Aggregate raw artifacts only after schema validation:

```bash
python3 scripts/aggregate-benchmarks.py \
  --output artifacts/benchmarks/local-allocation/aggregate.json \
  artifacts/benchmarks/local-allocation/planner-allocation-v1-*.json
```

## Layout and tail suites

```bash
bash scripts/check-planner-layout-timing.sh \
  --image liveroute-planner-service:measurement \
  --output-dir artifacts/benchmarks/local-layout

bash scripts/check-planner-tail.sh \
  --image liveroute-planner-service:measurement \
  --output-dir artifacts/benchmarks/local-tail \
  --combined-mask 7
```

The layout suite compares the ordinary object layout with a column-based
alternative. Native timing must pass before the alternative is profiled; a
failing candidate is not retained. The tail suite creates 125 raw artifacts and
an aggregate, then `scripts/evaluate-planner-tail.py` applies the exact integer
acceptance rules.

## Reporting rules

- State whether a number covers planning only, one runtime stage, route lookup,
  internal network work, or the full browser-to-result path.
- Report fixed-bucket percentiles as bucket bounds; do not imply finer
  precision.
- Include completed operations, throughput, deadline/overflow counters,
  expansions/candidates, and canonical-result digests with latency.
- Treat neutral or failed experiments as evidence, not as improvements.
- Never tune gates after viewing a candidate result.
- Keep generated artifacts under `artifacts/benchmarks/`; they are intentionally
  ignored by Git. Promote only reviewed summaries and digests into documentation.
