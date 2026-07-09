#!/usr/bin/env python3
"""Acquire SHM spool cutover gate with post-pause process recheck."""

from __future__ import annotations

import os
import subprocess
import sys
import time

CONTAINER = os.environ.get("SPOOL_CONTAINER", "shm-spool-1")
CGI_NAME = os.environ.get("CGI_NAME", "srv_customlab_nalog.cgi")
QUIET_RETRIES = int(os.environ.get("QUIET_RETRIES", "60"))
QUIET_DELAY = float(os.environ.get("QUIET_DELAY", "5"))
GATE_ATTEMPTS = int(os.environ.get("GATE_ATTEMPTS", "5"))
GATE_RETRY_DELAY = float(os.environ.get("GATE_RETRY_DELAY", "2"))


def run(argv: list[str], *, text: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(argv, capture_output=True, text=text, check=False)


def has_active_cgi() -> bool:
    result = run(
        [
            "docker",
            "exec",
            CONTAINER,
            "sh",
            "-ec",
            f"ps -eo args | grep -F {CGI_NAME} | grep -v grep || true",
        ]
    )
    if result.returncode != 0:
        print("spool_probe_failed=1", file=sys.stderr)
        return True
    active = bool(result.stdout.strip())
    print(f"active_cgi_processes={1 if active else 0}")
    return active


def wait_quiet() -> bool:
    for _ in range(QUIET_RETRIES):
        if not has_active_cgi():
            return True
        time.sleep(QUIET_DELAY)
    print("gate_quiet_timeout=1", file=sys.stderr)
    return False


def is_paused() -> bool:
    result = run(["docker", "inspect", "-f", "{{.State.Paused}}", CONTAINER])
    return result.returncode == 0 and result.stdout.strip() == "true"


def pause() -> bool:
    return run(["docker", "pause", CONTAINER]).returncode == 0


def unpause() -> bool:
    return run(["docker", "unpause", CONTAINER]).returncode == 0


def main() -> int:
    for attempt in range(1, GATE_ATTEMPTS + 1):
        print(f"gate_attempt={attempt}")
        if not wait_quiet():
            unpause()
            return 1

        was_already_paused = is_paused()
        paused_by_gate = False
        if not was_already_paused:
            if not pause():
                print("gate_pause_failed=1", file=sys.stderr)
                return 1
            paused_by_gate = True

        if not is_paused():
            print("gate_not_paused=1", file=sys.stderr)
            if paused_by_gate:
                unpause()
            return 1

        if has_active_cgi():
            print(f"post_pause_active_cgi=1", file=sys.stderr)
            if paused_by_gate:
                unpause()
            time.sleep(GATE_RETRY_DELAY)
            continue

        print("spool_gate_acquired=1")
        print(f"spool_was_paused_by_gate={1 if paused_by_gate else 0}")
        return 0

    unpause()
    print("gate_exhausted=1", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
