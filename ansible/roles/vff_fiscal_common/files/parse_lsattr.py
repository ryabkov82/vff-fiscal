#!/usr/bin/env python3
"""Parse lsattr output and report the immutable flag safely."""

from __future__ import annotations

import re
import sys

ATTRS_RE = re.compile(r"^(?P<attrs>[-A-Za-z]+)\s+")


def immutable_from_lsattr(output: str) -> bool:
    lines = [line.strip() for line in output.splitlines() if line.strip()]
    if len(lines) != 1:
        raise ValueError("expected exactly one lsattr row")
    match = ATTRS_RE.match(lines[0])
    if not match:
        raise ValueError("invalid lsattr output")
    return "i" in match.group("attrs")


def main() -> int:
    try:
        immutable = immutable_from_lsattr(sys.stdin.read())
    except ValueError:
        print("lsattr_parse_ok=0", file=sys.stderr)
        return 2
    print("lsattr_parse_ok=1")
    print(f"immutable={1 if immutable else 0}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
