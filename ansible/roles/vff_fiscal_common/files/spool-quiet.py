#!/usr/bin/env python3
"""Detect active srv_customlab_nalog.cgi processes inside shm-spool-1."""

from __future__ import annotations

import os
import subprocess
import sys

CONTAINER = os.environ.get("SPOOL_CONTAINER", "shm-spool-1")
CGI_NAME = os.environ.get("CGI_NAME", "srv_customlab_nalog.cgi")


def main() -> int:
    result = subprocess.run(
        [
            "docker",
            "exec",
            CONTAINER,
            "sh",
            "-ec",
            f"ps -eo args | grep -F {CGI_NAME} | grep -v grep || true",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        print("spool_probe_failed=1", file=sys.stderr)
        return 2
    active = bool(result.stdout.strip())
    print(f"active_cgi_processes={1 if active else 0}")
    return 1 if active else 0


if __name__ == "__main__":
    raise SystemExit(main())
