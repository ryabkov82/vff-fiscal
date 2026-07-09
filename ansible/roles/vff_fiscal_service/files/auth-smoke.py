#!/usr/bin/env python3
"""Perform authenticated GET /v1/user without printing secrets or PII."""

from __future__ import annotations

import os
import sys
import urllib.error
import urllib.request
from pathlib import Path


def parse_api_key(env_file: Path) -> str:
    for raw_line in env_file.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key.strip() == "VFF_FISCAL_API_KEY":
            token = value.strip()
            if token:
                return token
    raise RuntimeError("VFF_FISCAL_API_KEY is missing")


def main() -> int:
    env_file = Path(os.environ["ENV_FILE"])
    url = os.environ["USER_URL"]
    api_key = parse_api_key(env_file)
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/json",
            "Authorization": f"Bearer {api_key}",
        },
        method="GET",
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            if response.status != 200:
                return 1
    except urllib.error.HTTPError:
        return 1
    except urllib.error.URLError:
        return 1

    print("authenticated_user_check=ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
