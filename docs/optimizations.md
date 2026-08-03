# Measured Performance Improvements

This page records performance changes only after they have been tested against
the same workload and checked for identical results. The user's plan and the
correctness rules are never changed just to improve a benchmark.

The measurements use the schemas in
`schema/benchmark/liveroute-benchmark-v1.schema.json` and
`schema/benchmark/liveroute-benchmark-aggregate-v1.schema.json`. Results are
combined from raw runs before percentiles are calculated; individual-run
percentiles are never averaged. Exact commands and acceptance rules are in
`docs/benchmarking.md` and the contract specification.

## Reusing planner working memory

Decision: accepted.

The planner now reuses a worker-owned score workspace while comparing possible
schedule changes. Fixed-size validation buffers also avoid repeated temporary
allocations. This reduced memory calls and bytes without changing the selected
schedule.

| Remaining activities | Memory calls before → after | Bytes before → after | p99 bucket before → after | Throughput before → after (mops/s) |
| ---: | ---: | ---: | ---: | ---: |
| 4 | 391 → 169 | 16,280 → 8,288 | 1,000 → 500 us | 14,809,768 → 16,718,214 |
| 8 | 1,317 → 479 | 86,756 → 25,804 | 2,500 → 1,000 us | 4,399,201 → 5,266,456 |
| 16 | 5,761 → 1,931 | 697,730 → 112,234 | 2,500 → 2,500 us | 807,776 → 820,862 |
| 32 | 30,393 → 9,955 | 7,326,402 → 655,530 | 25,000 → 25,000 us | 63,661 → 64,238 |
| 64 | 185,641 → 60,819 | 91,067,714 → 4,542,762 | 500,000 → 500,000 us | 4,367 → 4,437 |

All five groups passed the allocation, latency, throughput, deadline, overflow,
and result-equivalence checks. The candidate aggregate digest is
`6235bd93f352d30bf2cb0db0cf66e3ae7afa0be97485902e2a2fcc9e5363afc4`.

## Comparing two in-memory data layouts

Decision: rejected and reverted.

The planner briefly tested storing activity fields in separate columns instead
of ordinary activity objects. The column-based layout was not faster overall:
combined throughput for remaining-plan sizes 16, 32, and 64 was 99.79% of the
ordinary layout, below the required 105%. The p99 result for size 8 also became
worse. The ordinary layout remains the serving representation, and no profiler
claim is made for the rejected alternative.

The timing aggregate digest is
`3376e1462ccbf5d2eabe2dac0c89db15120a905738ec8d6cbbb6ad91a167e4f3`.

## Testing tail latency ideas

Decision: rejected; the serving configuration remains unchanged.

Three private experiments tried to reduce long-running searches by validating
input once, reusing a small lower-bound buffer, and sorting only the candidates
that could remain in the search beam. Each preserved results and work counts,
but every version failed at least one predeclared latency, throughput, or
material-benefit rule. The combined version also made the size-8 p99 worse.

The final unchanged configuration passed 42 C++ tests. Runtime profiles for
many trips, one busy trip, bursts of GPS updates, and route-service timeouts
processed 1,000 events each without queue drops or internal acknowledgement
errors. These checks demonstrate bounded behavior; they are not end-to-end
browser latency measurements.

The tail-suite aggregate digest is
`7b0628b9396f823330d3c874b2f367c07cabbfe27e3ca71670c0b2f55d464373`.

## Route-estimate cache

The route cache uses fixed memory divided into 16 independently managed parts.
It stores at most 131,072 raw route estimates (64 MiB), expires fresh entries
after six hours, and permits data up to 24 hours old only when the route service
is unavailable and the cache fully covers the request. Eviction work is bounded.

The cache is correctness- and integration-tested, but this documentation does
not claim a latency improvement until a compatible cache-disabled versus
cold-cache versus fresh-cache comparison is recorded.

## How to interpret these results

“p99” is the bucket containing 99% of observations, not an interpolated exact
sample. “mops/s” means millions of operations per second as represented by the
benchmark schema. These numbers apply only to the pinned image, compiler,
machine architecture, fixture, search limits, and five-process procedure used
for the runs. They are not promises about every machine or workload.
