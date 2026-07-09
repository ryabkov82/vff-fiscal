#!/usr/bin/env python3
"""Acquire and release a host-side deployment lock safely."""

from __future__ import annotations

import argparse
import json
import os
import socket
import sys
from datetime import datetime, timezone
from pathlib import Path


def acquire(lock_dir: Path, operation: str, token: str) -> int:
    lock_dir.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    try:
        lock_dir.mkdir(mode=0o700)
    except FileExistsError:
        metadata_path = lock_dir / "metadata.json"
        metadata = metadata_path.read_text(encoding="utf-8") if metadata_path.exists() else ""
        print(f"deployment_lock_held=1\n{metadata}", file=sys.stderr)
        return 1

    metadata = {
        "operation": operation,
        "token": token,
        "controller_host": socket.gethostname(),
        "created_at": datetime.now(timezone.utc).isoformat(),
        "pid": os.getpid(),
    }
    (lock_dir / "token").write_text(token, encoding="utf-8")
    (lock_dir / "metadata.json").write_text(
        json.dumps(metadata, sort_keys=True, indent=2) + "\n", encoding="utf-8"
    )
    print("deployment_lock_acquired=1")
    return 0


def release(lock_dir: Path, token: str) -> int:
    token_path = lock_dir / "token"
    if not lock_dir.exists():
        print("deployment_lock_released=0")
        return 0
    if not token_path.exists() or token_path.read_text(encoding="utf-8") != token:
        print("deployment_lock_not_owned=1", file=sys.stderr)
        return 1
    for name in ("metadata.json", "token"):
        path = lock_dir / name
        if path.exists():
            path.unlink()
    lock_dir.rmdir()
    print("deployment_lock_released=1")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("acquire", "release"))
    parser.add_argument("--lock-dir", required=True)
    parser.add_argument("--operation", default="")
    parser.add_argument("--token", required=True)
    args = parser.parse_args()
    lock_dir = Path(args.lock_dir)
    if args.action == "acquire":
        return acquire(lock_dir, args.operation, args.token)
    return release(lock_dir, args.token)


if __name__ == "__main__":
    raise SystemExit(main())
