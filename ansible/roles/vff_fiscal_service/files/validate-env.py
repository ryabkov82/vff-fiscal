#!/usr/bin/env python3
"""Validate vff-fiscal .env keys without printing secret values."""

from __future__ import annotations

import os
import sys
from pathlib import Path


def parse_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip()
    return values


def main() -> int:
    env_file = Path(os.environ["ENV_FILE"])
    required = os.environ["REQUIRED_KEYS"].split(",")
    values = parse_env(env_file)
    missing = [key for key in required if not values.get(key, "").strip()]
    if missing:
        print(f"missing_or_empty_keys={len(missing)}", file=sys.stderr)
        return 1
    print("env_keys_ok=1")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
