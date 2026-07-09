#!/usr/bin/env python3
"""Execute a host-resident SHM Perl helper inside a container via stdin."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

ALLOWED_SCRIPTS = frozenset({"shm-config.pl", "shm-auth-smoke.pl"})
CONFIG_COMMANDS = frozenset({"status", "clear-update-marker"})
SET_ENABLED_VALUES = frozenset({"0", "1"})


def build_perl_argv(script: str, args: list[str]) -> list[str]:
    if script == "shm-auth-smoke.pl":
        if args:
            raise ValueError("shm-auth-smoke.pl accepts no arguments")
        return []
    if script != "shm-config.pl":
        raise ValueError(f"unsupported script: {script}")
    if not args:
        return ["status"]
    command = args[0]
    if command == "set-enabled":
        if len(args) != 2 or args[1] not in SET_ENABLED_VALUES:
            raise ValueError("set-enabled requires 0 or 1")
        return args[:2]
    if command not in CONFIG_COMMANDS:
        raise ValueError(f"unsupported command: {command}")
    if len(args) != 1:
        raise ValueError(f"{command} accepts no extra arguments")
    return [command]


def main() -> int:
    container = os.environ["SHM_CONTAINER"]
    tools_dir = Path(os.environ["DEPLOY_TOOLS_DIR"])
    script_name = os.environ.get("SHM_SCRIPT", "shm-config.pl")
    if script_name not in ALLOWED_SCRIPTS:
        print("unsupported_script=1", file=sys.stderr)
        return 2

    script_path = tools_dir / script_name
    if not script_path.is_file():
        print("script_missing=1", file=sys.stderr)
        return 2

    try:
        perl_args = build_perl_argv(script_name, sys.argv[1:])
    except ValueError:
        print("invalid_args=1", file=sys.stderr)
        return 2

    arg_str = " ".join(perl_args)
    remote_cmd = "cd /app && exec perl -" + (f" {arg_str}" if arg_str else "")

    with script_path.open("rb") as script_handle:
        result = subprocess.run(
            ["docker", "exec", "-i", container, "sh", "-ec", remote_cmd],
            stdin=script_handle,
            capture_output=True,
            check=False,
        )

    if result.stdout:
        sys.stdout.buffer.write(result.stdout)
    if result.stderr:
        sys.stderr.buffer.write(result.stderr)
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
