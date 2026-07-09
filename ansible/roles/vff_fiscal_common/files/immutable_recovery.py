#!/usr/bin/env python3
"""Emit safe recovery instructions when immutable protection cannot be verified."""

from __future__ import annotations

import os
import sys


def build_recovery_message(spool: str, cgi_path: str) -> str:
    return (
        f"SHM spool ({spool}) remains PAUSED intentionally because chattr +i "
        f"could not be verified on {cgi_path}.\n"
        "Before unpausing, verify the CGI SHA256 and run perl -c on the CGI "
        "and helper modules.\n"
        "Recovery commands:\n"
        f"  chattr +i {cgi_path}\n"
        f"  docker unpause {spool}\n"
    )


def main() -> int:
    spool = os.environ.get("SPOOL_CONTAINER", "shm-spool-1")
    cgi_path = os.environ.get("CGI_PATH", "/opt/shm/pay_systems/srv_customlab_nalog.cgi")
    immutable_ok = os.environ.get("IMMUTABLE_OK", "0") == "1"
    spool_paused = os.environ.get("SPOOL_PAUSED", "0") == "1"

    if immutable_ok or not spool_paused:
        print("immutable_recovery_not_required=1")
        return 0

    message = build_recovery_message(spool, cgi_path)
    print(message, end="")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
