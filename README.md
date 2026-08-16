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
`TravelTimeMatrix`. It does not parse browser messages, query PostgreSQL, call
OSRM, read files, or perform network requests. Route-service work and planner
CPU work use separate bounded worker pools, and each trip has one owner for its
mutable live state. This keeps slow route lookups, concurrent updates, and
stale results from changing the user's plan unexpectedly.

User-entered normalized UTC `open_windows` are the authoritative V1 hours
model. The checked-in seeded-hours provider is an optional importer and
deterministic fixture, not a serving-path place lookup.

## Local prerequisites

- Docker Engine or a compatible runtime with Docker Compose.
- The locked Rhode Island `.osm.pbf` file described in
  [docs/osrm.md](docs/osrm.md).
- For the backend profile, a regular development-token file containing exactly
  43 bytes, plus a local CSRF/HMAC key file containing at least 32 random
  bytes encoded as raw base64url. Keep both outside version control. For the
  default repository layout, set the files to mode `0640`, keep `secrets/`
  mode `0700`, and set `LIVEROUTE_SECRET_GID` to the host group that owns the
  files if it is not `1000`. Compose grants only that group to the non-root
  runtime services. `LIVEROUTE_DEV_TOKEN_FILE` and
  `LIVEROUTE_CSRF_HMAC_KEY_FILE` may point to alternate absolute paths.

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
When the Mapbox provider is configured, cold startup verifies and loads the
pinned 182 MB timezone-boundary dataset before readiness. Local acceptance
allows at most 300 seconds for that provider-enabled cold start; exceeding the
ceiling is a failure, not a reason to report partial readiness.

Important verification entry points:

```bash
bash scripts/check-hours-assets.sh
python3 scripts/check-http-contract.py
bash scripts/check-timezone-boundaries.sh
bash scripts/check-frontend.sh
python3 scripts/check-websocket-envelope.py
bash scripts/check-migrations.sh
bash scripts/check-cpp-proto-toolchain.sh
bash scripts/check-go-proto-generation.sh
bash scripts/check-backend-persistence.sh
bash scripts/check-backend-planner-stream.sh
LIVEROUTE_DEV_TOKEN_FILE=/absolute/path/to/liveroute_dev_token \
  bash scripts/check-websocket-load.sh
```

Some checks build multiple pinned images or start disposable PostgreSQL/OSRM
containers and therefore take longer than unit tests.

## Measured performance

The planner's reusable working memory reduced allocation calls and allocated
bytes at every measured remaining-plan size while preserving canonical results
and the declared throughput and tail-latency gates. For 64 remaining
activities, calls per replan fell from
185,641 to 60,819 and bytes from 91,067,714 to 4,542,762. These are
planner-scoped measurements—not WebSocket, database, gRPC, or OSRM latency.

The column-based data layout and three tail-latency ideas were also measured,
but rejected by the predeclared gates. The service therefore keeps its ordinary
activity layout and does not enable those experimental shortcuts. See
[docs/performance.md](docs/performance.md) for the summary and
[docs/optimizations.md](docs/optimizations.md) for the
detailed evidence ledger.

## Documentation

- [Architecture](docs/architecture.md)
- [Performance report](docs/performance.md)
- [Benchmark methodology](docs/benchmarking.md)
- [Local OSRM setup](docs/osrm.md)
- [Design tradeoffs](docs/design-tradeoffs.md)
- [Normative V1 contract](plans/LiveRouteV1ContractSpec.md)

The normative contract and architecture plan remain authoritative when a
reader-facing summary is less detailed.
