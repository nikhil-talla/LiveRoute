#!/usr/bin/env python3
"""Validate the V1.5 OpenAPI document and its compatibility corpus."""

from __future__ import annotations

import hashlib
import json
import pathlib
import sys
from typing import Any

import jsonschema
import yaml


ROOT = pathlib.Path(__file__).resolve().parents[1]
OPENAPI_PATH = ROOT / "schema/http/liveroute-v1.5.openapi.yaml"
MANIFEST_PATH = ROOT / "schema/http/liveroute-v1.5-schema-manifest.json"
POLICY_PATH = ROOT / "config/v15-contract-policy.yaml"
FRONTEND_LOCK_PATH = ROOT / "config/frontend-toolchain.lock"
TIMEZONE_LOCK_PATH = ROOT / "config/timezone-boundaries.lock"
TOOL_IMAGES_PATH = ROOT / "config/tool-images.lock"
FRONTEND_DOCKERFILE_PATH = ROOT / "docker/frontend/Dockerfile"
FRONTEND_PACKAGE_PATH = ROOT / "frontend/package.json"
FRONTEND_PACKAGE_LOCK_PATH = ROOT / "frontend/package-lock.json"
FRONTEND_ACTIVITY_DEFAULTS_PATH = ROOT / "frontend/src/planner/activity-defaults.json"
FRONTEND_GPS_POLICY_PATH = ROOT / "frontend/src/live/gps-policy.json"
FRONTEND_ROUTE_DEVIATION_POLICY_PATH = (
    ROOT / "frontend/src/live/route-deviation-policy.json"
)
TIMEZONE_DOCKERFILE_PATH = ROOT / "docker/timezone-boundaries/Dockerfile"


class UniqueKeyLoader(yaml.SafeLoader):
    """Safe YAML loader that refuses silently overwritten mapping keys."""


def construct_unique_mapping(
    loader: UniqueKeyLoader, node: yaml.MappingNode, deep: bool = False
) -> dict[Any, Any]:
    mapping: dict[Any, Any] = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in mapping:
            fail(f"duplicate YAML key at line {key_node.start_mark.line + 1}: {key}")
        mapping[key] = loader.construct_object(value_node, deep=deep)
    return mapping


UniqueKeyLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
    construct_unique_mapping,
)


def fail(message: str) -> None:
    raise RuntimeError(message)


def sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_yaml(path: pathlib.Path) -> Any:
    return yaml.load(path.read_text(encoding="utf-8"), Loader=UniqueKeyLoader)


def resolve_pointer(document: Any, reference: str) -> Any:
    if not reference.startswith("#/"):
        fail(f"external or malformed reference is forbidden: {reference}")
    current = document
    for raw_part in reference[2:].split("/"):
        part = raw_part.replace("~1", "/").replace("~0", "~")
        if not isinstance(current, dict) or part not in current:
            fail(f"unresolved reference: {reference}")
        current = current[part]
    return current


def walk(value: Any) -> Any:
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


def activity_semantic_errors(activity: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    schedule = activity.get("schedule", {})
    if schedule.get("state") == "scheduled":
        start = schedule.get("start_offset_ms")
        end = schedule.get("end_offset_ms")
        if isinstance(start, int) and isinstance(end, int) and start >= end:
            errors.append("scheduled activity start_offset_ms must precede end_offset_ms")
    timing = activity.get("timing", {})
    durations = [
        timing.get("min_duration_seconds"),
        timing.get("preferred_duration_seconds"),
        timing.get("max_duration_seconds"),
    ]
    if all(isinstance(value, int) for value in durations) and not (
        durations[0] <= durations[1] <= durations[2]
    ):
        errors.append("activity durations must satisfy min <= preferred <= max")
    return errors


def semantic_errors(schema_name: str, instance: Any) -> list[str]:
    if not isinstance(instance, dict):
        return []
    if schema_name == "ActivityInput":
        return activity_semantic_errors(instance)
    if schema_name == "CreateTripRequest":
        activities = instance.get("activities", [])
    elif schema_name == "Trip":
        activities = instance.get("saved_plan", {}).get("activities", [])
    else:
        return []
    if not isinstance(activities, list):
        return []
    errors = [
        error
        for activity in activities
        if isinstance(activity, dict)
        for error in activity_semantic_errors(activity)
    ]
    ordinals = [activity.get("ordinal") for activity in activities if isinstance(activity, dict)]
    if ordinals != list(range(len(activities))):
        errors.append("activity ordinals must be unique and contiguous from zero")
    return errors


def main() -> int:
    document = load_yaml(OPENAPI_PATH)
    if not isinstance(document, dict):
        fail("OpenAPI document must be an object")
    if document.get("openapi") != "3.1.1":
        fail("OpenAPI version must be 3.1.1")
    if document.get("jsonSchemaDialect") != "https://json-schema.org/draft/2020-12/schema":
        fail("OpenAPI JSON Schema dialect must be draft 2020-12")
    if document.get("info", {}).get("version") != "liveroute.http.v1.1":
        fail("HTTP contract version must be liveroute.http.v1.1")

    operation_ids: set[str] = set()
    for node in walk(document):
        reference = node.get("$ref")
        if reference is not None:
            resolve_pointer(document, reference)
        operation_id = node.get("operationId")
        if operation_id is not None:
            if operation_id in operation_ids:
                fail(f"duplicate operationId: {operation_id}")
            operation_ids.add(operation_id)

    expected_operations = {
        "createGoogleLoginNonce",
        "authenticateWithGoogle",
        "getSession",
        "logout",
        "createWebSocketTicket",
        "listTrips",
        "createTrip",
        "getTrip",
        "updateTripMetadata",
        "deleteTrip",
        "addTripActivity",
        "replaceTripActivity",
        "deleteTripActivity",
        "resolvePermanentPlace",
        "createPlace",
        "activateTrip",
        "deactivateTrip",
    }
    if operation_ids != expected_operations:
        fail(
            "HTTP operation set drifted: "
            f"missing={sorted(expected_operations - operation_ids)}, "
            f"extra={sorted(operation_ids - expected_operations)}"
        )

    for name, schema in document.get("components", {}).get("schemas", {}).items():
        if schema.get("type") == "object" and schema.get("additionalProperties") is not False:
            fail(f"object schema must reject unknown properties: {name}")

    manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    if manifest.get("contract_version") != "liveroute.http.v1.1":
        fail("manifest contract version is invalid")
    if manifest.get("openapi_sha256") != sha256(OPENAPI_PATH):
        fail("OpenAPI digest does not match manifest")

    resolver = jsonschema.RefResolver.from_schema(document)
    format_checker = jsonschema.FormatChecker()
    for case in manifest.get("cases", []):
        relative = case["path"]
        case_path = ROOT / relative
        if case.get("sha256") != sha256(case_path):
            fail(f"corpus digest does not match: {relative}")
        instance = json.loads(case_path.read_text(encoding="utf-8"))
        schema_name = case["schema"]
        schema = {"$ref": f"#/components/schemas/{schema_name}"}
        validator = jsonschema.Draft202012Validator(
            schema,
            resolver=resolver,
            format_checker=format_checker,
        )
        errors = sorted(validator.iter_errors(instance), key=lambda error: list(error.path))
        semantic = [] if errors else semantic_errors(schema_name, instance)
        expected_valid = case["valid"]
        if expected_valid and (errors or semantic):
            message = errors[0].message if errors else semantic[0]
            fail(f"positive corpus case failed {relative}: {message}")
        if not expected_valid and not errors and not semantic:
            fail(f"negative corpus case unexpectedly passed: {relative}")

    recorded_cases = {case["path"] for case in manifest["cases"]}
    actual_cases = {
        path.relative_to(ROOT).as_posix()
        for path in (ROOT / "schema/http/corpus").glob("*/*.json")
    }
    if recorded_cases != actual_cases:
        fail(
            "manifest/corpus membership drifted: "
            f"unrecorded={sorted(actual_cases - recorded_cases)}, "
            f"missing={sorted(recorded_cases - actual_cases)}"
        )

    policy = load_yaml(POLICY_PATH)
    frontend_lock = load_yaml(FRONTEND_LOCK_PATH)
    timezone_lock = load_yaml(TIMEZONE_LOCK_PATH)
    tool_images = load_yaml(TOOL_IMAGES_PATH)
    if policy.get("contract_version") != "liveroute.http.v1.1":
        fail("policy contract version is invalid")

    activity_defaults = json.loads(
        FRONTEND_ACTIVITY_DEFAULTS_PATH.read_text(encoding="utf-8")
    )
    if activity_defaults != policy["trip_creation"]["new_activity_defaults"]:
        fail("frontend new-activity defaults differ from the normative policy")

    gps_policy = json.loads(FRONTEND_GPS_POLICY_PATH.read_text(encoding="utf-8"))
    if gps_policy != policy["browser_gps"]:
        fail("frontend GPS policy differs from the normative policy")
    if gps_policy["minimum_send_interval_ms"] < 1000:
        fail("frontend GPS policy exceeds the one-message-per-second ceiling")
    if gps_policy["stationary_heartbeat_interval_ms"] < gps_policy["minimum_send_interval_ms"]:
        fail("frontend GPS heartbeat is faster than its send-rate ceiling")
    if gps_policy["maximum_in_flight_location_messages"] != 1:
        fail("frontend GPS policy must allow exactly one in-flight location message")
    if gps_policy["maximum_pending_location_samples"] != 1:
        fail("frontend GPS policy must retain exactly one pending location sample")

    route_deviation_policy = json.loads(
        FRONTEND_ROUTE_DEVIATION_POLICY_PATH.read_text(encoding="utf-8")
    )
    if route_deviation_policy != policy["route_deviation"]:
        fail("frontend route-deviation policy differs from the normative policy")
    for profile in ("walking", "driving"):
        enter = route_deviation_policy[f"{profile}_enter_effective_distance_meters"]
        exit_distance = route_deviation_policy[
            f"{profile}_exit_effective_distance_meters"
        ]
        if exit_distance >= enter:
            fail(f"frontend {profile} route-deviation hysteresis is invalid")
    if route_deviation_policy["directions_max_in_flight"] != 1:
        fail("frontend route-deviation policy must bound Directions in-flight work to one")
    if route_deviation_policy["directions_automatic_retry_count"] > 1:
        fail("frontend route-deviation policy permits too many automatic retries")

    frontend_toolchain = frontend_lock["toolchain"]
    frontend_image = tool_images["images"]["frontend_toolchain"]
    if frontend_toolchain["image_digest"] != frontend_image["digest"]:
        fail("frontend image digest differs between lock files")
    if frontend_toolchain["node_version"] != frontend_image["resolved_node_version"]:
        fail("frontend Node version differs between lock files")
    if frontend_toolchain["npm_version"] != frontend_image["resolved_npm_version"]:
        fail("frontend npm version differs between lock files")
    frontend_dockerfile = FRONTEND_DOCKERFILE_PATH.read_text(encoding="utf-8")
    for expected in (
        frontend_toolchain["image_digest"],
        f'v{frontend_toolchain["node_version"]}',
        frontend_toolchain["npm_version"],
    ):
        if str(expected) not in frontend_dockerfile:
            fail(f"frontend Dockerfile does not assert locked value: {expected}")

    package = json.loads(FRONTEND_PACKAGE_PATH.read_text(encoding="utf-8"))
    package_lock = json.loads(FRONTEND_PACKAGE_LOCK_PATH.read_text(encoding="utf-8"))
    expected_engines = {
        "node": str(frontend_toolchain["node_version"]),
        "npm": str(frontend_toolchain["npm_version"]),
    }
    for field, locked_field in (
        ("dependencies", "runtime_dependencies"),
        ("devDependencies", "development_dependencies"),
        ("overrides", "dependency_overrides"),
    ):
        if package.get(field) != frontend_lock[locked_field]:
            fail(f"frontend package.json {field} differs from the toolchain lock")
    if package.get("engines") != expected_engines:
        fail("frontend package.json engines differ from the toolchain lock")
    if package_lock.get("lockfileVersion") != frontend_toolchain["package_lock_version"]:
        fail("frontend package-lock.json version differs from the toolchain lock")
    lock_root = package_lock.get("packages", {}).get("")
    if not isinstance(lock_root, dict):
        fail("frontend package-lock.json has no root package")
    for field in ("dependencies", "devDependencies", "engines"):
        if lock_root.get(field) != package.get(field):
            fail(f"frontend package-lock.json root {field} differs from package.json")

    timezone_dataset = timezone_lock["dataset"]
    timezone_policy = policy["timezone_boundaries"]
    if timezone_policy["lock_file"] != TIMEZONE_LOCK_PATH.relative_to(ROOT).as_posix():
        fail("timezone policy points to the wrong lock file")
    if timezone_policy["boundary_policy"] != timezone_dataset["boundary_policy"]:
        fail("timezone boundary policy differs between policy and dataset lock")
    if timezone_policy["no_polygon_policy"] != timezone_dataset["no_polygon_policy"]:
        fail("timezone no-polygon policy differs between policy and dataset lock")
    for field in ("sha256", "extracted_geojson_sha256"):
        value = timezone_dataset[field]
        if not isinstance(value, str) or len(value) != 64 or any(
            character not in "0123456789abcdef" for character in value
        ):
            fail(f"invalid timezone dataset {field}")
    if timezone_dataset["container_build_file"] != TIMEZONE_DOCKERFILE_PATH.relative_to(ROOT).as_posix():
        fail("timezone dataset lock points to the wrong container build file")
    timezone_dockerfile = TIMEZONE_DOCKERFILE_PATH.read_text(encoding="utf-8")
    for lock_key in (
        "sha256",
        "size_bytes",
        "extracted_geojson_sha256",
        "extracted_geojson_size_bytes",
        "container_geojson_path",
    ):
        if lock_key not in timezone_dockerfile:
            fail(f"timezone Dockerfile does not consume locked field: {lock_key}")

    print(
        f"V1.5 contract foundation passed: {len(operation_ids)} HTTP operations, "
        f"{len(manifest['cases'])} corpus cases, and locked tool/provider assets."
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (KeyError, OSError, RuntimeError, TypeError, ValueError, yaml.YAMLError) as error:
        print(f"HTTP contract validation failed: {error}", file=sys.stderr)
        raise SystemExit(1)
