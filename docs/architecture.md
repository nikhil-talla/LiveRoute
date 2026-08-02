# LiveRoute V1 Architecture

## Authority and service boundaries

The user's current plan is canonical. A client creates and
edits that plan through the Go backend, which validates and commits immutable
plan revisions in PostgreSQL. C++ mirrors the plan and may publish a separate
proposal. Only a fresh explicit acceptance or another user-authored replacement
creates a new current-plan revision.

```text
client
  <-> WebSocket JSON
Go backend
  <-> PostgreSQL
  <-> bidirectional gRPC/Protobuf
C++ planner service
  -> private OSRM car/foot Table services
```

The Go backend owns authentication, trip authorization, WebSocket lifecycle,
canonical transactions, durable command delivery, and translation between JSON
and Protobuf. PostgreSQL is the source of truth for users, trips, activity
definitions, current-plan history, proposals, command results, outbox rows,
leases, and compatible snapshots.

The C++ service owns only active runtime state. It validates epochs and ordered
sequences, applies accepted events on the trip's owner shard, acquires route
matrices outside search, runs the planner, and fences results that became stale
while work was in flight.

## Request lifecycle

1. The gateway authenticates the connection and authorizes the trip.
2. Strict JSON/schema validation constructs an internal backend request.
3. Canonical-first user edits commit to PostgreSQL before runtime mirroring.
   Runtime-first events first create a durable intent/outbox record.
4. The backend sends typed Protobuf work on a bounded bidirectional stream.
5. C++ validates protocol version, epoch, mutation/observation sequence, trip
   revision, planner-state version, and deadline before shard admission.
6. The owner shard applies the event. Feasibility-changing events schedule
   provider work; ordinary telemetry may update/coalesce without replanning.
7. A bounded provider executor obtains the immutable matrix. A separate bounded
   planner executor searches the remaining authoritative suffix.
8. Completion returns to the owner shard. A planning-generation fence discards
   results derived from superseded state.
9. The backend persists a proposal before publishing it. The user plan remains
   unchanged unless the user accepts the fresh proposal.

## Concurrency and overload

Trip IDs map deterministically to shards. One shard owns all mutable state for
a trip, avoiding cross-worker mutation and per-trip lock choreography. Each
shard processes a bounded four-lane priority queue with FIFO ordering within a
lane and configured fairness between lanes.

Provider I/O and planner CPU use separate fixed-worker bounded executors. A
slow OSRM request therefore cannot turn the candidate-search loop into an I/O
loop. Queue admission, response reservation, message sizes, active trips,
provider concurrency, search candidates, expansions, and deadlines all have
explicit limits. Overload produces contracted rejection, coalescing,
cancellation, or degradation rather than unbounded allocation.

Newer feasibility-changing work cancels or supersedes obsolete planning. GPS
bursts are coalesced. Results carry a planning generation and are committed only
if their source epoch, state version, mutation metadata, and generation still
match shard-owned state.

## Planner boundary

The candidate loop operates on internal C++ types:

- `BeamSearchInput` and authoritative suffix ordering;
- normalized UTC windows and fixed activity durations;
- immutable `TravelTimeMatrix` values;
- hard constraints, deterministic candidate ordering, and explicit budgets;
- a reusable worker-owned score workspace.

Events are never shortened in V1 suggestions. The planner first moves flexible
events while preserving duration and travel separation, then skips lower
priority optional events when movement cannot make the suffix feasible. The
search returns a complete proposal, a valid best-so-far proposal, a
no-new-proposal result, or an exact terminal/error status. It never returns a
known-invalid suggestion.

No JSON, Protobuf, WebSocket, gRPC, PostgreSQL, OSRM, provider payload, file I/O,
or blocking logging type is admitted into the candidate loop.

## Durability and recovery

Canonical-first changes remain valid if C++ is unavailable and converge through
ordered mirror replay or a full bootstrap. Runtime-first durable commands use
intent/outbox/finalization records so reconnect and process crashes do not
double-apply state.

A backend instance acquires a fenced trip lease and advances the runtime epoch.
Higher-epoch bootstrap invalidates old in-memory observations, proposals, and
work. Compatible snapshots accelerate recovery; newer uncovered commands replay
in order. PostgreSQL finalization watermarks tell C++ which mutation prefix is
durably resolved and safe to compact from replay state.

## Time, hours, and routing

Algorithms operate on signed UTC Unix milliseconds. Durable activities retain
their IANA US time-zone name for input normalization, audit, and display.
User-entered normalized `open_windows` are authoritative. The optional seeded
adapter can normalize fixture/local civil-time data with pinned tzdata, but the
normal replanning path performs no place-hours lookup.

OSRM is accessed behind `TravelTimeProvider`. Car and foot profiles run as
private services from a checksum-locked Rhode Island extract. The provider
enforces location, cell, request, response, concurrency, and deadline limits.
The Phase 17 cache stores bounded raw route-estimate pairs outside the planner;
only a fully covered provider-unavailable request may use contracted stale data.

## Wire compatibility and observability

WebSocket JSON is governed by split strict schemas, a canonical digest manifest,
and positive/negative corpora. Protobuf compatibility is checked against a
baseline descriptor; Go and C++ generated bindings are reproduced with pinned
toolchains.

Stage histograms distinguish adapter validation, queue wait, event application,
OSRM, matrix conversion, planner, serialization, and total request work.
WebSocket timing begins after text delivery; gRPC timing begins at `OnReadDone`
after framework decoding. This deliberately excludes hidden transport codec
work rather than mislabeling it as application deserialization.

For exact fields, transactions, status mappings, compatibility rules, and
limits, see `plans/LiveRouteV1ContractSpec.md`.
