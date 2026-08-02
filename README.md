# LiveRoute

LiveRoute is a local, container-first itinerary replanning system. A user owns
the authoritative schedule; the C++20 engine evaluates live changes and offers
bounded, deterministic replacement suffixes as suggestions. It never silently
changes the user's plan.

V1 includes the Go WebSocket gateway, PostgreSQL durability, bidirectional
gRPC/Protobuf transport, sharded C++ runtime, local OSRM routing, load tools,
and compatibility/recovery tests. The React/TypeScript browser editor and
user-facing login are planned for V1.5.

## The hot path in five minutes

```text
CLI/load client
    |
    | versioned WebSocket JSON
    v
Go gateway ---- PostgreSQL
    |               |
    |               +-- user plans, proposals, command/outbox state,
    |                   snapshots, leases, and idempotent outcomes
    |
    | bidirectional gRPC + Protobuf
    v
C++ runtime shard
    |
    +-- validates epoch and ordered mutation/observation versions
    +-- owns mutable state for one trip on one shard
    +-- acquires a bounded OSRM matrix outside the planner loop
    +-- runs a deadline/cancellation-bounded suffix beam search
    +-- discards stale results by planning generation
    v
stored proposal -> WebSocket suggestion -> explicit user decision
```

The planner search loop sees only internal domain values and an immutable
`TravelTimeMatrix`. It does not parse JSON/Protobuf, query PostgreSQL, call
OSRM, read files, or perform RPCs. Provider work and planner CPU work use
separate bounded executors; trip mutation remains shard-owned.

User-entered normalized UTC `open_windows` are the authoritative V1 hours
model. The checked-in seeded-hours provider is an optional importer and
deterministic fixture, not a serving-path place lookup.

## Systems work demonstrated

- C++20 shard ownership, bounded priority queues, worker pools, cancellation,
  GPS coalescing, generation fencing, and stale-result rejection.
- A transport-independent finite beam search with hard feasibility constraints,
  deterministic ordering, explicit work budgets, and best-so-far behavior.
- PostgreSQL transactions for canonical user plans, separate engine proposals,
  outbox delivery, acknowledgements, idempotency, leases, and snapshots.
- Versioned WebSocket and Protobuf contracts with generated-binding and
  compatibility checks.
- Pinned container toolchains and local car/foot OSRM datasets.
- Schema-validated latency/allocation artifacts and measurement-gated
  optimization decisions, including documented rejected experiments.

## Local prerequisites

- Docker Engine or a compatible runtime with Docker Compose.
- The locked Rhode Island `.osm.pbf` file described in
  [docs/osrm.md](docs/osrm.md).
- For the backend profile, a regular development-token file containing exactly
  43 bytes. Keep it outside version control and set
  `LIVEROUTE_DEV_TOKEN_FILE` to its absolute path.

PostgreSQL, Go, C++ build tools, Protobuf, and OSRM run in containers; they do
not need to be installed on the host.

## Build and verify

Run the standalone pinned C++ gate:

```bash
docker compose --profile skeleton build planner-skeleton
docker compose --profile skeleton run --rm planner-skeleton
```

Run the complete local serving stack after installing the locked map and
configuring the token file:

```bash
export LIVEROUTE_DEV_TOKEN_FILE=/absolute/path/to/liveroute_dev_token
docker compose --profile backend up --build --wait
curl --fail http://127.0.0.1:8080/readyz
```

The backend is exposed only on `127.0.0.1:8080`. PostgreSQL, planner gRPC, and
both OSRM profiles remain private to the Compose network.

Important verification entry points:

```bash
bash scripts/check-hours-assets.sh
python3 scripts/check-websocket-envelope.py
bash scripts/check-cpp-proto-toolchain.sh
bash scripts/check-go-proto-generation.sh
bash scripts/check-backend-persistence.sh
bash scripts/check-backend-planner-stream.sh
```

Some checks build multiple pinned images or start disposable PostgreSQL/OSRM
containers and therefore take longer than unit tests.

## Measured performance

The accepted Phase 18 planner workspace reduced allocation calls and allocated
bytes at every measured suffix size while preserving canonical results and the
declared throughput/p99 gates. For suffix 64, calls per replan fell from
185,641 to 60,819 and bytes from 91,067,714 to 4,542,762. These are
planner-scoped measurements—not WebSocket, database, gRPC, or OSRM latency.

The Phase 19 SoA experiment and all three Phase 20 tail experiments were
measured but rejected by the predeclared gates, so serving retains AoS and tail
optimization mask `0`. See [docs/performance.md](docs/performance.md) for the
summary and [plans/summaries/optimizations.md](plans/summaries/optimizations.md)
for the evidence ledger.

## Documentation

- [Architecture](docs/architecture.md)
- [Performance report](docs/performance.md)
- [Benchmark methodology](docs/benchmarking.md)
- [Local OSRM setup](docs/osrm.md)
- [Design tradeoffs](docs/design-tradeoffs.md)
- [Normative V1 contract](plans/LiveRouteV1ContractSpec.md)
- [Implementation roadmap](plans/LiveRouteFeatureRoadmap.md)

The normative contract and architecture plan remain authoritative when a
reader-facing summary is less detailed.
