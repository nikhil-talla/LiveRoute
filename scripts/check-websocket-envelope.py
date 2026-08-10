#!/usr/bin/env python3

import argparse
import hashlib
import json
from pathlib import Path

from jsonschema import Draft202012Validator, ValidationError


ROOT = Path(__file__).resolve().parents[1]
SCHEMA_PATHS = (
    ROOT / "schema/websocket/liveroute-v1-client-envelope.schema.json",
    ROOT / "schema/websocket/liveroute-v1-server-envelope.schema.json",
    ROOT / "schema/websocket/liveroute-v1.5-navigation-extension.schema.json",
)
ENVELOPE_SCHEMA_PATHS = SCHEMA_PATHS[:2]
CORPUS_ROOT = ROOT / "schema/websocket/corpus"
NAVIGATION_CORPUS_ROOT = ROOT / "schema/websocket/navigation-corpus"
MANIFEST_PATH = ROOT / "schema/websocket/liveroute-v1-schema-manifest.json"
MAX_SAFE_JSON_INTEGER = 9_007_199_254_740_991


def reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON object key: {key}")
        result[key] = value
    return result


def reject_non_integer_number(value):
    raise ValueError(f"non-integer JSON number is forbidden in schema artifacts: {value}")


def reject_non_finite_number(value):
    raise ValueError(f"non-finite JSON number is forbidden: {value}")


def load_json(path: Path, *, schema_artifact: bool = False):
    options = {
        "object_pairs_hook": reject_duplicate_keys,
        "parse_constant": reject_non_finite_number,
    }
    if schema_artifact:
        options["parse_float"] = reject_non_integer_number
    with path.open(encoding="utf-8") as source:
        return json.load(source, **options)


def validate_jcs_subset(value) -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            if not key.isascii():
                raise ValueError(
                    "schema object member names must be ASCII so bytewise ordering "
                    "is identical to RFC 8785 UTF-16 ordering"
                )
            validate_jcs_subset(key)
            validate_jcs_subset(child)
    elif isinstance(value, list):
        for child in value:
            validate_jcs_subset(child)
    elif isinstance(value, str):
        if any(0xD800 <= ord(character) <= 0xDFFF for character in value):
            raise ValueError("unpaired Unicode surrogate is forbidden")
    elif isinstance(value, int) and not isinstance(value, bool):
        if abs(value) > MAX_SAFE_JSON_INTEGER:
            raise ValueError(f"JSON integer exceeds the RFC 8785 safe range: {value}")


def canonicalize_schema(schema) -> bytes:
    validate_jcs_subset(schema)
    return json.dumps(
        schema,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")


def expected_manifest():
    files = []
    bundle_input = bytearray()
    for schema_path in sorted(SCHEMA_PATHS, key=lambda path: path.as_posix().encode("utf-8")):
        relative_path = schema_path.relative_to(ROOT).as_posix()
        digest = hashlib.sha256(
            canonicalize_schema(load_json(schema_path, schema_artifact=True))
        ).hexdigest()
        files.append({"path": relative_path, "sha256": digest})
        bundle_input.extend(relative_path.encode("utf-8"))
        bundle_input.extend(b"\0")
        bundle_input.extend(digest.encode("ascii"))
        bundle_input.extend(b"\n")
    return {
        "manifest_version": 1,
        "canonicalization": "rfc8785-json-schema-integer-subset-v1",
        "bundle_algorithm": "sha256-path-nul-file-digest-lf-v1",
        "files": files,
        "bundle_sha256": hashlib.sha256(bundle_input).hexdigest(),
    }


def validate_directory(validator: Draft202012Validator, directory: Path, valid: bool) -> None:
    for fixture in sorted(directory.glob("*.json")):
        try:
            validator.validate(load_json(fixture))
        except ValidationError as error:
            if valid:
                raise AssertionError(f"expected {fixture} to validate: {error.message}") from error
        else:
            if not valid:
                raise AssertionError(f"expected {fixture} to fail validation")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--print-manifest",
        action="store_true",
        help="print the exact expected schema manifest and exit",
    )
    arguments = parser.parse_args()

    manifest = expected_manifest()
    if arguments.print_manifest:
        print(json.dumps(manifest, indent=2))
        return
    if load_json(MANIFEST_PATH) != manifest:
        raise AssertionError(
            f"{MANIFEST_PATH} does not match the canonical WebSocket schema bundle"
        )

    validators = []
    for schema_path in ENVELOPE_SCHEMA_PATHS:
        schema = load_json(schema_path, schema_artifact=True)
        Draft202012Validator.check_schema(schema)
        validators.append(Draft202012Validator(schema))
    for fixture in sorted((CORPUS_ROOT / "positive").glob("*.json")):
        payload = load_json(fixture)
        if not any(validator.is_valid(payload) for validator in validators):
            raise AssertionError(f"expected {fixture} to validate")
    for fixture in sorted((CORPUS_ROOT / "negative").glob("*.json")):
        payload = load_json(fixture)
        if any(validator.is_valid(payload) for validator in validators):
            raise AssertionError(f"expected {fixture} to fail validation")

    navigation_schema = load_json(SCHEMA_PATHS[2], schema_artifact=True)
    Draft202012Validator.check_schema(navigation_schema)
    navigation_validator = Draft202012Validator(navigation_schema)
    client_validator = validators[0]

    def navigation_errors(payload):
        if not client_validator.is_valid(payload):
            return ["message is incompatible with the completed V1 client schema"]
        if payload.get("kind") != "telemetry_update":
            return ["navigation extension is not on telemetry_update"]
        telemetry = payload.get("payload", {})
        if telemetry.get("observation_kind") != "route_deviation":
            return ["navigation extension is not on route_deviation"]
        extension = payload.get("extensions", {}).get("liveroute.navigation_v1")
        errors = [error.message for error in navigation_validator.iter_errors(extension)]
        if errors:
            return errors
        observed_at = telemetry.get("observed_at_unix_ms")
        if extension["updated_eta_unix_ms"] < observed_at:
            return ["updated ETA precedes observation time"]
        return []

    for fixture in sorted((NAVIGATION_CORPUS_ROOT / "positive").glob("*.json")):
        payload = load_json(fixture)
        errors = navigation_errors(payload)
        if errors:
            raise AssertionError(f"expected {fixture} to validate: {errors[0]}")
    for fixture in sorted((NAVIGATION_CORPUS_ROOT / "negative").glob("*.json")):
        payload = load_json(fixture)
        if not navigation_errors(payload):
            raise AssertionError(f"expected {fixture} to fail navigation validation")


if __name__ == "__main__":
    main()
