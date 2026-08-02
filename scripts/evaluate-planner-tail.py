#!/usr/bin/env python3
"""Evaluate a planner-tail-v1 aggregate against the Phase 20 gates."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


SUFFIXES = (4, 8, 16, 32, 64)
LARGE_SUFFIXES = (16, 32, 64)
CANDIDATES = (
    "validated-input",
    "lower-bound-scratch",
    "partial-beam-selection",
    "combined-candidate",
)


def at_least_ratio(
    candidate_numerator: int,
    candidate_denominator: int,
    baseline_numerator: int,
    baseline_denominator: int,
    percentage: int,
) -> bool:
    return (
        candidate_numerator * baseline_denominator * 100
        >= percentage * baseline_numerator * candidate_denominator
    )


def at_most_ratio(
    candidate_numerator: int,
    candidate_denominator: int,
    baseline_numerator: int,
    baseline_denominator: int,
    percentage: int,
) -> bool:
    return (
        candidate_numerator * baseline_denominator * 100
        <= percentage * baseline_numerator * candidate_denominator
    )


def combined(groups: dict[tuple[str, int], dict[str, Any]], variant: str,
             suffixes: tuple[int, ...], field: str) -> int:
    return sum(groups[(variant, suffix)][field] for suffix in suffixes)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("aggregate", type=Path)
    args = parser.parse_args()
    document = json.loads(args.aggregate.read_text(encoding="utf-8"))
    groups: dict[tuple[str, int], dict[str, Any]] = {}
    for group in document["groups"]:
        parameters = group["dimensions"]["parameters"]
        key = (parameters["variant"], parameters["suffix_size"])
        groups[key] = {
            "run_count": group["run_count"],
            "completed": group["measurement"]["completed_operations"],
            "elapsed": group["measurement"]["elapsed_microseconds"],
            "calls": group["counters"]["planner_allocation_calls"],
            "bytes": group["counters"]["planner_allocated_bytes"],
            "expansions": group["counters"]["planner_expansions"],
            "candidates": group["counters"]["planner_candidates"],
            "deadline_misses": group["counters"]["deadline_misses"],
            "scope_overflows": group["counters"][
                "planner_allocation_scope_overflows"
            ],
            "p99": group["histograms"]["planner"]["p99_microseconds"],
            "digest": parameters["result_digest"],
            "mask": parameters["tail_optimization_mask"],
        }

    expected = {(variant, suffix) for variant in ("tail-baseline", *CANDIDATES)
                for suffix in SUFFIXES}
    if set(groups) != expected:
        missing = sorted(expected - set(groups))
        extra = sorted(set(groups) - expected)
        raise ValueError(f"unexpected aggregate groups; missing={missing}, extra={extra}")

    baseline_variant = "tail-baseline"
    for variant in CANDIDATES:
        failures: list[str] = []
        for suffix in SUFFIXES:
            baseline = groups[(baseline_variant, suffix)]
            candidate = groups[(variant, suffix)]
            if baseline["run_count"] != 5 or candidate["run_count"] != 5:
                failures.append(f"suffix {suffix}: run_count is not 5")
            for field in ("digest", "expansions", "candidates", "completed"):
                if candidate[field] != baseline[field]:
                    failures.append(f"suffix {suffix}: {field} differs")
            if candidate["deadline_misses"] != 0 or candidate["scope_overflows"] != 0:
                failures.append(f"suffix {suffix}: deadline miss or scope overflow")
            if not at_least_ratio(candidate["completed"], candidate["elapsed"],
                                  baseline["completed"], baseline["elapsed"], 95):
                failures.append(f"suffix {suffix}: throughput below 95%")
            if candidate["p99"] > baseline["p99"]:
                failures.append(
                    f"suffix {suffix}: p99 {candidate['p99']} > {baseline['p99']} us"
                )
            for field in ("calls", "bytes"):
                if not at_most_ratio(candidate[field], candidate["completed"],
                                     baseline[field], baseline["completed"], 105):
                    failures.append(f"suffix {suffix}: allocation {field} above 105%")

        candidate_large_completed = combined(groups, variant, LARGE_SUFFIXES, "completed")
        candidate_large_elapsed = combined(groups, variant, LARGE_SUFFIXES, "elapsed")
        baseline_large_completed = combined(
            groups, baseline_variant, LARGE_SUFFIXES, "completed"
        )
        baseline_large_elapsed = combined(
            groups, baseline_variant, LARGE_SUFFIXES, "elapsed"
        )
        throughput_benefit = at_least_ratio(
            candidate_large_completed, candidate_large_elapsed,
            baseline_large_completed, baseline_large_elapsed, 102
        )
        p99_benefit = any(
            groups[(variant, suffix)]["p99"]
            < groups[(baseline_variant, suffix)]["p99"]
            for suffix in SUFFIXES if suffix >= 8
        )
        candidate_all_completed = combined(groups, variant, SUFFIXES, "completed")
        baseline_all_completed = combined(groups, baseline_variant, SUFFIXES, "completed")
        allocation_benefit = any(
            at_most_ratio(
                combined(groups, variant, SUFFIXES, field), candidate_all_completed,
                combined(groups, baseline_variant, SUFFIXES, field),
                baseline_all_completed, 95
            )
            for field in ("calls", "bytes")
        ) and at_least_ratio(
            candidate_large_completed, candidate_large_elapsed,
            baseline_large_completed, baseline_large_elapsed, 98
        )
        if not (throughput_benefit or p99_benefit or allocation_benefit):
            failures.append("no material benefit gate passed")

        decision = "ACCEPT" if not failures else "REJECT"
        print(f"{variant} mask={groups[(variant, 4)]['mask']}: {decision}")
        for failure in failures:
            print(f"  - {failure}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, KeyError, json.JSONDecodeError) as error:
        print(f"planner-tail evaluation failed: {error}")
        raise SystemExit(1)
