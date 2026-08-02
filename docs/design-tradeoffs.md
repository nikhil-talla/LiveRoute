# V1 Design Tradeoffs

## Global mutex versus sharded ownership

A single mutex would make ordering obvious but couples unrelated trips and
turns one slow mutation/completion into global contention. V1 deterministically
assigns each trip to one shard. The shard owns that trip's mutable state and
processes it sequentially, while different trips progress concurrently.

The cost is more explicit message passing: provider/planner completions return
to the owner shard, and generation/version fences must reject stale work. This
was chosen for bounded concurrency and maintainable ownership, not because
sharding removes every lock.

## Full recomputation versus suffix replanning

Completed activities and the user's progress boundary are immutable planning
history. Recomputing the entire day wastes work and risks proposing changes to
the past. V1 plans only the authoritative remaining current-plan suffix, using
current location/progress as the boundary.

The planner may move flexible remaining events while preserving their duration
and travel separation, then skip lower-priority optional events if needed. This
reduces the search domain while retaining the full remaining-feasibility check.

## Uncached OSRM versus a bounded route cache

Always calling OSRM is simple and fresh but repeats identical pair estimates and
amplifies provider latency. An unbounded cache would trade that problem for
uncontrolled memory and ambiguous staleness.

V1 stores raw route-estimate pairs in a fixed 16-shard, 131,072-entry/64-MiB
cache with deterministic coordinate/profile/dataset keys. Fresh entries live
six hours. Data up to 24 hours old is considered only when OSRM is unavailable
and only if it completely covers the request. Bounded second-chance eviction was
selected to cap eviction work; the project does not claim it universally
outperforms exact LRU.

## Unbounded queues versus bounded admission

Unbounded queues make bursts appear successful while converting overload into
memory growth and arbitrarily stale work. Every runtime lane, executor, stream,
and response path in V1 has explicit capacity.

The tradeoff is visible overload behavior: work may be rejected, GPS telemetry
coalesced/dropped, or obsolete planning cancelled. Durable PostgreSQL outbox
rows are recovery storage rather than an excuse for unbounded in-memory work.

## Full optimal search versus bounded best-so-far search

Exhaustive scheduling can provide an optimality proof but has unsuitable tail
behavior as the suffix grows. V1 uses deterministic finite candidate generation,
beam width, candidate/expansion limits, deadline checks, and cancellation.

Hard-infeasible candidates are pruned. If search completes, it returns the best
candidate under the exact lexicographic policy. If a deadline arrives after a
complete feasible candidate exists, it may return that candidate as
`BEST_SO_FAR`; without one, it returns the terminal status rather than claiming
false infeasibility. The consequence is bounded work without a global-optimum
claim.

## User authority versus automatic application

Automatically applying an engine result would simplify state flow but makes a
heuristic search the authority over user intent. V1 stores user plans and engine
proposals separately. Proposals are persisted before publication and become
current only after a fresh explicit user acceptance.

This requires proposal identity, source-version fencing, and acceptance
transactions, but makes failure and reconnect behavior explainable: C++ can be
unavailable without invalidating the user's committed plan.

## Aggressive optimization versus measurement gates

Private layout and tail changes can look attractive in isolation while harming
small-suffix p99 or maintainability. V1 accepts an optimization only after
compatible correctness, latency, throughput, allocation, and work-count gates.

The reusable score workspace passed and was retained. SoA and three tail
experiments did not pass every gate and remain disabled/reverted. Recording
negative results prevents an optimization narrative from outrunning evidence.
