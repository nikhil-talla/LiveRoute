# LiveRoute Measured Optimization Ledger

## Purpose

This file records optimization decisions only after compatible measurements
exist. It is not a list of hoped-for improvements, illustrative benchmark
numbers, or unmeasured implementation work. The user-authoritative plan and all
correctness contracts remain unchanged by an optimization.

Raw measurements use
`schema/benchmark/liveroute-benchmark-v1.schema.json`; aggregates use
`schema/benchmark/liveroute-benchmark-aggregate-v1.schema.json`. Aggregation,
comparability, percentile, throughput, and Phase 18 allocation rules are
normative in `plans/LiveRouteV1ContractSpec.md`.

## Evidence Rules

An accepted entry must include:

- the optimization phase, date, exact change, and hypothesis;
- compatible baseline and candidate artifact run IDs and SHA-256 digests;
- the relevant dimensions, including fixture/policy version and suffix size;
- before/after p50, p95, p99 bucket, and throughput;
- before/after allocation calls and bytes per operation when the planner is in
  scope;
- before/after expansion/candidate counts and, for Phase 19, Callgrind
  instructions, data references, L1/last-level data misses, branches, and branch
  misses per admitted candidate;
- the raw Callgrind artifact digest, profiler image/tool/cache geometry, and GCC
  vectorization-report command/digest for a Phase 19 experiment;
- correctness/result-digest and relevant test results;
- the quantitative acceptance rule and the accept, reject, or revert decision.

Percentiles are derived from merged raw histogram buckets. Entries never average
per-run percentiles or compare mismatched hardware, build, workload, provider,
planner-policy, or cache-policy dimensions. A `null` percentile is written as
`>1000000 us`. Numbers copied from examples in planning documents are not
evidence.

## Accepted Optimizations

### Phase 18 reusable planner score workspace

- Phase/date: 18 / 2026-08-02
- Decision: accepted
- Change and hypothesis: `score_candidate` now accepts worker-owned
  `PlannerScoreScratch` storage and `run_beam_search` reuses it across
  candidate scores. Fixed-size stack buffers also remove validation vectors
  from the measured scope. The hypothesis was that repeated score-workspace
  allocations dominated the baseline allocation count and bytes without
  changing candidate semantics.
- Baseline aggregate: `artifacts/benchmarks/phase18-planner-allocation-baseline/planner-allocation-v1-baseline-aggregate.json`, SHA-256
  `3cfedc38ce41e8aaa354b133fffa6fca0954e785d95612f8d088d6c52be901ac`.
- Candidate aggregate: `artifacts/benchmarks/phase18-planner-allocation-candidate/planner-allocation-v1-candidate-aggregate.json`, SHA-256
  `6235bd93f352d30bf2cb0db0cf66e3ae7afa0be97485902e2a2fcc9e5363afc4`.
- Comparable dimensions: `planner-allocation-v1`, `RelWithDebInfo`, GNU
  13.3.0, x86_64, seed `1`, planner policy
  `liveroute-v1-lexicographic-1`, suffix sizes `4, 8, 16, 32, 64`, beam `32`,
  max candidates `4096`, max expansions `16384`, 60-second attempt deadline,
  ten warmups and 200 measured attempts per suffix, no route cache/provider/
  transport activity. Baseline image:
  `sha256:d444554d0353949feb48ea1ef97073d29385f22acdd89349a106ca471d53ab68`;
  candidate image:
  `sha256:88c7dac3be04525a3bdbce50af79921bfffeb51068058c07f71bd48ea81bba4b`.
- Latency and allocation comparison (merged five-process aggregates; p95/p99
  are inclusive histogram buckets):

| Suffix | Calls/op before → after | Bytes/op before → after | p50 us before → after | p95 bucket us before → after | p99 bucket us before → after | Throughput mops/s before → after |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 4 | 391 → 169 | 16,280 → 8,288 | 50 → 50 | 250 → 250 | 1,000 → 500 | 14,809,768 → 16,718,214 |
| 8 | 1,317 → 479 | 86,756 → 25,804 | 250 → 250 | 500 → 500 | 2,500 → 1,000 | 4,399,201 → 5,266,456 |
| 16 | 5,761 → 1,931 | 697,730 → 112,234 | 2,500 → 2,500 | 2,500 → 2,500 | 2,500 → 2,500 | 807,776 → 820,862 |
| 32 | 30,393 → 9,955 | 7,326,402 → 655,530 | 25,000 → 25,000 | 25,000 → 25,000 | 25,000 → 25,000 | 63,661 → 64,238 |
| 64 | 185,641 → 60,819 | 91,067,714 → 4,542,762 | 250,000 → 250,000 | 500,000 → 500,000 | 500,000 → 500,000 | 4,367 → 4,437 |

- Candidate counters: 0 deadline misses and 0 allocation-scope overflows for
  every suffix group; 1,000 completed operations per suffix.
- Canonical result digests matched the baseline on every warmup and measured
  attempt: suffix 4 `86ca355df8f32d42f5b2b3445d99c449b0ec908a34a9cb278ea89e483c4d4241`,
  8 `9abf9d98fe24545df2452fed317b2c869424db57f7b4db334a6bbc5f6b6727c9`,
  16 `13f206242deb30bf301460ec84b770d9420dedb79892256d880d5eaef6b17425`,
  32 `3b2f180b41f2e96e1e7c2a2a1f989f729d56762b3fdfd07c4aeb23350f909910`,
  64 `d53cc164aceca31e3600b3163c13caafa0d6a665e2c9fb176f54b7cb2f46e1d2`.
- Candidate raw artifact run IDs and SHA-256 digests:

| Suffix | Run ID | Raw SHA-256 |
| ---: | --- | --- |
| 4 | `32df737e-c6c8-4c4f-a1e5-8b57662488c3` | `c20fe4abb0b5b2d09b5b60c71e4ee72afb2da60f363cc63f87d6b289dc9d4b75` |
| 4 | `371762da-a426-466b-ab56-69edfedf5d4e` | `d177861ac5ef9aecd0bfc1e81b76740f04046745813c9fec3bb1344bc7c926ef` |
| 4 | `481338ba-dd6e-497b-ac82-8a5ea3e0e790` | `7ceb2edae45edbca7ef317ad656ba983f1cbc0c0ec31f02255ad793b91f9163f` |
| 4 | `73c0ccb0-87ed-466b-a1ce-401fe25ae928` | `8ae15cf7614cd4da367b66d5ba6920afac66771a9769abd77f55fd2d1b5e8ca3` |
| 4 | `9b5d71f3-0127-4581-a337-071bba50c735` | `e3c5c01d12634d538ad9b6059a08c9820814dca0404479057cdb859db96f3476` |
| 8 | `09ce3cf0-c3c4-4651-a037-0916703fe109` | `1cca1aec5da493895b63f64c8427b085c3b4dc77ec2ba355e8c67ea88d240171` |
| 8 | `13d33cfb-e7c3-4aa7-a9db-fbc4fb3c752a` | `97b9a988c0677a39b6ea8e8743efb941293956d7c2e4ca5bf57ecbc731affd2e` |
| 8 | `4c502e6d-4f9b-4525-a419-f69119319e91` | `a1f44f684e8bce0dc79a22f1c7966b58d5c1c5c76ec4179547d54295e46828bb` |
| 8 | `6e230bab-bf3d-4dd3-a475-0f1f4089305f` | `3a297503861d2e8a63ff5b5ca2c6b7a598297d12109d0f51457736f3f744b93e` |
| 8 | `ddcb83dd-f07c-4bdf-a3be-08a5c9cdc6fe` | `503b88a9a4cf6df2320257d7010b59eb0720c6cd64342a1c013480e35aeee826` |
| 16 | `13c06805-731d-4cde-a15f-7a84c38ce448` | `a07bff8bf48a2b9b32a215a1b49a05e7c004f6097dfb408a8a45c0ba6b4e6de7` |
| 16 | `1a1ae4fb-e8fa-4af3-ae03-a55c704ef817` | `9d9294dd2530e531d49128aef312dc36fe7cad9f72c1ab85e8f9712b79aaedaa` |
| 16 | `82673ff9-0b64-4a47-a642-048b584b597b` | `41a31b8293f0ff886aa9dad99d612ad984e6b954d1e0fe8a65487614cd2c77c6` |
| 16 | `e866674d-5320-4dc9-aa42-02b72608033d` | `cb92bd8b6d19405bf47936a5c9b236ce249c7ca7ff06f725c659feec738a27ed` |
| 16 | `ff3eb758-110f-434c-a0b8-dd0510250df4` | `a43355a98578a3619c24039c5b5b40cc30928d9be32417b92b73fa4330dd2d5a` |
| 32 | `3bfcac00-60fb-4f56-a7b4-846c22c9e189` | `8d23f164a8f2d9816fc4e1ab4775c573c402a645f10206639dce0570f03f24ab` |
| 32 | `61c9e2a6-992c-41d3-a3c1-9ff91f5fd2a3` | `f03a41e9eace44a1aef949d512468826c96142b5e832fd12b599160ce07c9da1` |
| 32 | `a93f7834-6db0-4d66-af21-dfafcb211f58` | `450da80f9bd9acc45fedebd3bbcf194f92d7d053685025b6be9bfc77c065ed5c` |
| 32 | `cec6c063-520f-43ca-af37-d91d25e52dc2` | `116df7ab66a79e0e3881d8076bea491a76b4be34d7d631a258720ee4bddd2019` |
| 32 | `efffbc3f-7c5d-4e48-a25f-20cfbdf90ed4` | `64c44a2544b8c197b7f9d39a985ea1d4010364101da07a0c2938bde696f903a4` |
| 64 | `8b319b99-e21e-4ebf-a367-2424be0e4e0f` | `569927f094ada747813dd48e73249d737171721f6759d0c4d72ff6286eec1ed6` |
| 64 | `c340d687-5fb7-4cdc-aaff-555bc84583b4` | `9c4268c933bae5cdfc419c53ffc17622f45bcb27c88d770913f229ea3f1eae7f` |
| 64 | `d760bd9a-1055-47b2-a234-590ffc86b8b1` | `f03e0a2f05caca7966f54feda307e202afdf9aae3963018d1251b3c689ef9009` |
| 64 | `ddd2175f-b59b-40d8-a1c5-2c1e687935cc` | `37d6dc32d177436245d6bb9cee741a0c61da0796960c937b6573ba381bb7aa8c` |
| 64 | `fcbac434-1137-4cde-a849-3849c0e51d07` | `1667aca5c35794694e32bf26a5f69f8aaf1a06d0dc36c81e1a9f56fccefca0e5` |
- Correctness/check results: all five processes completed; raw artifacts and
  aggregate passed their Draft 2020-12 schemas; focused score, input,
  replan, and allocation tests passed; result digests matched; no deadline or
  scope-overflow counters were recorded. The authoritative candidate image
  `sha256:8d1e28e2585512a00a371108bb5112919846950f5d4fabdff152583f5732d748`
  then passed all 42 pinned CTests and both existing C++ benchmarks.
- Acceptance rule and conclusion: accepted because every suffix had calls at
  most 50% of baseline, bytes at most 60%, throughput at least 95% of
  baseline, no individual suffix regression, no deadline/scope overflow, and
  identical canonical result digests.
- Maintainability or operational tradeoff: score scratch remains explicit
  worker-owned storage; the public no-scratch overload preserves its existing
  behavior by using a local scratch object. The fixed validation buffers rely
  only on the existing 64-activity input limit.

## Measured Experiments and Work Awaiting Comparative Evidence

### Phase 20 tail-latency experiments

- Phase/date: 20 / 2026-08-02
- Decision: all three individual experiments and the combined candidate were
  measured and rejected; the serving mask is `0`.
- Changes and hypotheses: the experiment harness independently measured (bit
  0) validate-once internal planner calls, (bit 1) a worker-owned protected
  activity bitmap, and (bit 2) partial sorting of only the retained beam. The
  hypotheses were respectively to remove repeated structural checks, remove a
  per-child allocation, and avoid ordering children that beam truncation would
  discard. The public standalone APIs retain full validation, and all bits are
  private computation strategies rather than planner-result changes.
- Aggregate: `artifacts/benchmarks/phase20-planner-tail/planner-tail-v1-aggregate.json`,
  SHA-256
  `7b0628b9396f823330d3c874b2f367c07cabbfe27e3ca71670c0b2f55d464373`.
  Its 125 schema-valid raw artifacts are retained under the same directory.
- Comparable dimensions: `planner-tail-v1`, image
  `sha256:22d56f14870a4fbc2ad0198fa15baaf7a06bbd9101ec62ef7995bd3e9665b801`,
  `RelWithDebInfo`, GNU 13.3.0, x86_64, seed `1`, AoS layout, planner policy
  `liveroute-v1-lexicographic-1`, suffixes `4, 8, 16, 32, 64`, beam `32`,
  max candidates `4096`, max expansions `16384`, 60-second benchmark
  deadline, 10 warmups and 200 measured attempts in each of five independent
  processes per variant. No provider, cache, transport, or database work was
  in the measured boundary.
- Aggregate results (throughput is millioperations/second; p99 is the inclusive
  histogram bucket):

| Variant | Suffix | Throughput | p99 us | Calls/op | Bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| baseline / mask 0 | 4 / 8 / 16 / 32 / 64 | 20,219,584 / 5,292,545 / 839,483 / 71,049 / 4,938 | 500 / 500 / 2,500 / 25,000 / 500,000 | 169 / 479 / 1,931 / 9,955 / 60,819 | 8,248 / 25,804 / 113,322 / 668,202 / 4,659,242 |
| validated-input / mask 1 | 4 / 8 / 16 / 32 / 64 | 37,263,377 / 7,514,051 / 3,339,455 / 547,438 / 79,857 | 100 / 1,000 / 1,000 / 5,000 / 25,000 | 169 / 479 / 1,931 / 9,955 / 60,819 | 8,248 / 25,804 / 113,322 / 668,202 / 4,659,242 |
| lower-bound-scratch / mask 2 | 4 / 8 / 16 / 32 / 64 | 21,714,583 / 4,772,609 / 793,590 / 66,753 / 4,647 | 250 / 1,000 / 2,500 / 25,000 / 500,000 | 159 / 443 / 1,795 / 9,427 / 58,739 | 8,208 / 25,516 / 111,146 / 651,306 / 4,526,122 |
| partial-beam-selection / mask 4 | 4 / 8 / 16 / 32 / 64 | 23,675,923 / 4,727,595 / 849,418 / 71,328 / 4,911 | 250 / 1,000 / 2,500 / 25,000 / 250,000 | 169 / 479 / 1,931 / 9,955 / 60,819 | 8,248 / 25,804 / 113,322 / 668,202 / 4,659,242 |
| combined / mask 7 | 4 / 8 / 16 / 32 / 64 | 43,114,598 / 7,653,041 / 3,685,127 / 624,911 / 86,242 | 100 / 1,000 / 1,000 / 5,000 / 25,000 | 159 / 443 / 1,795 / 9,427 / 58,739 | 8,208 / 25,516 / 111,146 / 651,306 / 4,526,122 |

- Correctness evidence: every corresponding group had the same canonical
  result digest, 10,000/36,000/136,000/528,000/2,080,000 expansions and
  4,000/8,000/16,000/32,000/64,000 admitted candidates; all deadline-miss and
  allocation-scope-overflow counters were zero. The focused mask-equivalence
  traversal test passed in the measurement image.
- Gate decisions: `validated-input` failed only because suffix 8 p99 was in the
  1,000us bucket versus the baseline's 500us bucket. `lower-bound-scratch`
  additionally fell below the 95% throughput guard at suffixes 8, 16, 32, and
  64 and did not reach a material-benefit gate. `partial-beam-selection` fell
  below 95% throughput and regressed p99 at suffix 8. The independently
  measured combined mask also regressed suffix-8 p99. The predeclared gates
  were not relaxed after observing the otherwise large validate-once
  throughput improvement.
- Reproducibility: `scripts/check-planner-tail.sh` captures and schema-validates
  the suite; `scripts/evaluate-planner-tail.py` performs the exact integer gate
  comparisons and reports the four rejection reasons. Experimental code stays
  available behind a disabled private mask solely for reproducibility and
  focused equivalence coverage.
- Final verification: mask-zero image
  `sha256:1ee25e8c5395d171abddaa2dd90dab744f3f0b293d78ac7faa5bb176cc8ff22e`
  passed all 42 pinned CTests plus the smoke and Protobuf serialization
  benchmarks. The many-trips, hot-trip, bursty-GPS, and provider-timeout
  1,000-event runtime profiles each drained all accepted work with zero queue
  drops or internal acknowledgements. Provider-timeout produced the expected
  1,000 deadline/provider failures; bursty GPS avoided all 1,000 idle telemetry
  replans; many-trips and hot-trip completed all started attempts with bounded
  cancellation/supersession.

### Phase 19 data-layout baseline and candidate

- Status: measured neutral and reverted from serving use. The accepted Phase 18
  build is the required `aos-v1` baseline; no SoA performance claim exists.

### Phase 19 SoA layout experiment

- Phase/date: 19 / 2026-08-02
- Decision: neutral/reverted
- Change and hypothesis: a private worker-owned `PlannerActivityColumns` view
  was prepared once per attempt and used for scoring fields, matrix indices,
  and binary stable-ordinal lookup. The hypothesis was that columnar access
  would improve planner throughput for large suffixes without changing work or
  result semantics. The serving default is restored to AoS.
- Timing aggregate: `artifacts/benchmarks/phase19-planner-layout-neutral/planner-layout-timing-v1-neutral-aggregate.json`, SHA-256
  `3376e1462ccbf5d2eabe2dac0c89db15120a905738ec8d6cbbb6ad91a167e4f3`.
- Dimensions: exact `planner-layout-timing-v1` fixture, seed `1`, suffixes
  `4, 8, 16, 32, 64`, five independent processes per layout, 10 warmups and
  200 measured attempts, beam `32`, max candidates `4096`, max expansions
  `16384`, 60-second deadline, no provider/transport/cache activity. Raw
  artifacts are retained in the same directory.
- Native comparison:

| Suffix | AoS throughput → SoA throughput (mops/s) | AoS p99 → SoA p99 (us) | AoS calls/op → SoA calls/op | AoS bytes/op → SoA bytes/op |
| ---: | ---: | ---: | ---: | ---: |
| 4 | 26,114,433 → 24,172,689 | 250 → 250 | 169 → 169 | 8,288 → 8,288 |
| 8 | 4,929,167 → 4,286,694 | 1,000 → 2,500 | 479 → 479 | 25,804 → 25,804 |
| 16 | 807,077 → 763,112 | 2,500 → 2,500 | 1,931 → 1,931 | 112,234 → 112,234 |
| 32 | 67,833 → 65,569 | 25,000 → 25,000 | 9,955 → 9,955 | 655,530 → 655,530 |
| 64 | 4,579 → 4,581 | 500,000 → 500,000 | 60,819 → 60,819 | 4,542,762 → 4,542,762 |

- Combined suffix `16/32/64` throughput was `12,800` mops/s for AoS versus
  `12,775` mops/s for SoA, or `99.79%` of baseline; the required floor was
  `105%`. The suffix-8 p99 also regressed from the 1,000us bucket to 2,500us.
- Expansions, admitted candidates, allocation counters, deadline counters,
  and canonical result digests were identical for corresponding groups. All
  raw artifacts and the aggregate passed their Draft 2020-12 schemas; the
  pinned candidate C++ tests passed before the neutral decision.
- Phase 19 was rejected at the mandatory native gate; Callgrind profiling was
  not run after the native failure because the SoA could not be retained under
  the contract. The SoA runtime default was disabled and the AoS
  representation remains authoritative.

### Phase 18 planner-allocation-v1 baseline capture

- Phase/date: 18 / 2026-08-02
- Decision: baseline captured; no optimization accepted yet
- Exact dimensions: verified pinned `RelWithDebInfo` image
  `sha256:d444554d0353949feb48ea1ef97073d29385f22acdd89349a106ca471d53ab68`,
  seed `1`, planner policy `liveroute-v1-lexicographic-1`, suffix sizes
  `4, 8, 16, 32, 64`, beam `32`, max candidates `4096`, max expansions
  `16384`, 60-second attempt deadline, ten warmups and 200 measured attempts
  per suffix. Five independent processes produced 25 raw artifacts.
- Aggregate artifact: `artifacts/benchmarks/phase18-planner-allocation-baseline/planner-allocation-v1-baseline-aggregate.json`, SHA-256
  `3cfedc38ce41e8aaa354b133fffa6fca0954e785d95612f8d088d6c52be901ac`.
- Baseline evidence by suffix (calls and bytes are exact merged totals divided
  by 1,000 completed operations; p99 is the inclusive histogram bucket):

| Suffix | Calls/replan | Bytes/replan | Planner p50 (us) | p95 bucket (us) | p99 bucket (us) | Throughput (mops/s) |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 4 | 391 | 16,280 | 50 | 250 | 1,000 | 14,809,768 |
| 8 | 1,317 | 86,756 | 250 | 500 | 2,500 | 4,399,201 |
| 16 | 5,761 | 697,730 | 2,500 | 2,500 | 2,500 | 807,776 |
| 32 | 30,393 | 7,326,402 | 25,000 | 25,000 | 25,000 | 63,661 |
| 64 | 185,641 | 91,067,714 | 250,000 | 500,000 | 500,000 | 4,367 |

- Canonical result digests checked on every warmup and measured attempt:
  suffix 4 `86ca355df8f32d42f5b2b3445d99c449b0ec908a34a9cb278ea89e483c4d4241`,
  8 `9abf9d98fe24545df2452fed317b2c869424db57f7b4db334a6bbc5f6b6727c9`,
  16 `13f206242deb30bf301460ec84b770d9420dedb79892256d880d5eaef6b17425`,
  32 `3b2f180b41f2e96e1e7c2a2a1f989f729d56762b3fdfd07c4aeb23350f909910`,
  64 `d53cc164aceca31e3600b3163c13caafa0d6a665e2c9fb176f54b7cb2f46e1d2`.
- Raw artifact run IDs and SHA-256 digests:

| Suffix | Run ID | Raw SHA-256 |
| ---: | --- | --- |
| 8 | `017a5e3f-ea3b-4100-a544-e7bdbf87f8b0` | `1d8685d783282620ddd8b76db834976fa30b43141476a62cb472824270b34bd0` |
| 16 | `16031b50-5cfa-48b2-a0d6-6cbb5d6a1994` | `37b0101bffdee7a1848355d23e9b3b7e5378d1f8ef5de59890b7c5a465de839b` |
| 16 | `20dc5cab-ecc3-43fd-a512-5fb3bb7b1b54` | `4e9757115ef5f3146d6f08da47bec693f851a63ec4ef93479b18c13ac6ff53a3` |
| 4 | `2b2f347b-d924-4e2a-a3a3-0ac22f52ffca` | `726c36aef7e8622b72223fee7c0ff6f181901753af4419ab2b5e1db6fcf17727` |
| 32 | `2ba06ae7-f515-4fa7-ac93-f78ff2fe3e09` | `2a2884f5d2063114cd337d4cff4fd1d0aa6465fee8d88037b367245906d84409` |
| 32 | `2c9716d6-dc80-49da-ab6c-97fbda3f2300` | `e9c64f9104dd1dc5597762c9d677a49b084c823ee42677887629095a19b61bae` |
| 64 | `3b9aa9d6-3b68-4e29-a9ca-6776acf560dd` | `f628f638527a118ac193be869e6cadf2c2ece46dbaebeca276b6c88e0e8ece21` |
| 8 | `4139e525-81fa-4547-ad28-a55d005d5637` | `c25631a6f7d4c2ce542cad6ebf0e3a54501f7cac30139ea597799b67fa49e030` |
| 8 | `4430e2d6-4daa-4d97-a70f-d41b37879874` | `c78f1305cbe656e445fd0a7f32d695536fa8ad732b71823081aefef01c871638` |
| 16 | `52811e4f-0a71-43e4-ab95-ae808be32ee7` | `304a9d2c73bc9cd98280010dbd94985022f8ab14a5a95c10df3cef3e653fcd24` |
| 64 | `5bdacff0-5b94-48b0-a174-8587f7c945d6` | `853468bde7ca8ed7af0bfb5e226217846a6815d65e0782d885da3529af8aeb91` |
| 16 | `78c59c20-7e25-41f2-ae63-d35bef435a18` | `589c7068897d9ce6989f9c45b28cf3050ca71dc2ef9a1b4ac544a101d5c62668` |
| 4 | `85d2fecd-2bc3-441e-a3e2-68976653f55c` | `4968fb1dfd20738e3d173a87feebfc3eac24f70f57aa95c8e7962c1624d9a018` |
| 8 | `9507492f-9d3c-4210-a4f9-493ad087b863` | `d1d03f0eb0e997bb694c601305ccbdfd3e82f5da51577018376961a6b7cc885c` |
| 64 | `b4c33394-c9b0-4c28-a677-761b795a5878` | `e9e569854db52ff858f0899b480bf79aee6cfdd8a1fd683ce578deb4c52fcdd5` |
| 64 | `b59cb4af-5e09-41f5-a2c0-31e07099d004` | `90e24f447ed8acbaa8713f91f0c9b084454114416bc75fc24174bc6e8c286804` |
| 32 | `bd7306d5-a01d-4cc0-a62a-862b4b351b69` | `7b7459c976a2ee644a442f7f6a5db3c453da205a31e61a4b1083e6d6db17aa59` |
| 4 | `c6f02f41-29fc-48e5-ac95-aa386554dc0e` | `3b3da205e57ac0047b1c61307e503cf905927c4a59f25f0afc9b46fe92506dc4` |
| 32 | `cc80fc9a-889c-4307-a081-601774b5b047` | `537b780db7ea23a87eb6df423640e1eef9724040f0defb1febcb8aab63d515d4` |
| 8 | `cca8b8d3-d33e-44be-a030-3735331407ff` | `48d31c13a3ffc771d56e1642bcbe6032d052fc4effcbbb818ed63230802b5a8f` |
| 4 | `cdd6d2a1-d3ad-4caf-a214-3f23245bc0a2` | `8d3240e9bbcac3258ab36b5c33766827c8fe42c27ed31b687a7e912503ed1e3b` |
| 32 | `d0bfb109-f248-4d21-a7f9-becc8fe8d4c3` | `0be6917617129e07a45aaa292b2f6e429a84e0db76281fd5ff87322def300799` |
| 4 | `d0f1fdc4-b7f0-470a-a458-4873283070c0` | `2a12d927b2b4db378a4853bbeee561d73a906f26af01673e52724e10c1a24e8b` |
| 64 | `d742bdfc-9de8-4f37-a04d-1f02de00809c` | `f7a849ad3165f25b48f4281f791719ee6359607c38d2b62c640c1807b7f09723` |
| 16 | `faa82313-4454-4233-a1be-c46939ed9de2` | `adc238f427caf6d1e918a64326de378121611269c776eb00f5e7661d4ebff64a` |

- Correctness/check results: raw artifacts and aggregate passed their Draft
  2020-12 schemas; all 42 pinned CTests and both existing C++ benchmarks passed;
  shell syntax and `git diff --check` passed.

### Phase 17 route-matrix cache

- Status: implemented and correctness/integration tested; no relative-latency
  claim is recorded yet.
- Required comparison: cache-disabled OSRM-backed path versus cold-miss and
  fresh-hit `liveroute-route-cache-v1` paths with compatible raw artifacts.
- Required metrics: route-cache lookup and total-path p50/p95/p99, OSRM request
  count/latency, throughput, hit/miss/eviction counts, and cache bytes/entries.

### Existing planner scratch reuse

- Status: implemented before the Phase 18 allocation baseline.
- Treatment: it is part of the baseline, not claimed retroactively as a measured
  Phase 18 improvement. Later scratch/capacity changes compare against that
  captured baseline.

## Experiment Entry Template

### `<optimization id and short name>`

- Phase/date:
- Decision: `accepted`, `rejected`, or `reverted`
- Change and hypothesis:
- Baseline artifact run IDs and SHA-256 digests:
- Candidate artifact run IDs and SHA-256 digests:
- Comparable dimensions:
- Latency before/after: p50, p95, p99 bucket, throughput
- Allocation before/after: calls/op, bytes/op
- Planner work before/after: expansions/op, admitted candidates/op
- Phase 19 profile before/after: instructions, data references, L1/LL data
  misses, branches, and branch misses per admitted candidate
- Phase 19 profiler/compiler artifacts: tool/image/cache geometry, raw
  Callgrind IDs/digests, vectorization-report command/digest/findings
- Other relevant counters/gauges:
- Correctness/result-digest and test results:
- Acceptance rule and conclusion:
- Maintainability or operational tradeoff:
