#!/usr/bin/env python3
"""Merge schema-validated LiveRoute benchmark artifacts without lossy averages."""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator, RefResolver

UINT64_MAX = (1 << 64) - 1


def fail(message: str) -> "NoReturn":
    raise ValueError(message)


def checked_add(left: int, right: int, label: str) -> int:
    result = left + right
    if result > UINT64_MAX:
        fail(f"uint64 overflow while summing {label}")
    return result


def canonical(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def validator_for(path: Path) -> Draft202012Validator:
    schema = json.loads(path.read_text(encoding="utf-8"))
    Draft202012Validator.check_schema(schema)
    store: dict[str, Any] = {}
    for sibling in path.parent.glob("*.schema.json"):
        sibling_schema = json.loads(sibling.read_text(encoding="utf-8"))
        if "$id" in sibling_schema:
            store[sibling_schema["$id"]] = sibling_schema
    resolver = RefResolver(path.parent.as_uri() + "/", schema, store=store)
    return Draft202012Validator(schema, resolver=resolver)


def validate_document(validator: Draft202012Validator, document: Any, path: Path) -> None:
    errors = sorted(validator.iter_errors(document), key=lambda error: list(error.path))
    if errors:
        error = errors[0]
        location = ".".join(str(part) for part in error.path) or "$"
        fail(f"{path}: {location}: {error.message}")


def percentile(histogram: dict[str, Any], percentage: int) -> int | None:
    count = histogram["count"]
    if count == 0:
        return None
    target = math.ceil(percentage * count / 100)
    cumulative = 0
    for upper_bound, bucket_count in zip(
        histogram["upper_bounds_microseconds"], histogram["bucket_counts"]
    ):
        cumulative += bucket_count
        if cumulative >= target:
            return upper_bound
    fail("histogram bucket counts do not cover its count")


def merge_histograms(histograms: list[dict[str, Any]]) -> dict[str, Any]:
    first = histograms[0]
    bounds = first["upper_bounds_microseconds"]
    bucket_counts = [0] * len(bounds)
    count = 0
    total = 0
    maximum = 0
    for histogram in histograms:
        if histogram["upper_bounds_microseconds"] != bounds:
            fail("histogram bucket boundaries differ")
        count = checked_add(count, histogram["count"], "histogram count")
        total = checked_add(total, histogram["sum_microseconds"], "histogram sum")
        maximum = max(maximum, histogram["max_microseconds"])
        for index, bucket_count in enumerate(histogram["bucket_counts"]):
            bucket_counts[index] = checked_add(
                bucket_counts[index], bucket_count, "histogram bucket count"
            )
    if sum(bucket_counts) != count:
        fail("merged histogram bucket counts do not equal count")
    merged = {
        "unit": first["unit"],
        "count": count,
        "sum_microseconds": total,
        "max_microseconds": maximum,
        "upper_bounds_microseconds": bounds,
        "bucket_counts": bucket_counts,
        "p50_microseconds": percentile(
            {**first, "count": count, "bucket_counts": bucket_counts}, 50
        ),
        "p95_microseconds": percentile(
            {**first, "count": count, "bucket_counts": bucket_counts}, 95
        ),
        "p99_microseconds": percentile(
            {**first, "count": count, "bucket_counts": bucket_counts}, 99
        ),
    }
    return merged


def merge_group(documents: list[dict[str, Any]]) -> dict[str, Any]:
    first = documents[0]
    measurement: dict[str, int] = {}
    for field in ("warmup_operations", "measured_operations", "elapsed_microseconds", "completed_operations"):
        measurement[field] = sum(
            (checked_add(0, document["measurement"][field], field) for document in documents),
            0,
        )
        if measurement[field] > UINT64_MAX:
            fail(f"uint64 overflow while summing measurement {field}")
    if measurement["elapsed_microseconds"] == 0:
        fail("aggregate elapsed time is zero")

    counter_names = set().union(*(document["counters"] for document in documents))
    counters: dict[str, int] = {}
    for name in sorted(counter_names):
        total = 0
        for document in documents:
            total = checked_add(total, document["counters"].get(name, 0), f"counter {name}")
        counters[name] = total

    histogram_names = set().union(*(document["histograms"] for document in documents))
    histograms: dict[str, Any] = {}
    for name in sorted(histogram_names):
        observations = [document["histograms"][name] for document in documents if name in document["histograms"]]
        if len(observations) != len(documents):
            fail(f"incomplete histogram metric shape for {name}")
        histograms[name] = merge_histograms(observations)

    gauge_names = set().union(*(document["gauges"] for document in documents))
    gauges: dict[str, Any] = {}
    latest = max(documents, key=lambda document: (document["started_at"], document["run_id"]))
    for name in sorted(gauge_names):
        observations = [document["gauges"][name] for document in documents if name in document["gauges"]]
        if len(observations) != len(documents):
            fail(f"incomplete gauge metric shape for {name}")
        gauges[name] = {
            "last": latest["gauges"][name]["last"],
            "maximum": max(observation["maximum"] for observation in observations),
        }

    completed = measurement["completed_operations"]
    throughput_numerator = completed * 1_000_000_000
    if throughput_numerator > UINT64_MAX:
        fail("uint64 overflow while deriving throughput")
    throughput = throughput_numerator // measurement["elapsed_microseconds"]

    rates: dict[str, dict[str, int]] = {}
    if "planner_allocation_calls" in counters:
        rates["planner_allocation_calls_per_operation"] = {
            "numerator": counters["planner_allocation_calls"],
            "denominator": completed,
        }
    if "planner_allocated_bytes" in counters:
        rates["planner_allocated_bytes_per_operation"] = {
            "numerator": counters["planner_allocated_bytes"],
            "denominator": completed,
        }

    return {
        "benchmark_name": first["benchmark_name"],
        "dimensions": first["dimensions"],
        "run_count": len(documents),
        "run_ids": sorted(document["run_id"] for document in documents),
        "measurement": measurement,
        "counters": counters,
        "histograms": histograms,
        "gauges": gauges,
        "throughput_millioperations_per_second": throughput,
        "rates": rates,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("artifacts", type=Path, nargs="+")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    raw_validator = validator_for(root / "schema/benchmark/liveroute-benchmark-v1.schema.json")
    aggregate_schema = root / "schema/benchmark/liveroute-benchmark-aggregate-v1.schema.json"
    aggregate_validator = validator_for(aggregate_schema)

    documents: list[dict[str, Any]] = []
    seen_run_ids: set[str] = set()
    for path in sorted(args.artifacts):
        document = json.loads(path.read_text(encoding="utf-8"))
        validate_document(raw_validator, document, path)
        if document["run_id"] in seen_run_ids:
            fail(f"duplicate run_id: {document['run_id']}")
        seen_run_ids.add(document["run_id"])
        documents.append(document)
    if not documents:
        fail("no benchmark artifacts supplied")

    partitions: dict[tuple[str, str], list[dict[str, Any]]] = {}
    for document in documents:
        key = (document["benchmark_name"], canonical(document["dimensions"]))
        partitions.setdefault(key, []).append(document)
    groups = [merge_group(partitions[key]) for key in sorted(partitions)]
    aggregate = {"schema_version": "liveroute.benchmark.aggregate.v1", "groups": groups}
    validate_document(aggregate_validator, aggregate, args.output)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(aggregate, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"benchmark aggregation failed: {error}", file=sys.stderr)
        raise SystemExit(1)
