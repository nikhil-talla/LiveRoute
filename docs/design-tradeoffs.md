# Design Tradeoffs

## One global lock versus one owner per trip

A single lock would make ordering obvious but couples unrelated trips and turns
one slow update into a system-wide delay. Each trip is assigned deterministically
to one worker. That worker owns the trip's mutable state and processes it in
order, while different trips progress concurrently.

The cost is more explicit message passing: route and planning completions return
to the owning worker, and version checks must reject stale work. This was chosen
for bounded concurrency and clear ownership; it does not eliminate every lock.

## Full recomputation versus suffix replanning

Completed activities and the user's progress boundary are fixed planning
history. Recomputing the entire day wastes work and risks changing the past.
The planner therefore considers only the authoritative remaining part of the
current plan, starting from the user's current location and progress.

The planner may move flexible remaining events while preserving their duration
and travel separation, then skip lower-priority optional events if needed. This
reduces the search domain while retaining the full remaining-feasibility check.

## Always requesting routes versus a bounded route cache

Always calling OSRM is simple and fresh but repeats identical pair estimates and
amplifies provider latency. An unbounded cache would trade that problem for
uncontrolled memory and ambiguous staleness.

The system stores raw route-estimate pairs in a fixed 16-part, 131,072-entry/
64-MiB cache with deterministic coordinate, profile, and dataset keys. Fresh entries live
six hours. Data up to 24 hours old is considered only when OSRM is unavailable
and only if it completely covers the request. Bounded second-chance eviction was
selected to cap eviction work; the project does not claim it universally
outperforms exact LRU.

## Unbounded queues versus bounded admission

Unbounded queues make bursts appear successful while converting overload into
memory growth and arbitrarily stale work. Every runtime lane, executor, stream,
and response path has explicit capacity.

The tradeoff is visible overload behavior: work may be rejected, GPS telemetry
coalesced/dropped, or obsolete planning cancelled. Durable PostgreSQL delivery
records provide recovery storage rather than allowing unbounded in-memory work.

## Full optimal search versus bounded best-so-far search

Exhaustive scheduling can provide an optimality proof but has unsuitable tail
behavior as the remaining plan grows. The planner uses deterministic finite candidate generation,
beam width, candidate/expansion limits, deadline checks, and cancellation.

Hard-infeasible candidates are pruned. If search completes, it returns the best
candidate under the exact tie-breaking policy. If a deadline arrives after a
complete feasible candidate exists, it may return that candidate as
`BEST_SO_FAR`; without one, it returns the terminal status rather than claiming
false infeasibility. The consequence is bounded work without a global-optimum
claim.

## User authority versus automatic application

A system that automatically applied a planner result would simplify state flow
but make a heuristic search the authority over user intent. The system stores user plans and engine
proposals separately. Proposals are persisted before publication and become
current only after a fresh explicit user acceptance.

This requires proposal identity, source-version fencing, and acceptance
transactions, but makes failure and reconnect behavior explainable: C++ can be
unavailable without invalidating the user's committed plan.

## Aggressive optimization versus measurement gates

Private data-layout and tail-latency changes can look attractive in isolation
while harming small-plan tail latency or maintainability. The system accepts an
optimization only after
compatible correctness, latency, throughput, allocation, and work-count gates.

The reusable score workspace passed and was retained. The column-based layout
and three tail-latency experiments did not pass every gate and remain
disabled/reverted. Recording
negative results prevents an optimization narrative from outrunning evidence.
