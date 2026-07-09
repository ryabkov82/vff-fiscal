#!/usr/bin/env python3
"""Extract a service image from a simple Docker Compose file."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


def service_image(compose_path: Path, service: str) -> str:
    in_services = False
    service_indent: int | None = None
    in_service = False
    for raw in compose_path.read_text(encoding="utf-8").splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        stripped = raw.strip()
        if stripped == "services:":
            in_services = True
            continue
        if not in_services:
            continue
        if not raw.startswith(" ") and stripped.endswith(":"):
            in_services = False
            in_service = False
            continue
        if stripped == f"{service}:":
            service_indent = indent
            in_service = True
            continue
        if in_service and service_indent is not None:
            if indent <= service_indent:
                in_service = False
                service_indent = None
                continue
            if stripped.startswith("image:"):
                return stripped.split(":", 1)[1].strip().strip("\"'")
    raise ValueError(f"service image not found for {service}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--compose-file", required=True)
    parser.add_argument("--service", required=True)
    args = parser.parse_args()
    try:
        image = service_image(Path(args.compose_file), args.service)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print(image)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
