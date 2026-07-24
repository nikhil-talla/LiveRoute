#!/usr/bin/env python3

import json
from pathlib import Path

from jsonschema import Draft202012Validator, ValidationError


ROOT = Path(__file__).resolve().parents[1]
SCHEMA_PATHS = (
    ROOT / "schema/websocket/liveroute-v1-client-envelope.schema.json",
    ROOT / "schema/websocket/liveroute-v1-server-envelope.schema.json",
)
CORPUS_ROOT = ROOT / "schema/websocket/corpus"


def load_json(path: Path):
    with path.open(encoding="utf-8") as source:
        return json.load(source)


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
    validators = []
    for schema_path in SCHEMA_PATHS:
        schema = load_json(schema_path)
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


if __name__ == "__main__":
    main()
