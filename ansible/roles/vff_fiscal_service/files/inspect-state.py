#!/usr/bin/env python3
"""Validate vff-fiscal state.json without printing sensitive values."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path


def block_creating() -> bool:
    return os.environ.get("BLOCK_CREATING", "1") == "1"


def validate_state(data: object) -> tuple[int, list[str]]:
    errors: list[str] = []
    if not isinstance(data, dict):
        return 1, ["invalid_root"]

    version = data.get("version")
    if version != 1:
        errors.append("invalid_version")

    auth = data.get("auth")
    if not isinstance(auth, dict):
        errors.append("missing_auth")
    else:
        for key in ("refresh_token", "device_id", "inn"):
            value = auth.get(key)
            if not isinstance(value, str) or not value.strip():
                errors.append(f"missing_auth_{key}")

    receipts = data.get("receipts")
    if receipts is None:
        receipts = {}
    if not isinstance(receipts, dict):
        errors.append("invalid_receipts")
        receipts = {}

    creating_count = sum(
        1
        for record in receipts.values()
        if isinstance(record, dict) and record.get("status") == "creating"
    )
    print(f"creating_count={creating_count}")
    if creating_count and block_creating():
        errors.append("creating_receipts_present")

    if errors:
        print("state_errors=" + ",".join(errors), file=sys.stderr)
        return 1, errors

    print("state_ok=1")
    return 0, []


def main() -> int:
    state_path = Path(os.environ["STATE_FILE"])
    data = json.loads(state_path.read_text(encoding="utf-8"))
    rc, _ = validate_state(data)
    return rc


if __name__ == "__main__":
    raise SystemExit(main())
