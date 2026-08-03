# LiveRoute Architecture

## Authority and service boundaries

The user's current plan is authoritative. A client creates and edits that plan
through the Go backend, which validates it and stores each revision in
PostgreSQL. The C++ planner keeps a working copy and may publish a separate
suggestion. A suggestion changes the current plan only after the user accepts
it, or the user makes another edit.

```text
browser client
  <-> browser messages
Go backend
  <-> PostgreSQL
  <-> typed internal stream
C++ planner service
  -> private car/foot route services
```

The Go backend handles sign-in, trip permissions, the browser connection,
database transactions, reliable delivery of work, and translation between the
browser and planner formats. PostgreSQL is the source of truth for users,
trips, activities, plan history, suggestions, delivery records, ownership
leases, and recovery snapshots.

The C++ service owns only live working state. It checks ordering and ownership,
applies updates on the worker responsible for a trip, obtains travel-time data
before searching, runs the planner, and discards results made obsolete while
work was running.

## Request lifecycle

1. The gateway authenticates the connection and checks trip permissions.
2. Strict JSON/schema validation constructs an internal backend request.
3. User edits are committed to PostgreSQL before being copied to the planner.
   Events that begin in the live runtime first receive a durable database record
   so they can be recovered after a disconnect.
4. The backend sends typed work over a bounded two-way stream.
5. C++ checks the protocol version, ordering numbers, plan revision, state
   version, and deadline before accepting the work.
6. The trip's worker applies the event. Changes that can affect feasibility
   request travel data; ordinary location updates may be combined without a
   new plan.
7. A limited provider worker obtains an immutable travel-time matrix. A
   separate limited planner worker searches the remaining authoritative plan.
8. Completion returns to the trip's worker. A generation check discards results
   based on state that has since been replaced.
9. The backend persists a proposal before publishing it. The user plan remains
   unchanged unless the user accepts the fresh proposal.

## Concurrency and overload

Trip IDs map deterministically to workers. One worker owns all mutable state for
a trip, avoiding concurrent edits to the same trip. Each worker processes a
bounded four-lane priority queue, preserving order within each lane and using
configured fairness between lanes.

Travel-service requests and planner computation use separate fixed-size worker
pools. A slow OSRM request therefore cannot block the search itself. Queue
capacity, response size, active trips, provider concurrency, search work, and
deadlines all have explicit limits. Under load, work is rejected, combined,
cancelled, or degraded according to the contract instead of consuming memory
without a bound.

Newer feasibility-changing work cancels obsolete planning. Bursts of GPS
updates are combined. Results are committed only when their source version and
generation still match the trip state that owns them.

## Planner boundary

The search loop operates only on internal C++ data structures:

- `BeamSearchInput` and authoritative suffix ordering;
- normalized UTC windows and fixed activity durations;
- immutable `TravelTimeMatrix` values;
- hard constraints, deterministic candidate ordering, and explicit budgets;
- a reusable worker-owned score workspace.

Events are never shortened in suggestions. The planner first moves flexible
events while preserving duration and travel separation, then skips lower
priority optional events when movement cannot make the suffix feasible. The
search returns a complete proposal, a valid best-so-far proposal, a
no-new-proposal result, or an exact terminal/error status. It never returns a
known-invalid suggestion.

No browser data, database object, network message, OSRM response, file access,
or blocking log operation enters the search loop.

## Durability and recovery

User changes remain valid if C++ is unavailable and catch up through ordered
replay or a full reload. Live events use durable intent, delivery, and
finalization records so reconnects and process crashes do not apply the same
change twice.

One backend instance at a time owns a trip lease. When ownership changes, the
new instance advances the trip's runtime version, making old in-memory work
invalid. A compatible snapshot speeds recovery; newer commands are replayed in
order. PostgreSQL records the highest consecutive command known to be safely
resolved, allowing old replay records to be compacted.

## Time, hours, and routing

Algorithms operate on signed UTC Unix milliseconds. Durable activities retain
their IANA US time-zone name for input normalization, audit, and display.
User-entered normalized `open_windows` are authoritative. The optional seeded
adapter can normalize fixture/local civil-time data with pinned tzdata, but the
normal replanning path performs no place-hours lookup.

OSRM is accessed behind a travel-time provider interface. Separate car and foot
settings run as private services from a checksum-locked Rhode Island map
extract. The provider
enforces location, cell, request, response, concurrency, and deadline limits.
The route cache stores a bounded set of raw route estimates outside the
planner. Older estimates are used only when the provider is unavailable and
the cache fully covers the request.

## Wire compatibility and observability

Browser messages are governed by strict schemas and compatibility test data.
The internal message format is checked against a saved compatibility baseline,
and generated Go and C++ bindings are reproduced with fixed tool versions.

Stage histograms distinguish adapter validation, queue wait, event application,
OSRM, matrix conversion, planner, serialization, and total request work.
Browser timing begins after the message is delivered; internal-stream timing
begins after the networking layer has decoded it. This keeps hidden transport
work from being reported as application deserialization.

For exact fields, transactions, status mappings, compatibility rules, and
limits, see `plans/LiveRouteV1ContractSpec.md`.
