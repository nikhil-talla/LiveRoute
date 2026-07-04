The **low-latency C++ systems programming** part is the runtime that receives live trip events, updates state, runs the planner, and returns a new itinerary under a strict deadline.

The travel UI, PostgreSQL schema, authentication, and trip CRUD are general SWE. The following pieces are the actual low-latency systems work.

## 1. The C++ online serving path

The hot path is:

```text
location event arrives
        ↓
deserialize request
        ↓
find trip state
        ↓
apply event
        ↓
determine whether replanning is needed
        ↓
run incremental planner
        ↓
serialize revised plan
        ↓
send response
```

You would define an end-to-end latency target such as:

```text
p50 < 5 ms
p95 < 15 ms
p99 < 30 ms
```

Then measure where those milliseconds go.

That creates real work involving:

* Request dispatch.
* Serialization overhead.
* Queueing delay.
* Synchronization.
* Memory allocation.
* Cache misses.
* Planner execution time.
* Tail latency.

This is the central low-latency component.

---

## 2. Incremental replanning instead of full recomputation

A basic implementation might rebuild the entire itinerary after every location update.

A low-latency implementation only recomputes the affected suffix:

```text
Completed:
Hotel → Museum

Current:
Museum visit

Affected future:
Lunch → Park → Train
```

If the museum ends late, you only re-evaluate:

```text
Lunch → Park → Train
```

rather than every activity in the trip.

This relates to low latency because you are reducing:

* Algorithmic work.
* Number of candidate schedules evaluated.
* Route-cache lookups.
* Memory allocations.
* Response time variance.

You could benchmark:

| Algorithm                  | p99 latency | Candidates evaluated |
| -------------------------- | ----------: | -------------------: |
| Full recomputation         |       42 ms |               18,000 |
| Suffix recomputation       |       14 ms |                4,800 |
| Incremental + cached state |        7 ms |                1,900 |

That is a legitimate C++ optimization story.

---

## 3. Bounded thread pool and work queues

The service processes many trips concurrently.

A naïve implementation might create one thread per request. That causes:

* Thread creation overhead.
* Context switching.
* Unbounded memory usage.
* Poor behavior under load.

Instead, build:

```text
network threads
      ↓
bounded request queue
      ↓
fixed C++ worker pool
      ↓
planning engine
```

Systems concepts involved:

* `std::jthread`
* `std::atomic`
* Condition variables.
* Bounded producer-consumer queues.
* Worker affinity.
* Queue contention.
* Wake-up overhead.
* Graceful shutdown.
* Request cancellation.

You would tune:

* Number of network threads.
* Number of planner workers.
* Queue capacity.
* Work distribution.
* Spin versus block behavior.
* Per-worker scratch memory.

This is directly related to low-latency C++ service design.

---

## 4. Trip-state sharding

Multiple events may arrive for the same trip.

A naïve design might put all trip state behind one global mutex:

```cpp
std::mutex global_trip_mutex;
std::unordered_map<TripId, TripState> trips;
```

That becomes a contention bottleneck.

Instead:

```cpp
shard = hash(trip_id) % shard_count;
```

Each shard owns a subset of trips and has its own queue or lock.

Benefits:

* Different trips execute concurrently.
* Events for one trip remain ordered.
* Less lock contention.
* Better cache locality.
* Predictable state ownership.

You can compare:

* Global mutex.
* Per-trip mutex.
* Sharded mutexes.
* Single-writer shard queues.

Measure:

* Lock wait time.
* p99 latency.
* Throughput.
* Cache misses.
* Performance as worker count increases.

This is one of the strongest systems-programming aspects.

---

## 5. GPS-event coalescing and overload control

A phone might send location updates faster than the planner can use them.

Suppose these are queued:

```text
Location at 10:00:01
Location at 10:00:02
Location at 10:00:03
Location at 10:00:04
Location at 10:00:05
```

If no important boundary was crossed, the planner may only need the newest update.

Instead of processing all five, replace them with:

```text
Latest location at 10:00:05
```

This is useful for:

* Reducing queue depth.
* Reducing wasted CPU.
* Preventing stale work.
* Lowering tail latency.
* Maintaining freshness during bursts.

Related systems concepts:

* Backpressure.
* Load shedding.
* Work coalescing.
* Stale-request cancellation.
* Latest-value semantics.
* Priority queues.

This is similar to how real-time serving systems discard obsolete work.

---

## 6. Deadlines and cancellation

A replan is only useful if it arrives quickly.

Every request should carry a deadline:

```cpp
ReplanResult replan(
    const TripState& trip,
    std::chrono::steady_clock::time_point deadline,
    std::stop_token stop_token);
```

The planner periodically checks:

```cpp
if (stop_token.stop_requested() ||
    std::chrono::steady_clock::now() >= deadline) {
    return best_plan_found_so_far;
}
```

This lets the system:

* Stop expensive searches.
* Return the best partial result.
* Cancel outdated replans.
* Avoid wasting CPU on disconnected users.
* Protect p99 latency.

Low-latency services often prefer a good answer within 20 ms over a theoretically optimal answer after 500 ms.

That tradeoff is highly relevant to search and recommendation infrastructure.

---

## 7. Tail-latency optimization

Average latency is not enough.

Suppose:

```text
Average: 4 ms
p95:     12 ms
p99:     140 ms
```

The system feels unreliable even though the average is good.

You would investigate p99 spikes caused by:

* Lock contention.
* Queue buildup.
* Cache misses.
* Route-provider delays.
* Memory allocation.
* Page faults.
* Logging.
* Oversized searches.
* CPU scheduling.

Then introduce:

* Work budgets.
* Cache warming.
* Bounded search.
* Preallocated buffers.
* Request prioritization.
* Slow-provider timeouts.
* Reduced synchronous logging.

Tail latency is one of the clearest connections to low-latency infrastructure engineering.

---

## 8. Data-oriented planner implementation

The planner may evaluate thousands of candidate activities repeatedly.

A pointer-heavy representation:

```cpp
struct ActivityNode {
    Activity* activity;
    std::vector<ActivityNode*> edges;
};
```

may produce poor locality.

A more cache-efficient representation might use:

```cpp
struct PlannerData {
    std::vector<int32_t> activity_ids;
    std::vector<int32_t> earliest_start;
    std::vector<int32_t> latest_finish;
    std::vector<float> utility;
    std::vector<float> duration;
};
```

This can improve:

* Sequential memory access.
* CPU cache utilization.
* Vectorization.
* Prefetching.
* Batch candidate scoring.

You can investigate with:

* `perf stat`
* Cache-miss counters.
* Flame graphs.
* Compiler vectorization reports.

This is classic optimized C++ work.

---

## 9. Allocation reduction

A planner that repeatedly creates vectors, strings, and candidate objects may spend significant time in the allocator.

For example:

```cpp
for (const Candidate& candidate : candidates) {
    std::vector<Activity> next_plan = candidate.plan;
    next_plan.push_back(activity);
}
```

This can create thousands of allocations.

Alternatives include:

* Preallocated candidate arrays.
* Reused scratch buffers.
* Object pools.
* `std::pmr::monotonic_buffer_resource`.
* Fixed-capacity containers.
* Index-based parent references instead of copying plans.

You would report:

```text
Before:
4,600 allocations per replan
p99 = 33 ms

After:
120 allocations per replan
p99 = 18 ms
```

That is a concrete optimized-C++ achievement.

---

## 10. Efficient caching

Travel-time lookups may dominate planning time.

A low-latency cache should consider:

* Fast key hashing.
* Sharded locks.
* Compact keys.
* Fixed memory limits.
* LRU or approximate LRU.
* TTL expiration.
* Cache-line contention.
* Hit-rate versus memory tradeoffs.

For example:

```cpp
struct RouteKey {
    uint32_t origin_cell;
    uint32_t destination_cell;
    uint16_t departure_bucket;
    uint8_t travel_mode;
};
```

This is better for the hot path than repeatedly hashing strings and full floating-point coordinates.

Measure:

* Cache lookup latency.
* Hit rate.
* Memory consumption.
* Provider calls avoided.
* Lock contention.
* p99 reduction.

---

## 11. Async networking and serialization

The C++ service may use asynchronous gRPC or another event-driven networking library.

Relevant work includes:

* Completion queues or event loops.
* Connection state machines.
* Nonblocking I/O.
* Request deadlines.
* Flow control.
* Buffer ownership.
* Protobuf serialization.
* Avoiding unnecessary request copies.
* Handling partial failures.

You could benchmark:

```text
JSON request:
1.8 KB, 21 µs deserialize

Protobuf request:
620 B, 4 µs deserialize
```

Serialization probably will not be the hardest part, but it belongs in the end-to-end latency budget.

---

## 12. Profiling and benchmarking

The project only becomes a low-latency systems project when you measure and optimize it.

Use:

```bash
perf record
perf report
perf stat
heaptrack
valgrind --tool=callgrind
```

And sanitizers:

```bash
-fsanitize=address
-fsanitize=undefined
-fsanitize=thread
```

Create benchmark categories for:

* Planner alone.
* Queue throughput.
* Cache lookup.
* Serialization.
* Event application.
* End-to-end service latency.
* Concurrent active trips.
* Overload behavior.

You should produce before-and-after results for multiple optimizations.

---

# What is not specifically low-latency C++ systems work

These are still useful, but they serve other project goals:

| Component                | Main category              |
| ------------------------ | -------------------------- |
| React itinerary editor   | Frontend/general SWE       |
| Authentication           | Backend/general SWE        |
| PostgreSQL trip storage  | Backend/data modeling      |
| Map visualization        | Product/frontend           |
| User preferences         | Product design             |
| Docker setup             | Development infrastructure |
| CI/CD                    | General SWE                |
| Reservation UI           | Product functionality      |
| Social itinerary sharing | General SWE/product        |
| Basic REST CRUD          | Backend fundamentals       |

The following are systems-oriented but not necessarily low latency:

| Component              | Main purpose                |
| ---------------------- | --------------------------- |
| Write-ahead log        | Durability and recovery     |
| Snapshots              | Recovery performance        |
| Event replay           | Correctness                 |
| Checksums              | Data integrity              |
| PostgreSQL persistence | Durability                  |
| Kafka                  | Distributed event transport |

They still strengthen the project, but they should not distract from the hot path.

# The core low-latency subsystem

The narrow version is:

```text
C++20 Live Replanning Service

- Async event ingestion
- Sharded trip-state ownership
- Bounded MPMC queues
- Fixed worker pool
- Incremental beam-search planner
- Request deadlines and cancellation
- GPS-event coalescing
- Sharded ETA cache
- Preallocated planner scratch memory
- p50/p95/p99 performance benchmarks
- perf-driven CPU and memory optimization
```

That alone is the low-latency C++ systems project.

The frontend, database, WAL, and travel features provide the real-world setting and broader SWE credibility.

The strongest recruiting claim would therefore not be:

> Built a travel itinerary application in C++.

It would be:

> Built and optimized a multithreaded C++20 online planning service that incrementally replanned live itineraries under bounded latency, using sharded state ownership, request cancellation, event coalescing, cache-efficient data structures, and profiler-guided allocation reduction.
