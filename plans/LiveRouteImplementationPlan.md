# LiveRoute C++ Systems Implementation Plan

## Summary

Build LiveRoute as a C++20 low-latency live itinerary replanning service, with the C++ serving path as the core project and only minimal surrounding app infrastructure.

The cross-component v1 contracts are fixed in `plans/LiveRouteV1ArchitecturePlan.md` and `plans/LiveRouteV1ContractSpec.md`. This document describes implementation components; where wording differs, those contracts govern.

Primary goal:

> A multithreaded C++ service that receives live trip events, mirrors the user-authoritative current plan by shard, fetches local OSRM travel-time matrices, incrementally replans only the affected itinerary suffix, and returns an advisory proposal under bounded latency with p50/p95/p99 benchmarks.

V1 architecture:

```text
CLI/load/integration client <-> WebSocket <-> single backend gateway
                                                |-- PostgreSQL: current plans, proposals, command intents/outbox, leases
                                                `-- bounded gRPC/Protobuf stream pool
                                                      <-> single C++ callback server with in-memory active trips
  -> bounded ingress queue
  -> event priority lanes
  -> trip-id shard
  -> event dedupe/version check
  -> route/hours providers
  -> OSRM matrix provider
  -> incremental in-memory planner
  -> suggested itinerary suffix response
```

Latency targets:

- Planner-only: `p50 < 5 ms`, `p95 < 15 ms`, `p99 < 30 ms`
- Cache-hit service path: `p99 < 30 ms`
- OSRM-backed end-to-end path: measured separately because local routing latency is an external dependency
- Always report planner, queueing, serialization, OSRM, and total latency independently

## Key Interfaces

Use bidirectional streaming gRPC + Protocol Buffers between the backend and C++ service. The single V1 backend owns client sessions and durable PostgreSQL records; the single V1 C++ process owns active trip state in memory. Horizontal service replication is future work.

The backend is Go 1.26 using `net/http`, `coder/websocket`, gRPC-Go, `pgx/v5`, Goose SQL migrations, and draft-2020-12 JSON Schema validation. Dependency/version pinning and readiness rules follow `plans/LiveRouteV1ContractSpec.md`.

Initial stream:

```proto
service LiveRoutePlanner {
  rpc PlanTrips(stream PlannerStreamRequest)
      returns (stream PlannerStreamResponse);
}
```

Each request and response envelope must carry a backend-generated `request_id` for correlation. The stream protocol must support trip bootstrap/restore with the authoritative current plan, live event application, canonical-first current-plan mirroring, proposal acceptance/rejection, snapshot export, acknowledgements, and structured errors without exposing Protobuf types to the planner domain.

Request fields:

- `request_id`
- `trip_id`
- `runtime_epoch`
- `mutation_sequence`
- `observation_sequence`
- optional `expected_planner_state_version`
- optional `expected_trip_revision`
- `expires_at_unix_ms`
- exactly one payload defined by the architecture contract; event occurrence time belongs to `ApplyTripEvent`
- event priority derived by the service from event type and current trip state
- `ConfirmFinalizedMutations` carries PostgreSQL's cumulative finalized mutation watermark in its payload; envelope sequences remain zero

Response fields:

- `request_id`
- `trip_id`
- `runtime_epoch`
- `trip_revision`
- `planner_state_version`
- `accepted_mutation_sequence`
- `accepted_observation_sequence`
- exactly one response payload such as acknowledgement, plan proposal, snapshot, structured error, or pong; disposition/status and retryability live in the relevant payload
- `FinalizedMutationsAcknowledged` confirms the cumulative watermark idempotently
- `TripBootstrapped` confirms the exact current-plan id and accepted/finalized watermarks loaded, allowing verified canonical-mirror convergence

Transcribe the exact fields/numbers/enums in `plans/LiveRouteV1ContractSpec.md` into checked-in `.proto` definitions and WebSocket JSON Schemas before handlers. Buf `FILE` breaking checks, a descriptor baseline, frozen JSON schema digest, and positive/negative corpus prevent contract drift.

Keep the hot path transport-independent:

```cpp
ReplanResult apply_event_and_replan(
    TripState& state,
    const TripEvent& event,
    Deadline deadline,
    std::stop_token stop_token);
```

Event priority model:

```cpp
enum class EventPriority : std::uint8_t {
    Critical,
    High,
    Normal,
    Low
};
```

Default classification:

- Critical: canonical-first `TripEdited`/`CurrentPlanReplaced`, `ActivityStarted`, `ActivityCompleted`, `ActivitySkipped`, `ReservationChanged`, `MandatoryDeadlineChanged`, `UserAcceptedProposal`, `UserRejectedProposal`, `PlaceFoundClosed`
- High: `RouteDeviationDetected`, `OperatingHoursChanged` when it affects a remaining activity
- Normal/coalescible: `LocationUpdated`, `VelocityUpdated`, `HeadingUpdated`, `MinorETAChanged`
- Low/advisory: `RecommendationRefreshRequested`, `WeatherChanged`, `CrowdEstimateChanged`, `SocialUpdate`

Route provider interface:

```cpp
enum class TravelMode : std::uint8_t {
    Walking,
    Driving
};

struct RouteEstimate {
    std::chrono::seconds duration;
    std::uint32_t distance_meters;
    bool reachable;
};

class TravelTimeMatrix {
public:
    [[nodiscard]] const RouteEstimate& at(
        std::size_t origin,
        std::size_t destination) const;
};

class TravelTimeProvider {
public:
    virtual ~TravelTimeProvider() = default;

    virtual TravelTimeMatrix get_matrix(
        std::span<const Location> locations,
        TravelMode mode,
        std::chrono::system_clock::time_point departure_time,
        std::chrono::steady_clock::time_point deadline,
        std::stop_token stop_token) = 0;
};
```

Implement v1 provider:

```cpp
class OsrmTravelTimeProvider final : public TravelTimeProvider {
public:
    TravelTimeMatrix get_matrix(
        std::span<const Location> locations,
        TravelMode mode,
        std::chrono::system_clock::time_point departure_time,
        std::chrono::steady_clock::time_point deadline,
        std::stop_token stop_token) override;
};
```

The planner must never call OSRM, perform HTTP, parse JSON, log synchronously, or allocate heavily inside its candidate loop. It receives an immutable `TravelTimeMatrix` and performs only in-memory lookups.

Operating-hours provider interface:

```cpp
class PlaceHoursProvider {
public:
    virtual ~PlaceHoursProvider() = default;

    virtual HoursInfo get_hours(
        PlaceId place_id,
        LocalDateRange date_range,
        Deadline deadline,
        std::stop_token stop_token) = 0;
};
```

The provider returns normalized time windows and activity timing constraints. The planner only sees in-memory constraints such as open windows, reservation start/grace, min/preferred/max duration, and whether an activity is mandatory, movable, skippable, or shorten-able. The planner must not call Google Places, Yelp, OpenStreetMap, scrape websites, or parse provider responses while evaluating candidates.

V1 accepts IANA zones whose pinned tzdata entry includes country `US`. CLI/fixture/seed ingestion rejects DST gaps, requires an explicit valid offset for DST folds, expands overnight/exception hours, and converts local input to UTC Unix milliseconds. The backend revalidates normalized values; the planner operates only on UTC, and output carries the activity IANA zone for display conversion after replanning.

Operating-hours provider progression:

- V1: manual or seed-file hours
- V1.5: OpenStreetMap `opening_hours`
- V2: Google Places details/current opening hours
- Optional: Yelp business hours for restaurants

## Product and Architecture Decisions

Product focus:

- V1 focuses on the notification system and the C++ low-latency request-handling/replanning path.
- The user-selected current plan is the ultimate product authority. The frontend/CLI submits it through the backend to PostgreSQL; C++ mirrors it and produces suggestions rather than approving or silently replacing it.
- The product should notify the user when they are approaching lateness for the next event.
- Replanning is useful only when it supports a notification or gives the user a concrete alternative itinerary.
- V1 exercises the WebSocket gateway with CLI, load, and integration clients; it does not ship a user-facing frontend.
- V1.5 adds the React/TypeScript frontend with web and mobile-browser support, plus user-facing login.
- Native Android with Kotlin is a v2 target.
- Native iOS with Swift/SwiftUI is a v3 target.

Replanning behavior:

- Replan only the future events for the current day; completed events are immutable.
- V1 uses deadline-bounded beam search with pruning from the first functional planner implementation.
- Replanning inputs include current location, travel-time estimates, event operating hours, min/max desired event duration, reservation/fixed-time constraints, and user-provided event priority rank.
- The planner should consider whether lower-priority events can be cut, compressed, moved, or skipped.
- Reservations and explicitly fixed events should not move in v1 when feasible.
- Reservation rebooking is a v2 feature.
- ML/profile-specific behavior, such as learning which event types a user prefers to move, is v2.
- Multi-day route planning is v2: users provide many events with constraints, and the planner can use TSP-style heuristics plus time-window scheduling.
- V1 returns one stored advisory proposal when replanning is useful. V1.5 may return up to three good feasible proposals, while the user still chooses whether any becomes the current plan.

Example notification/replan flow:

```text
location or ETA-risk event arrives
  -> update progress and compute slack to next fixed/important event
  -> if slack is healthy, no full replan
  -> if slack is low, notify user and compute alternative suffix plans
  -> rank alternatives by event priority, lateness, travel time, and amount cut/compressed
  -> persist and return notification payload plus best proposal, or up to three proposals in v1.5
```

Trip sharding over a simple semaphore:

- A semaphore can cap the number of concurrent workers, but it does not define ownership of trip state.
- Trip sharding gives each trip a deterministic owner shard, which preserves per-trip event order and reduces shared mutable state.
- Shards allow different trips to run concurrently while keeping one trip's state mutations serialized.
- A semaphore alone still needs locks around the trip map and per-trip state; under load that can become a contention point.
- The preferred design is sharded state ownership plus bounded queues, with worker counts tuned to available CPU cores.

HTTP/WebSocket/gRPC boundary:

- V1 uses WebSockets between protocol clients and the backend for user plan creation/replacement, live events, notifications, persisted proposals, reconnects, and server-originated messages.
- V1 deploys one Go backend and one C++ planner process. The backend uses a fixed pool of long-lived bidirectional gRPC streams to that planner process.
- The backend translates WebSocket messages and PostgreSQL records into Protobuf stream envelopes; the C++ planner remains unaware of WebSocket, JSON, authentication, and database types.
- Flow control is bounded at both boundaries. Slow clients and a slow or reconnecting planner stream must not create unbounded buffers.

Persistence direction:

- PostgreSQL is part of v1 and is the durable source of truth for users, trips, immutable current-plan history, separately stored engine proposals/decisions, and recovery snapshots.
- The backend owns PostgreSQL access. The C++ service keeps active trip state in memory and is rehydrated from a versioned snapshot after restart.
- Use the architecture contract's runtime-first two-stage acknowledgement for live lifecycle/constraint mutations and proposal decisions, and the canonical-first `canonical_committed`/`runtime_synced` path for user trip/current-plan edits. Retain command-intent, immutable plan, and proposal rows for the trip lifetime while pruning covered outbox delivery rows. GPS telemetry is non-durable/latest-value, and versioned snapshots checkpoint resolved mutation watermarks asynchronously.
- Avoid putting database transactions in the C++ planner hot path or giving the planner direct database access.
- Firebase is not the preferred durable source of truth for this systems-focused backend because it hides more of the database/query/runtime behavior and is less aligned with explicit low-latency service design.
- Supabase is a hosted platform around PostgreSQL plus auth/storage/realtime features. It can be useful for product acceleration, but the core design should still treat PostgreSQL as the durable storage model and keep it outside the planner candidate loop.

`std::stop_token`:

- A stop token is C++20's cooperative cancellation signal, commonly passed to work running in `std::jthread`.
- LiveRoute uses it to stop planner search, provider calls, or queued replaceable work when the request deadline expires, its transport disconnects, or a higher-priority event supersedes it. An already recorded durable command is not cancelled merely because its WebSocket client disconnects.
- The planner should check the token periodically and return the best feasible plan found so far.

OSRM integration:

- OSRM's Table service is an HTTP/JSON API, not a gRPC service.
- The C++ `OsrmTravelTimeProvider` uses a bounded libcurl-multi wrapper, then parses JSON and converts the result into `TravelTimeMatrix`.
- The planner must not call OSRM directly and must not parse OSRM JSON.
- The backend gateway speaks WebSocket to clients and bidirectional gRPC to the C++ replanning service, but OSRM-to-C++ remains behind the `TravelTimeProvider` abstraction.
- There is no "gRPC with HTTP GET" path from OSRM; gRPC and HTTP GET are different protocols. For v1, use the documented HTTP GET Table endpoint from the provider adapter to local OSRM, then pass an immutable in-memory matrix into the planner.

V2 route provider:

- Google Routes API is a future provider adapter for live-traffic-aware ETAs and more routing modes.
- It should convert responses into the same `TravelTimeMatrix` interface so planner code does not change.

## Implementation Changes

Set up the C++ project:

- Use CMake, C++20, clang/gcc, `-Wall -Wextra -Wpedantic`, sanitizers, and release/profile builds.
- Start with lightweight CTest/simple executable tests and `std::chrono` benchmark harnesses; defer GoogleTest and Google Benchmark until the core model, planner API, and runtime API stabilize.
- Add a single-host Docker Compose workflow for the backend, C++ service, PostgreSQL, and separate car/foot OSRM instances; expose only the backend on loopback by default.
- Add Protobuf/gRPC generation, WebSocket JSON-Schema validation, a small CLI/load client, and local OSRM configuration documented for development.
- Suggested layout: `src/domain`, `src/planner`, `src/runtime`, `src/routing`, `src/server`, `bench`, `tests`, `proto`.

Implement the domain model:

- Define compact value types for `TripId`, `ActivityId`, `Location`, `TimeWindow`, `Activity`, `Reservation`, `TripEvent`, `TripState`, authoritative `CurrentPlan`, advisory `PlanProposal`, `ProposalSegment`, and `ReplanResult`.
- Add `PlaceId`, `ActivityTiming`, `HoursInfo`, `EventPriority`, and explicit event types for `PlaceFoundClosed`, `OperatingHoursChanged`, `RouteDeviationDetected`, and advisory refresh events.
- Store itinerary activities in index-based vectors rather than pointer-heavy graphs.
- Track `trip_revision`, `planner_state_version`, accepted mutation/observation sequences, completed prefix index, current activity, current location, authoritative current plan, separate latest active proposal, and remaining suffix.
- Enforce idempotency: duplicate events are accepted without double-applying state changes; stale events do not overwrite newer state.

Implement incremental planning:

- Implement a bounded beam-search planner over the remaining suffix as the first functional V1 replanner.
- Treat the current plan as the baseline chosen by the user. Planner output is a proposal and cannot update that baseline without a later user command.
- Preserve completed activities and fixed reservations when feasible.
- Allow flexible activities to be moved, shortened, skipped, or reordered according to explicit activity constraints.
- Score candidates using utility, lateness penalties, skipped-activity penalties, reservation protection, travel feasibility, and operating-hours feasibility.
- Use `PlannerScratch` with reusable vectors, candidate pools, and parent indices to avoid copying entire plans.
- Add `ReplanBudget` containing deadline, max candidates, beam width, max expansions, and stop token.
- Return best-so-far if the deadline or cancellation token fires.
- Enforce normalized time-window constraints in memory: activity start must fall inside an open window, finish must occur before close, reservations must be protected when feasible, and flexible activities may be shortened, moved, or skipped.

Implement OSRM integration:

- Use OSRM Table service through the `TravelTimeProvider` interface.
- Request only current location plus remaining activity locations.
- Convert OSRM JSON into an immutable dense `TravelTimeMatrix`.
- Support walking and driving only.
- Handle timeout, cancellation, unreachable routes, malformed responses, oversized responses, and partial failures.
- Initially use OSRM directly; add caching only after the uncached path works.

Implement runtime systems:

- Use a fixed set of `std::jthread` workers.
- Add bounded MPSC/MPMC queues for ingress and shard dispatch.
- Add priority lanes for critical, high, normal, and advisory work; prefer critical/high work while allowing bounded normal-work progress to avoid starvation.
- Partition trip ownership by `hash(trip_id) % shard_count`.
- Ensure one trip's events are processed in order by its owning shard.
- Run provider I/O and planner CPU work on separate bounded executors; shard owners never block on either.
- Reserve internal completion capacity before dispatching provider/planner work and reserve essential gRPC response capacity before admitting a state-changing request.
- Tag immutable planning snapshots with state version and planning generation; commit results only when both still match on the owner shard.
- Allow at most one running plan and one coalesced pending replan trigger per trip.
- Backend GPS handling is transport-only: assign epoch-scoped observation sequences and retain only the newest sample while disconnected/overloaded, with explicit coalesced/dropped status. The backend never infers route/geofence boundaries.
- On the owner shard, apply a non-stale latest observation, classify route/geofence/slack conditions against shard-owned state, promote a current boundary condition, and keep at most one pending ordinary-location replan trigger. A boundary that must never be lost is an explicit non-coalescible domain event rather than an intermediate GPS sample.
- Do not replan on every GPS update; trigger full replans only for route deviation, geofence crossing, material itinerary improvement, or deadline-slack risk.
- Use initial slack policy: `slack > 20 min` no full replan, `10-20 min` ETA/progress refresh, `< 10 min` full replan, `< 0 min` critical replan.
- Cancel or supersede lower-priority queued/running work when higher-priority events arrive, such as cancelling a recommendation refresh on `ActivityCompleted` or a location-based replan on `ReservationChanged`.
- Add exact overload behavior: use `RESOURCE_EXHAUSTED` for capacity, `DEADLINE_EXCEEDED` for an attempt with no valid result, `PROVIDER_UNAVAILABLE` for missing required provider data, or `OK` plus result-quality metadata when a valid lower-quality result exists.
- Treat `degraded` as result metadata rather than a status: valid best-so-far/stale-cache/not-advancing results use `OK` plus explicit quality fields; actual capacity/provider/durability failures use their exact status.
- Add graceful shutdown using `std::stop_token`.

Implement gRPC server:

- Use the modern C++ callback API.
- Implement the `PlanTrips` bidirectional stream with explicit connection lifecycle, per-message correlation, flow control, and reconnect behavior.
- Keep gRPC handlers thin: deserialize, validate, convert `expires_at_unix_ms` to a monotonic internal deadline, reserve response capacity, dispatch to shard, and serialize asynchronous responses.
- Propagate stream cancellation and per-message expiry into replaceable runtime/provider/planner work without abandoning accepted durable commands.
- Do not assume stream arrival order provides global or cross-reconnect event ordering; enforce per-trip sequence and state-version checks.
- Enforce current runtime lease epochs, at-least-once idempotency, contiguous durable mutation ordering, independent trip/planner version rules, and latest-value observation ordering.
- Apply `CurrentPlanReplaced` as a canonical-first authoritative mirror event: validate structural compatibility, replace the in-memory baseline, cancel older proposal work, and never reject it for optimality or feasibility.
- On higher epoch, discard all old telemetry/active proposals/work, let the backend mark pending durable proposal rows stale, and reset epoch-scoped observation/planner versions; on same-epoch reconnect, preserve the in-memory observation watermark.
- Accept cumulative `ConfirmFinalizedMutations` after PostgreSQL finalization; return `SNAPSHOT_NOT_READY` until accepted and finalized watermarks match.
- Return `TripBootstrapped` with the exact loaded current-plan id/watermarks; let the backend resolve multiple pending canonical mirrors only after a matching full canonical bootstrap.
- Bind a trip to its current stream during bootstrap. Load the PostgreSQL-authoritative `CurrentPlan`, route committed proposal results to that binding, and retain at most the latest proposal in `TripState` when disconnected. Never treat retained proposal output as current-plan authority.
- Return only the stable statuses and quality metadata defined in `plans/LiveRouteV1ContractSpec.md`; all old epoch/sequence/version/proposal cases are `STALE` with a structured reason.

Implement backend durability and WebSocket gateway:

- Use one Go 1.26 backend process in V1; do not add cross-instance routing or distributed fanout.
- Add complete `liveroute.v1` Protobuf schemas and WebSocket JSON Schemas before implementing handlers.
- Add Goose SQL migrations for every exact table/column/constraint/index in `plans/LiveRouteV1ContractSpec.md`, including development token digests, canonical trip/activity windows, immutable `itinerary_plans`, separate `plan_proposals`, `command_intents`, lease-neutral `planner_outbox`, two retained compatible snapshots, and runtime leases.
- Implement `create_trip`, `trip_edited` plus its complete resulting user plan, and `replace_current_plan` as canonical-first PostgreSQL transactions. Emit `canonical_committed` without waiting for C++, preserve the user state during C++ outage, and converge the ordered `TripEdited`/`CurrentPlanReplaced` mirror through replay or full bootstrap before later runtime-first dispatch.
- Validate user-plan structure and safety boundaries in Go, but do not reject a structurally valid plan for nonoptimality or routing/hours/reservation/deadline infeasibility. Never accept browser-supplied route/provider/planner metadata as authoritative.
- Persist exact `StoredPlanProposal` bytes before WebSocket publication, retain proposal/current-plan history separately, and update `current_plan_id` only after an explicit fresh user acceptance or direct user-authored replacement.
- Canonicalize validated durable commands with RFC 8785 and SHA-256; retain digest algorithm/digest/outcome for the trip lifetime and reject changed-payload key reuse.
- Acquire/renew leases using PostgreSQL time. Add the current epoch only when dispatching an outbox entry, including replay after restart.
- Generate a new gRPC `request_id` and attempt deadline per dispatch while preserving stable `event_id`/mutation/payload identity. Use capped full-jitter outbox retry without a fixed attempt cutoff; stop only on an acknowledged terminal/applied outcome, deletion, or explicit paused-internal repair. Logical expiry must be terminally resolved by C++ as `COMMAND_EXPIRED`.
- After PostgreSQL finalization, send the cumulative finalized mutation watermark to C++; snapshot only after acknowledgement or bootstrap convergence.
- Hold at most one latest proposal whose `source_accepted_mutation_sequence` is ahead of PostgreSQL finalization or awaiting proposal persistence; publish it only after the watermark covers it, its epoch/state/base-plan tuple remains current, and its proposal row commits.
- Use bounded per-connection admission, bounded per-trip pending-command queues, and bounded outbound buffers.
- Read the exact 43-character development token only from `/run/secrets/liveroute_dev_token`, store only its SHA-256 digest, authorize every trip message, enforce exact origin/close-code policy, and redact tokens/location/provider/database bodies.
- On reconnect, return canonical trip state and authoritative current plan, requested outstanding command outcomes, current runtime-sync state, and the latest stored pending proposal when one exists.
- Add PostgreSQL/Goose, OSRM Table, gRPC health, backend `/healthz`, and dependency-aware `/readyz` checks. Only Docker/Compose is required on the host.

Implement observability:

- Collect stage timings:
  - request deserialization
  - queue wait
  - event application
  - OSRM request
  - hours provider request
  - matrix conversion
  - planner
  - response serialization
  - total
- Track queue depth by priority, shard load, candidate count, route-cache hit rate once caching exists, hours-cache hit rate once caching exists, deadline misses, cancelled requests, duplicate/stale events, events dropped by priority, events coalesced, replan trigger count, replan cancellation count, OSRM failures, and hours-provider failures.
- Produce benchmark reports with p50/p95/p99 and throughput.

Optimize after correctness:

- Replace string-heavy route keys with compact coordinate-cell keys when adding cache.
- Add sharded route-matrix cache with TTL and fixed memory budget.
- Reduce planner allocations using `std::pmr`, reusable scratch buffers, and fixed-capacity candidate arrays where practical.
- Use `perf`, `heaptrack`, sanitizer builds, and flame graphs to justify each optimization with before/after numbers.

## Test Plan

Correctness tests:

- Applying a location event updates current state and version.
- Duplicate event does not duplicate state transitions.
- Stale event is rejected or ignored.
- Completed activities are never replanned.
- Mandatory reservations remain protected when feasible.
- Flexible activities may be moved, shortened, or skipped.
- No returned engine proposal has overlapping activities.
- No returned engine proposal violates operating-hours windows.
- Every proposed segment allows enough travel time from the previous location.
- No feasible itinerary returns a structured infeasible response.
- Deadline expiration returns `OK` + `BEST_SO_FAR` for a valid plan or the exact terminal/error status when no valid plan exists.
- `PlaceFoundClosed` triggers replanning.
- `OperatingHoursChanged` triggers replanning only when remaining-itinerary feasibility changes.
- Low-priority recommendation refresh can be dropped or cancelled under load.

OSRM-backed integration tests:

- Use fixed coordinates and a local OSRM instance.
- Museum-late scenario causes suffix replanning.
- User leaves hotel late.
- Current activity takes longer than expected.
- Flexible lunch becomes impossible.
- Destination unreachable.
- OSRM timeout.
- Hours provider timeout.
- Two events arrive out of order.
- Same event delivered twice.
- No replanning necessary.
- Place found closed.
- Operating hours change makes a remaining activity infeasible.

Cross-component recovery and contract tests:

- Complete Protobuf descriptors and WebSocket JSON Schemas pass compatibility checks.
- Connection-scoped WebSocket messages work without `trip_id`; trip-scoped messages require it.
- Unauthenticated, unauthorized, oversized, and disallowed-origin messages are rejected before domain work.
- A command retry returns its stored outcome after the covered outbox row has been pruned.
- Reusing one `message_id` with a different payload is rejected.
- Backend restart wraps replayed lease-neutral outbox work in the newly acquired epoch.
- Telemetry may advance planner state without causing an ordinary durable trip mutation to fail its trip-revision check.
- Essential response capacity is unavailable before admission, so no state mutation occurs.
- A proposal completing across stream reconnect is delivered on the current binding, persisted before publication, or discarded if its source becomes stale.
- Higher-epoch bootstrap discards old observations/active proposals/work, marks older pending proposal records stale, resets epoch-scoped sequences, and accepts the first new observation; same-epoch reconnect preserves the watermark.
- Initial user plan creation is atomic/idempotent and does not require C++ availability or approval.
- User trip/current-plan edits commit before runtime sync, remain authoritative through C++/stream failure, and converge by ordered mirror replay or full bootstrap before later runtime-first work.
- Structurally invalid user plans are rejected, while structurally valid but infeasible user plans remain current and generate warnings/proposals.
- Proposal persistence precedes WebSocket publication; generation, rejection, staleness, or persistence failure never changes the current plan.
- Fresh proposal acceptance creates a new immutable current-plan revision and marks the proposal accepted atomically; stale acceptance changes neither record, while an explicit replacement records `USER_AUTHORED`.
- Crashes at every boundary between intent commit, C++ acceptance, PostgreSQL finalization, finalization confirmation, and WebSocket acknowledgement converge without double application.
- Snapshot request racing an unresolved mutation returns `SNAPSHOT_NOT_READY`; a terminal rejection advances finalized/mutation watermarks without advancing trip revision.
- Per-attempt deadline retry and optional logical command expiry produce different contracted outcomes.
- Lease races/expiry, outbox claim expiry/duplicate claim, transaction rollback/deadlock retry, corrupt/incompatible snapshot fallback, and shutdown with in-flight durable/provider/planner work preserve recovery invariants.
- US DST gap/fold, explicit fold offset, non-DST zones, overnight windows, and exceptional closure normalization are tested.
- Every WebSocket version/unknown-field/extension/close/auth/origin/size case and every contracted OSRM malformed/error case has a test.
- Buf baseline, frozen JSON-schema digest/corpus for current-plan/proposal messages, previous database migration, and snapshot golden-fixture compatibility checks run in the standard check target.

Benchmark suites:

- Planner-only latency by suffix size and beam width.
- Allocation count per replan.
- Queue throughput under concurrent trips.
- Priority-lane throughput, starvation behavior, and drop/cancel rates.
- Same-trip ordered event processing.
- GPS coalescing under bursty updates.
- Protobuf serialization/deserialization latency.
- OSRM matrix request latency.
- Hours provider request/cache latency.
- End-to-end gRPC latency under normal load and overload.
- Shard scaling: global mutex baseline vs sharded ownership.

Acceptance criteria:

- Planner-only benchmark meets the target for representative trips.
- End-to-end report separates C++ planner time from OSRM time.
- No sanitizer failures in unit/integration tests.
- Thread sanitizer passes shard/queue tests.
- Benchmarks produce reproducible p50/p95/p99 tables.
- The final README explains the systems design and includes before/after optimization results.

## Assumptions

- The initial systems-design context is `plans/LiveRouteInitialPlan.md`.
- V1 is a single-host, single-backend, single-C++-process development/demo deployment with a WebSocket gateway and PostgreSQL durability; horizontal scaling and production deployment are future work.
- The V1 backend stack is Go 1.26, `coder/websocket`, gRPC-Go, `pgx/v5`, Goose, and draft-2020-12 JSON Schema validation.
- V1 uses CLI/load/integration WebSocket clients. The React/TypeScript frontend and user-facing login are V1.5.
- Bidirectional gRPC + Protobuf is the v1 backend-to-planner interface.
- OSRM is the first realistic travel-time provider.
- Manual or seed-file hours are the first operating-hours provider; external place APIs are future adapters.
- Google Routes, native mobile clients, reservation rebooking, ML personalization, and transit routing are future extensions.
- The core project story is low-latency C++ service design: sharded state, priority-aware bounded queues, cancellation, incremental planning, provider boundaries, cache-aware data layout, allocation reduction, and profiler-driven optimization.
