#!/usr/bin/env python3
"""Atomically write deploy-state.json without printing secret values."""

from __future__ import annotations

import json
import os
import sys
import tempfile
from pathlib import Path


def sync_path(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def main() -> int:
    manifest_path = Path(os.environ["MANIFEST_PATH"])
    payload = json.load(sys.stdin)
    manifest_path.parent.mkdir(parents=True, exist_ok=True)

    fd, tmp_name = tempfile.mkstemp(
        dir=manifest_path.parent,
        prefix=".deploy-state-",
        suffix=".tmp",
    )
    tmp_path = Path(tmp_name)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(tmp_path, 0o600)
        os.replace(tmp_path, manifest_path)
        sync_path(manifest_path.parent)
    except Exception:
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)
        raise

    print("manifest_written=1")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
