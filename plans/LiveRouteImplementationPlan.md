# LiveRoute C++ Systems Implementation Plan

## Summary

Build LiveRoute as a C++20 low-latency live itinerary replanning service, with the C++ serving path as the core project and only minimal surrounding app infrastructure.

Primary goal:

> A multithreaded C++ service that receives live trip events, owns trip state by shard, fetches local OSRM travel-time matrices, incrementally replans only the affected itinerary suffix, and returns a revised plan under bounded latency with p50/p95/p99 benchmarks.

Default architecture:

```text
gRPC client
  -> C++ gRPC callback server
  -> bounded ingress queue
  -> trip-id shard
  -> event dedupe/version check
  -> OSRM matrix provider
  -> incremental in-memory planner
  -> revised itinerary suffix response
```

Latency targets:

- Planner-only: `p50 < 5 ms`, `p95 < 15 ms`, `p99 < 30 ms`
- Cache-hit service path: `p99 < 30 ms`
- OSRM-backed end-to-end path: measured separately because local routing latency is an external dependency
- Always report planner, queueing, serialization, OSRM, and total latency independently

## Key Interfaces

Use gRPC + Protocol Buffers for the service boundary.

Initial RPC:

```proto
service LiveRoutePlanner {
  rpc ApplyEventAndReplan(ApplyEventRequest) returns (ApplyEventResponse);
}
```

Request fields:

- `trip_id`
- `sequence_number`
- `expected_state_version`
- `event_timestamp`
- `deadline_unix_ms`
- one event delta: location update, activity completed, activity delayed, reservation changed, travel delay

Response fields:

- `trip_id`
- `new_state_version`
- `accepted_sequence_number`
- `status`
- revised itinerary suffix
- completed/unchanged prefix summary
- per-stage latency metrics
- planner candidate count
- deadline/cancellation/degraded-result flags

Keep the hot path transport-independent:

```cpp
ReplanResult apply_event_and_replan(
    TripState& state,
    const TripEvent& event,
    Deadline deadline,
    std::stop_token stop_token);
```

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

## Implementation Changes

Set up the C++ project:

- Use CMake, C++20, clang/gcc, `-Wall -Wextra -Wpedantic`, sanitizers, and release/profile builds.
- Add GoogleTest for correctness tests and Google Benchmark for planner/queue/cache benchmarks.
- Add protobuf/gRPC generation, a small CLI load generator, and a local OSRM configuration documented for development.
- Suggested layout: `src/domain`, `src/planner`, `src/runtime`, `src/routing`, `src/server`, `bench`, `tests`, `proto`.

Implement the domain model:

- Define compact value types for `TripId`, `ActivityId`, `Location`, `TimeWindow`, `Activity`, `Reservation`, `TripEvent`, `TripState`, `ItinerarySegment`, and `ReplanResult`.
- Store itinerary activities in index-based vectors rather than pointer-heavy graphs.
- Track `state_version`, latest accepted sequence number, completed prefix index, current activity, current location, and remaining suffix.
- Enforce idempotency: duplicate events are accepted without double-applying state changes; stale events do not overwrite newer state.

Implement incremental planning:

- Start with a bounded beam-search planner over the remaining suffix.
- Preserve completed activities and fixed reservations when feasible.
- Allow flexible activities to be moved, shortened, skipped, or reordered according to explicit activity constraints.
- Score candidates using utility, lateness penalties, skipped-activity penalties, reservation protection, and travel feasibility.
- Use `PlannerScratch` with reusable vectors, candidate pools, and parent indices to avoid copying entire plans.
- Add `ReplanBudget` containing deadline, max candidates, beam width, max expansions, and stop token.
- Return best-so-far if the deadline or cancellation token fires.

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
- Partition trip ownership by `hash(trip_id) % shard_count`.
- Ensure one trip's events are processed in order by its owning shard.
- Add GPS-event coalescing: if multiple location updates are queued for the same trip and no boundary event is pending, keep only the newest.
- Add overload behavior: reject or degrade requests when queues are full, deadlines are already expired, or OSRM cannot return before deadline.
- Add graceful shutdown using `std::stop_token`.

Implement gRPC server:

- Use the modern C++ callback API.
- Keep gRPC handlers thin: deserialize, validate, assign deadline, dispatch to shard, serialize response.
- Propagate client cancellation and deadline into the runtime and planner.
- Return structured statuses for accepted, duplicate, stale, infeasible, degraded, deadline exceeded, and internal error.

Implement observability:

- Collect stage timings:
  - request deserialization
  - queue wait
  - event application
  - OSRM request
  - matrix conversion
  - planner
  - response serialization
  - total
- Track queue depth, shard load, candidate count, cache hit rate once caching exists, deadline misses, cancelled requests, duplicate/stale events, and OSRM failures.
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
- No returned itinerary has overlapping activities.
- Every segment allows enough travel time from the previous location.
- No feasible itinerary returns a structured infeasible response.
- Deadline expiration returns best-so-far or degraded result.

OSRM-backed integration tests:

- Use fixed coordinates and a local OSRM instance.
- Museum-late scenario causes suffix replanning.
- User leaves hotel late.
- Current activity takes longer than expected.
- Flexible lunch becomes impossible.
- Destination unreachable.
- OSRM timeout.
- Two events arrive out of order.
- Same event delivered twice.
- No replanning necessary.

Benchmark suites:

- Planner-only latency by suffix size and beam width.
- Allocation count per replan.
- Queue throughput under concurrent trips.
- Same-trip ordered event processing.
- GPS coalescing under bursty updates.
- Protobuf serialization/deserialization latency.
- OSRM matrix request latency.
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

- The existing source of truth is `context/LiveRouteInitialPlan.md`; the filename typo is left unchanged.
- Project scope is C++ service first, not a full consumer travel app.
- gRPC + protobuf is the initial serving interface.
- OSRM is the first realistic travel-time provider.
- Google Routes, frontend CRUD, authentication, PostgreSQL persistence, and transit routing are future extensions.
- The core project story is low-latency C++ service design: sharded state, bounded queues, cancellation, incremental planning, cache-aware data layout, allocation reduction, and profiler-driven optimization.
