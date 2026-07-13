# LiveRoute C++ Systems Implementation Plan

## Summary

Build LiveRoute as a C++20 low-latency live itinerary replanning service, with the C++ serving path as the core project and only minimal surrounding app infrastructure.

Primary goal:

> A multithreaded C++ service that receives live trip events, owns trip state by shard, fetches local OSRM travel-time matrices, incrementally replans only the affected itinerary suffix, and returns a revised plan under bounded latency with p50/p95/p99 benchmarks.

V1 architecture:

```text
Browser <-> WebSocket <-> Backend gateway
                           |-- PostgreSQL: durable users, trips, saved plans, snapshots
                           `-- bidirectional gRPC/Protobuf stream
                                 <-> C++ gRPC callback server with in-memory active trip state
  -> bounded ingress queue
  -> event priority lanes
  -> trip-id shard
  -> event dedupe/version check
  -> route/hours providers
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

Use bidirectional streaming gRPC + Protocol Buffers between the backend and C++ service. The backend owns browser sessions and durable PostgreSQL records; the C++ service owns active trip state in memory.

Initial stream:

```proto
service LiveRoutePlanner {
  rpc PlanTrips(stream PlannerStreamRequest)
      returns (stream PlannerStreamResponse);
}
```

Each request and response envelope must carry a backend-generated `request_id` for correlation. The stream protocol must support trip bootstrap/restore, live event application, plan acceptance/rejection, snapshot export, acknowledgements, and structured errors without exposing Protobuf types to the planner domain.

Request fields:

- `trip_id`
- `sequence_number`
- `expected_state_version`
- `event_timestamp`
- `deadline_unix_ms`
- one event delta: location update, activity completed, activity delayed, reservation changed, travel delay
- event priority derived by the service from event type and current trip state

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

- Critical: `ActivityStarted`, `ActivityCompleted`, `ActivitySkipped`, `ReservationChanged`, `MandatoryDeadlineChanged`, `UserAcceptedPlan`, `UserRejectedPlan`, `PlaceFoundClosed`
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

Operating-hours provider progression:

- V1: manual or seed-file hours
- V1.5: OpenStreetMap `opening_hours`
- V2: Google Places details/current opening hours
- Optional: Yelp business hours for restaurants

## Product and Architecture Decisions

Product focus:

- V1 focuses on the notification system and the C++ low-latency request-handling/replanning path.
- The product should notify the user when they are approaching lateness for the next event.
- Replanning is useful only when it supports a notification or gives the user a concrete alternative itinerary.
- The frontend target is React/TypeScript with web and mobile-browser support.
- Native Android with Kotlin is a v2 target.
- Native iOS with Swift/SwiftUI is a v3 target.

Replanning behavior:

- Replan only the future events for the current day; completed events are immutable.
- V1 can start with greedy scheduling over time windows, then move to deadline-bounded branch-and-bound or beam search with pruning.
- Replanning inputs include current location, travel-time estimates, event operating hours, min/max desired event duration, reservation/fixed-time constraints, and user-provided event priority rank.
- The planner should consider whether lower-priority events can be cut, compressed, moved, or skipped.
- Reservations and explicitly fixed events should not move in v1 when feasible.
- Reservation rebooking is a v2 feature.
- ML/profile-specific behavior, such as learning which event types a user prefers to move, is v2.
- Multi-day route planning is v2: users provide many events with constraints, and the planner can use TSP-style heuristics plus time-window scheduling.
- V1.5 should return up to three good feasible plans when multiple alternatives exist, then let the user choose.

Example notification/replan flow:

```text
location or ETA-risk event arrives
  -> update progress and compute slack to next fixed/important event
  -> if slack is healthy, no full replan
  -> if slack is low, notify user and compute alternative suffix plans
  -> rank alternatives by event priority, lateness, travel time, and amount cut/compressed
  -> return notification payload plus best plan, or up to three plans in v1.5
```

Trip sharding over a simple semaphore:

- A semaphore can cap the number of concurrent workers, but it does not define ownership of trip state.
- Trip sharding gives each trip a deterministic owner shard, which preserves per-trip event order and reduces shared mutable state.
- Shards allow different trips to run concurrently while keeping one trip's state mutations serialized.
- A semaphore alone still needs locks around the trip map and per-trip state; under load that can become a contention point.
- The preferred design is sharded state ownership plus bounded queues, with worker counts tuned to available CPU cores.

HTTP/WebSocket/gRPC boundary:

- V1 uses WebSockets between browsers and the backend for live events, notifications, revised plans, reconnects, and server-originated messages.
- V1 uses a long-lived bidirectional gRPC stream between each backend instance and the C++ planner service.
- The backend translates WebSocket messages and PostgreSQL records into Protobuf stream envelopes; the C++ planner remains unaware of WebSocket, JSON, authentication, and database types.
- Flow control is bounded at both boundaries. Slow browser connections and a slow or reconnecting planner stream must not create unbounded buffers.

Persistence direction:

- PostgreSQL is part of v1 and is the durable source of truth for users, trips, saved plans, and recovery snapshots.
- The backend owns PostgreSQL access. The C++ service keeps active trip state in memory and is rehydrated from a versioned snapshot after restart or reassignment.
- Define an explicit durability boundary: which events/results must commit before acknowledgement, which GPS updates may be ephemeral or coalesced, and when snapshots are written.
- Avoid putting database transactions in the C++ planner hot path or giving the planner direct database access.
- Firebase is not the preferred durable source of truth for this systems-focused backend because it hides more of the database/query/runtime behavior and is less aligned with explicit low-latency service design.
- Supabase is a hosted platform around PostgreSQL plus auth/storage/realtime features. It can be useful for product acceleration, but the core design should still treat PostgreSQL as the durable storage model and keep it outside the planner candidate loop.

`std::stop_token`:

- A stop token is C++20's cooperative cancellation signal, commonly passed to work running in `std::jthread`.
- LiveRoute uses it to stop planner search, provider calls, or queued work when the request deadline expires, the client disconnects, or a higher-priority event supersedes the current work.
- The planner should check the token periodically and return the best feasible plan found so far.

OSRM integration:

- OSRM's Table service is an HTTP/JSON API, not a gRPC service.
- The C++ `OsrmTravelTimeProvider` should call OSRM with a C++ HTTP client such as libcurl or Boost.Beast, then parse JSON and convert the result into `TravelTimeMatrix`.
- The planner must not call OSRM directly and must not parse OSRM JSON.
- The backend gateway speaks WebSocket to browsers and bidirectional gRPC to the C++ replanning service, but OSRM-to-C++ remains behind the `TravelTimeProvider` abstraction.
- There is no "gRPC with HTTP GET" path from OSRM; gRPC and HTTP GET are different protocols. For v1, use HTTP GET/POST from the provider adapter to local OSRM, then pass an immutable in-memory matrix into the planner.

V2 route provider:

- Google Routes API is a future provider adapter for live-traffic-aware ETAs and more routing modes.
- It should convert responses into the same `TravelTimeMatrix` interface so planner code does not change.

## Implementation Changes

Set up the C++ project:

- Use CMake, C++20, clang/gcc, `-Wall -Wextra -Wpedantic`, sanitizers, and release/profile builds.
- Start with lightweight CTest/simple executable tests and `std::chrono` benchmark harnesses; defer GoogleTest and Google Benchmark until the core model, planner API, and runtime API stabilize.
- Add protobuf/gRPC generation, a small CLI load generator, and a local OSRM configuration documented for development.
- Suggested layout: `src/domain`, `src/planner`, `src/runtime`, `src/routing`, `src/server`, `bench`, `tests`, `proto`.

Implement the domain model:

- Define compact value types for `TripId`, `ActivityId`, `Location`, `TimeWindow`, `Activity`, `Reservation`, `TripEvent`, `TripState`, `ItinerarySegment`, and `ReplanResult`.
- Add `PlaceId`, `ActivityTiming`, `HoursInfo`, `EventPriority`, and explicit event types for `PlaceFoundClosed`, `OperatingHoursChanged`, `RouteDeviationDetected`, and advisory refresh events.
- Store itinerary activities in index-based vectors rather than pointer-heavy graphs.
- Track `state_version`, latest accepted sequence number, completed prefix index, current activity, current location, and remaining suffix.
- Enforce idempotency: duplicate events are accepted without double-applying state changes; stale events do not overwrite newer state.

Implement incremental planning:

- Start with a bounded beam-search planner over the remaining suffix.
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
- Add GPS-event coalescing: keep at most one pending location update per trip, replacing older updates unless an older event crossed a geofence or route-deviation boundary.
- Do not replan on every GPS update; trigger full replans only for route deviation, geofence crossing, material itinerary improvement, or deadline-slack risk.
- Use initial slack policy: `slack > 20 min` no full replan, `10-20 min` ETA/progress refresh, `< 10 min` full replan, `< 0 min` critical replan.
- Cancel or supersede lower-priority queued/running work when higher-priority events arrive, such as cancelling a recommendation refresh on `ActivityCompleted` or a location-based replan on `ReservationChanged`.
- Add overload behavior: reject or degrade requests when queues are full, deadlines are already expired, or OSRM cannot return before deadline.
- Add graceful shutdown using `std::stop_token`.

Implement gRPC server:

- Use the modern C++ callback API.
- Implement the `PlanTrips` bidirectional stream with explicit connection lifecycle, per-message correlation, flow control, and reconnect behavior.
- Keep gRPC handlers thin: deserialize, validate, assign deadline, dispatch to shard, and serialize asynchronous responses.
- Propagate client cancellation and deadline into the runtime and planner.
- Do not assume stream arrival order provides global or cross-reconnect event ordering; enforce per-trip sequence and state-version checks.
- Return structured statuses for accepted, duplicate, stale, infeasible, degraded, deadline exceeded, and internal error.

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
- No returned itinerary has overlapping activities.
- No returned itinerary violates operating-hours windows.
- Every segment allows enough travel time from the previous location.
- No feasible itinerary returns a structured infeasible response.
- Deadline expiration returns best-so-far or degraded result.
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
- V1 includes a browser WebSocket gateway and PostgreSQL durability, while the low-latency C++ service remains the core systems component.
- Bidirectional gRPC + Protobuf is the v1 backend-to-planner interface.
- OSRM is the first realistic travel-time provider.
- Manual or seed-file hours are the first operating-hours provider; external place APIs are future adapters.
- Google Routes, native mobile clients, reservation rebooking, ML personalization, and transit routing are future extensions.
- The core project story is low-latency C++ service design: sharded state, priority-aware bounded queues, cancellation, incremental planning, provider boundaries, cache-aware data layout, allocation reduction, and profiler-driven optimization.
