#!/usr/bin/env python3
"""Validate Ansible stat mode strings for service deployment preflight."""

from __future__ import annotations

import re
import sys

ENV_ALLOWED_MODES = frozenset({"0600", "0400"})
STATE_REQUIRED_MODE = "0600"
MODE_PATTERN = re.compile(r"^[0-7]{4}$")


def normalize_mode(mode: object) -> str | None:
    if not isinstance(mode, str):
        return None
    value = mode.strip()
    if not MODE_PATTERN.fullmatch(value):
        return None
    return value


def env_mode_allowed(mode: object) -> bool:
    normalized = normalize_mode(mode)
    return normalized in ENV_ALLOWED_MODES if normalized is not None else False


def state_mode_allowed(mode: object) -> bool:
    normalized = normalize_mode(mode)
    return normalized == STATE_REQUIRED_MODE


def main(argv: list[str] | None = None) -> int:
    args = argv if argv is not None else sys.argv[1:]
    if len(args) != 2 or args[0] not in {"env", "state"}:
        print("usage: file_mode_gate.py <env|state> <mode>", file=sys.stderr)
        return 2

    check, mode = args
    allowed = env_mode_allowed(mode) if check == "env" else state_mode_allowed(mode)
    if not allowed:
        print(f"file_mode_rejected=1 check={check} mode={mode!r}", file=sys.stderr)
        return 1

    print(f"file_mode_ok=1 check={check} mode={normalize_mode(mode)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
