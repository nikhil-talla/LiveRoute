# LiveRoute Performance Report

## Scope and interpretation

LiveRoute reports planner-only, runtime, provider, and end-to-end stages
separately. The tables below measure three planner improvements and the route cache.
They do not include browser message framing, database transactions, internal
network transport, or route-service latency, and must not be presented as
end-to-end response times.

Latency values are inclusive fixed histogram buckets, not interpolated samples.
Throughput is reported in millioperations per second (`mops/s`) by the versioned
artifact schema. Results apply to the exact pinned images, x86_64 environment,
fixture, compiler, search budgets, and five-process methodology recorded in the
optimization ledger; they are not universal hardware claims.

## Accepted: reusable planner workspace

The accepted workspace change replaced repeated scoring allocations with explicit worker-owned
`PlannerScoreScratch` and fixed validation buffers. Canonical output digests,
work limits, and correctness remained unchanged.

| Suffix | Calls/replan before → after | Bytes/replan before → after | p99 bucket before → after | Throughput mops/s before → after |
| ---: | ---: | ---: | ---: | ---: |
| 4 | 391 → 169 | 16,280 → 8,288 | 1,000 → 500 us | 14,809,768 → 16,718,214 |
| 8 | 1,317 → 479 | 86,756 → 25,804 | 2,500 → 1,000 us | 4,399,201 → 5,266,456 |
| 16 | 5,761 → 1,931 | 697,730 → 112,234 | 2,500 → 2,500 us | 807,776 → 820,862 |
| 32 | 30,393 → 9,955 | 7,326,402 → 655,530 | 25,000 → 25,000 us | 63,661 → 64,238 |
| 64 | 185,641 → 60,819 | 91,067,714 → 4,542,762 | 500,000 → 500,000 us | 4,367 → 4,437 |

All five suffix groups passed the predeclared allocation, per-suffix latency,
95% throughput, deadline, overflow, and canonical-result gates. The candidate
aggregate SHA-256 is
`6235bd93f352d30bf2cb0db0cf66e3ae7afa0be97485902e2a2fcc9e5363afc4`.

## Rejected: structure-of-arrays layout

The data-layout experiment measured a private column-based view against the
accepted ordinary object layout. Combined suffix 16/32/64 throughput reached 99.79% of the ordinary layout, below
the required 105%, and suffix-8 p99 regressed from the 1,000 us bucket to the
2,500 us bucket. The experiment was therefore reverted from serving use before
the profiler step required by the timing gate. The ordinary layout remains the
serving representation.

Timing aggregate SHA-256:
`3376e1462ccbf5d2eabe2dac0c89db15120a905738ec8d6cbbb6ad91a167e4f3`.

## Rejected: three tail-latency experiments

Three tail-latency experiments independently measured validate-once calls, protected-lower-bound
scratch, and partial beam selection, plus their combined mask. Every variant
preserved canonical results and work counts, but each failed at least one
predeclared p99, throughput, or material-benefit gate. Serving therefore uses
tail optimization mask `0`; the project claims three measured experiments, not
three improvements.

The final mask-zero image passed 42 CTests. The many-trips, hot-trip,
bursty-GPS, and provider-timeout runtime profiles each processed 1,000 events,
drained accepted work, and recorded no queue drops or internal acknowledgements.
Those runtime checks validate bounded behavior; they are not substituted for
the planner-only latency table.

Tail aggregate SHA-256:
`7b0628b9396f823330d3c874b2f367c07cabbfe27e3ca71670c0b2f55d464373`.

## Route cache

The route cache uses fixed memory, 16 partitions, deterministic coordinate keys,
six-hour fresh expiry, 24-hour maximum stale age, and bounded
64-slot second-chance eviction. Its purpose is to bound provider work and permit
a narrowly contracted `PROVIDER_UNAVAILABLE` stale fallback. No unmeasured
route-cache latency reduction is claimed here.

## Evidence

The detailed run IDs, raw and aggregate artifact paths, exact dimensions,
digests, gates, and rejected-result reasoning live in
`plans/summaries/optimization-evidence-ledger.md`. Reproduction and aggregation procedures are
documented in `docs/benchmarking.md`.
