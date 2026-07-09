#!/usr/bin/env python3
"""Inspect or rewrite a single service image in a simple Docker Compose file."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


def _service_image_lines(lines: list[str], service: str) -> tuple[int, int, str]:
    in_services = False
    service_indent: int | None = None
    in_service = False
    service_count = 0
    image_line = -1
    image_indent = ""
    image = ""
    for idx, raw in enumerate(lines):
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
            service_count += 1
            if service_count > 1:
                raise ValueError(f"service {service} appears more than once")
            service_indent = indent
            in_service = True
            continue
        if in_service and service_indent is not None:
            if indent <= service_indent:
                in_service = False
                service_indent = None
                continue
            if stripped.startswith("image:"):
                if image_line != -1:
                    raise ValueError(f"service {service} has more than one image")
                image_line = idx
                image_indent = raw[:indent]
                image = stripped.split(":", 1)[1].strip().strip("\"'")
    if service_count != 1:
        raise ValueError(f"service {service} not found")
    if image_line == -1:
        raise ValueError(f"service image not found for {service}")
    return image_line, len(image_indent), image


def service_image(compose_path: Path, service: str) -> str:
    lines = compose_path.read_text(encoding="utf-8").splitlines()
    _, _, image = _service_image_lines(lines, service)
    return image


def replace_service_image(compose_path: Path, service: str, image: str) -> str:
    text = compose_path.read_text(encoding="utf-8")
    lines = text.splitlines()
    image_line, image_indent, old_image = _service_image_lines(lines, service)
    lines[image_line] = f"{' ' * image_indent}image: {image}"
    trailing_newline = "\n" if text.endswith("\n") else ""
    compose_path.write_text("\n".join(lines) + trailing_newline, encoding="utf-8")
    return old_image


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--compose-file", required=True)
    parser.add_argument("--service", required=True)
    parser.add_argument("--replace-image", default="")
    args = parser.parse_args()
    try:
        compose_path = Path(args.compose_file)
        if args.replace_image:
            old_image = replace_service_image(compose_path, args.service, args.replace_image)
            print(old_image)
        else:
            image = service_image(compose_path, args.service)
            print(image)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
