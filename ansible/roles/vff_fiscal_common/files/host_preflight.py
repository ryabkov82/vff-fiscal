#!/usr/bin/env python3
"""Check required host executables without invoking a shell."""

from __future__ import annotations

import shutil
import sys

REQUIRED = ("git", "docker", "python3", "chattr", "lsattr")


def missing_commands() -> list[str]:
    return [name for name in REQUIRED if shutil.which(name) is None]


def main() -> int:
    missing = missing_commands()
    if missing:
        print(f"missing_commands={','.join(missing)}", file=sys.stderr)
        return 1
    print("host_commands_ok=1")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
