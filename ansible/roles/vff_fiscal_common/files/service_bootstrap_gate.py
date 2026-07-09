#!/usr/bin/env python3
"""Evaluate whether legacy service bootstrap is allowed for the deploy manifest."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

SERVICE_IDENTITY_FIELDS = (
    "service_commit",
    "service_image",
    "service_image_id",
)


def normalize_identity_value(value: object) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value.strip()
    return str(value).strip()


def load_manifest(path: Path) -> dict[str, object]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid manifest JSON: {exc.msg}") from exc
    if not isinstance(payload, dict):
        raise ValueError("manifest must be a JSON object")
    return payload


def service_identity_registered(manifest: dict[str, object]) -> bool:
    return any(
        normalize_identity_value(manifest.get(field))
        for field in SERVICE_IDENTITY_FIELDS
    )


def evaluate_manifest(path: Path) -> tuple[bool, bool]:
    """Return (manifest_present, service_identity_registered)."""
    if not path.is_file():
        return False, False
    manifest = load_manifest(path)
    return True, service_identity_registered(manifest)


def main() -> int:
    manifest_path = Path(os.environ["MANIFEST_PATH"])
    try:
        manifest_present, identity_registered = evaluate_manifest(manifest_path)
    except ValueError as exc:
        print("manifest_parse_failed=1", file=sys.stderr)
        print(str(exc), file=sys.stderr)
        return 1

    print(f"manifest_present={1 if manifest_present else 0}")
    print(f"service_identity_registered={1 if identity_registered else 0}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
