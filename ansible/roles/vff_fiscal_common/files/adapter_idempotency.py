#!/usr/bin/env python3
"""Decide whether adapter cutover can be skipped safely."""

from __future__ import annotations

import json
import os
import sys


REQUIRED_FILES = (
    "cgi",
    "AdapterConfig.pm",
    "PaymentTimestamp.pm",
)


def parse_bool(value: str) -> bool:
    return value.lower() in {"1", "true", "yes", "on"}


def main() -> int:
    staged = {
        "cgi": os.environ.get("STAGED_CGI_SHA256", ""),
        "AdapterConfig.pm": os.environ.get("STAGED_ADAPTER_CONFIG_SHA256", ""),
        "PaymentTimestamp.pm": os.environ.get("STAGED_PAYMENT_TIMESTAMP_SHA256", ""),
    }
    active = {
        "cgi": os.environ.get("ACTIVE_CGI_SHA256", ""),
        "AdapterConfig.pm": os.environ.get("ACTIVE_ADAPTER_CONFIG_SHA256", ""),
        "PaymentTimestamp.pm": os.environ.get("ACTIVE_PAYMENT_TIMESTAMP_SHA256", ""),
    }

    for name in REQUIRED_FILES:
        if not staged[name]:
            print(f"skip_cutover=0 reason=missing_staged_{name}")
            return 0
        if not active[name]:
            print(f"skip_cutover=0 reason=missing_active_{name}")
            return 0
        if staged[name] != active[name]:
            print(f"skip_cutover=0 reason=checksum_mismatch_{name}")
            return 0

    if not parse_bool(os.environ.get("ADAPTER_IMMUTABLE", "0")):
        print("skip_cutover=0 reason=missing_immutable")
        return 0

    status_raw = os.environ.get("SHM_STATUS_JSON", "{}")
    try:
        status = json.loads(status_raw)
    except json.JSONDecodeError:
        print("skip_cutover=0 reason=invalid_status_json")
        return 0

    if status.get("need_update_to_defined") in (True, "true", "True", 1, "1"):
        print("skip_cutover=0 reason=need_update_to_present")
        return 0

    if not parse_bool(os.environ.get("DIAGNOSTIC_OK", "0")):
        print("skip_cutover=0 reason=diagnostic_failed")
        return 0

    print("skip_cutover=1 reason=all_checks_passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
