#!/usr/bin/env python3
"""Verify SHM Perl helpers can run via stdin without host deploy-tools mount."""

from __future__ import annotations

import os
import subprocess
import sys


def run(
    argv: list[str],
    *,
    input_text: str | None = None,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        argv,
        input=input_text,
        capture_output=True,
        text=True,
        check=False,
    )


def main() -> int:
    container = os.environ["SHM_CONTAINER"]
    pay_systems_dir = os.environ.get("SHM_PAY_SYSTEMS_CONTAINER_DIR", "/app/data/pay_systems")

    mounted = run(["docker", "exec", container, "test", "-d", "/opt/vff-fiscal/deploy-tools"])
    if mounted.returncode == 0:
        print("deploy_tools_mounted_in_container=1", file=sys.stderr)
        return 1

    pay_systems = run(["docker", "exec", container, "test", "-d", pay_systems_dir])
    if pay_systems.returncode != 0:
        print("pay_systems_container_dir_missing=1", file=sys.stderr)
        return 1

    stdin_probe = run(
        ["docker", "exec", "-i", container, "sh", "-ec", "cd /app && exec perl -"],
        input_text='print "stdin_exec_ok=1\\n";',
    )
    if stdin_probe.returncode != 0 or "stdin_exec_ok=1" not in stdin_probe.stdout:
        print("stdin_exec_probe_failed=1", file=sys.stderr)
        return 1

    print("shm_exec_preflight_ok=1")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
