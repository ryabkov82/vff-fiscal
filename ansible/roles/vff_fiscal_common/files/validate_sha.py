#!/usr/bin/env python3
"""Validate deployment version strings."""

from __future__ import annotations

import re
import sys

FULL_SHA_RE = re.compile(r"^[0-9a-f]{40}$")
IMAGE_TAG_RE = re.compile(r"^(?:[0-9a-f]{12}|[0-9a-f]{40})$")


def is_full_sha(value: str) -> bool:
    return bool(FULL_SHA_RE.fullmatch(value))


def image_tag_from_sha(value: str) -> str:
    if not is_full_sha(value):
        raise ValueError("invalid full sha")
    return value[:12]


def is_image_tag(value: str) -> bool:
    return bool(IMAGE_TAG_RE.fullmatch(value))


def main() -> int:
    value = sys.argv[1] if len(sys.argv) > 1 else ""
    if not is_full_sha(value):
        print("invalid_full_sha=1", file=sys.stderr)
        return 1
    print(f"image_tag={image_tag_from_sha(value)}")
    print("valid_full_sha=1")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
