#!/usr/bin/env python3
"""Acquire the SHM spool cutover gate using Docker daemon process data."""

from __future__ import annotations

import os
import re
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


CGI_PATTERN = re.compile(rf"(?:^|[/\s]){re.escape(CGI_NAME)}(?:\s|$)")


def parse_top_rows(output: str) -> list[tuple[str, str]]:
    """Parse `docker top -eo pid,args` output without trusting column widths."""
    rows: list[tuple[str, str]] = []
    lines = output.splitlines()
    for line in lines[1:]:
        fields = line.strip().split(maxsplit=1)
        if len(fields) == 2 and fields[0].isdigit():
            rows.append((fields[0], fields[1]))
    return rows


def probe_active_cgi() -> tuple[bool, bool]:
    """Return (probe_ok, active); docker top works for paused containers."""
    result = run(["docker", "top", CONTAINER, "-eo", "pid,args"])
    if result.returncode != 0:
        print("spool_probe_failed=1", file=sys.stderr)
        return False, True
    active = any(CGI_PATTERN.search(args) for _, args in parse_top_rows(result.stdout))
    print(f"active_cgi_processes={1 if active else 0}")
    return True, active


def wait_quiet() -> bool:
    for _ in range(QUIET_RETRIES):
        probe_ok, active = probe_active_cgi()
        if probe_ok and not active:
            return True
        time.sleep(QUIET_DELAY)
    print("gate_quiet_timeout=1", file=sys.stderr)
    return False


def paused_state() -> tuple[bool, bool]:
    result = run(["docker", "inspect", "-f", "{{.State.Paused}}", CONTAINER])
    if result.returncode != 0 or result.stdout.strip() not in {"true", "false"}:
        print("spool_state_probe_failed=1", file=sys.stderr)
        return False, False
    return True, result.stdout.strip() == "true"


def pause() -> bool:
    return run(["docker", "pause", CONTAINER]).returncode == 0


def unpause() -> bool:
    return run(["docker", "unpause", CONTAINER]).returncode == 0


def unpause_and_verify() -> bool:
    if not unpause():
        return False
    state_ok, paused = paused_state()
    return state_ok and not paused


def main() -> int:
    was_already_paused = False
    for attempt in range(1, GATE_ATTEMPTS + 1):
        print(f"gate_attempt={attempt}")
        if not wait_quiet():
            return 1

        state_ok, was_already_paused = paused_state()
        if not state_ok:
            return 1
        paused_by_gate = False
        if not was_already_paused:
            if not pause():
                print("gate_pause_failed=1", file=sys.stderr)
                return 1
            paused_by_gate = True

        state_ok, now_paused = paused_state()
        if not state_ok or not now_paused:
            print("gate_not_paused=1", file=sys.stderr)
            if paused_by_gate:
                if not unpause_and_verify():
                    print("gate_cleanup_unpause_failed=1", file=sys.stderr)
                    print("spool_paused_by_gate=1")
                    print("spool_was_already_paused=0")
            return 1

        probe_ok, active = probe_active_cgi()
        if not probe_ok or active:
            print(f"post_pause_active_cgi=1", file=sys.stderr)
            if paused_by_gate:
                if not unpause_and_verify():
                    print("gate_retry_unpause_failed=1", file=sys.stderr)
                    print("spool_paused_by_gate=1")
                    print("spool_was_already_paused=0")
                    return 1
            time.sleep(GATE_RETRY_DELAY)
            continue

        print("spool_gate_acquired=1")
        print(f"spool_paused_by_gate={1 if paused_by_gate else 0}")
        print(f"spool_was_already_paused={1 if was_already_paused else 0}")
        return 0

    print("gate_exhausted=1", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
