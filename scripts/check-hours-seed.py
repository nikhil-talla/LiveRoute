#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import sys

from jsonschema import Draft202012Validator
import yaml


REPO_ROOT = Path(__file__).resolve().parent.parent


def fail(message: str) -> None:
    raise ValueError(message)


def load_yaml(path: Path) -> object:
    with path.open("r", encoding="utf-8") as source:
        return yaml.safe_load(source)


def main() -> int:
    schema_path = REPO_ROOT / "schema/hours/liveroute-v1-hours-seed.schema.json"
    seed_lock_path = REPO_ROOT / "config/hours-seed.lock"
    tzdata_lock_path = REPO_ROOT / "config/tzdata.lock"
    local_config_path = REPO_ROOT / "config/local-v1.yaml"

    schema = json.loads(schema_path.read_text(encoding="utf-8"))
    seed_lock = load_yaml(seed_lock_path)
    tzdata_lock = load_yaml(tzdata_lock_path)
    local_config = load_yaml(local_config_path)

    if not isinstance(seed_lock, dict) or not isinstance(seed_lock.get("artifact"), dict):
        fail("config/hours-seed.lock must contain an artifact mapping")
    artifact = seed_lock["artifact"]
    repository_path = artifact.get("repository_path")
    container_path = artifact.get("container_path")
    expected_sha256 = artifact.get("sha256")
    if not all(isinstance(value, str) for value in (
        repository_path,
        container_path,
        expected_sha256,
    )):
        fail("hours seed lock paths and SHA-256 must be strings")
    if len(expected_sha256) != 64 or any(
        character not in "0123456789abcdef" for character in expected_sha256
    ):
        fail("hours seed lock SHA-256 must be 64 lowercase hexadecimal characters")

    seed_path = (REPO_ROOT / repository_path).resolve()
    try:
        seed_path.relative_to(REPO_ROOT.resolve())
    except ValueError:
        fail("hours seed repository path must stay within the repository")
    if not seed_path.is_file():
        fail(f"hours seed artifact does not exist: {repository_path}")

    seed_bytes = seed_path.read_bytes()
    actual_sha256 = hashlib.sha256(seed_bytes).hexdigest()
    if actual_sha256 != expected_sha256:
        fail(
            "hours seed artifact SHA-256 does not match config/hours-seed.lock: "
            f"expected {expected_sha256}, got {actual_sha256}"
        )

    seed = json.loads(seed_bytes)
    Draft202012Validator.check_schema(schema)
    errors = sorted(
        Draft202012Validator(schema).iter_errors(seed),
        key=lambda error: [str(part) for part in error.absolute_path],
    )
    if errors:
        first = errors[0]
        location = "/".join(str(part) for part in first.absolute_path) or "<root>"
        fail(f"hours seed schema validation failed at {location}: {first.message}")

    place_ids = [place["place_id"] for place in seed["places"]]
    if place_ids != sorted(place_ids) or len(place_ids) != len(set(place_ids)):
        fail("hours seed places must be strictly sorted by unique place_id")

    if not isinstance(tzdata_lock, dict) or not isinstance(
        tzdata_lock.get("release"), str
    ):
        fail("config/tzdata.lock must contain a release string")
    tzdata_release = tzdata_lock["release"]
    if seed["tzdata_release"] != tzdata_release:
        fail("hours seed tzdata_release must match config/tzdata.lock")

    if not isinstance(local_config, dict) or not isinstance(
        local_config.get("hours"), dict
    ):
        fail("config/local-v1.yaml must contain an hours mapping")
    hours_config = local_config["hours"]
    expected_zoneinfo_path = (
        f"/opt/liveroute/share/tzdata/{tzdata_release}/zoneinfo"
    )
    required_config = {
        "seed_file_path": container_path,
        "seed_file_sha256": expected_sha256,
        "tzdata_release": tzdata_release,
        "tzdata_zoneinfo_path": expected_zoneinfo_path,
    }
    for key, expected in required_config.items():
        if hours_config.get(key) != expected:
            fail(
                f"config/local-v1.yaml hours.{key} must be {expected!r}, "
                f"got {hours_config.get(key)!r}"
            )

    print(
        "Seeded-hours seed/config checks passed: "
        f"{repository_path} ({actual_sha256})."
    )
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, json.JSONDecodeError, yaml.YAMLError) as error:
        print(f"hours seed check failed: {error}", file=sys.stderr)
        sys.exit(1)
