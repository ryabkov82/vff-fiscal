#!/usr/bin/env python3
"""Validate and reconcile idempotent service deploy manifests from trusted backups."""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Callable

FULL_SHA_RE = re.compile(r"^[0-9a-f]{40}$")
IMMUTABLE_TAG_RE = re.compile(r"^[0-9a-f]{12}$|^[0-9a-f]{40}$")
SHA256_ID_RE = re.compile(r"^sha256:[0-9a-f]{64}$")

MANIFEST_FIELDS = (
    "service_commit",
    "service_image",
    "service_image_id",
    "deployed_at",
    "backup_directory",
    "previous_service_image",
    "previous_service_image_id",
    "adapter_commit",
    "adapter_sha256",
    "adapter_enabled",
    "need_update_to_present",
    "deployment_status",
)


def load_json_file(path: Path) -> dict[str, object]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ValueError(f"missing JSON file: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON in {path}: {exc.msg}") from exc
    if not isinstance(payload, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return payload


def normalize_string(value: object) -> str:
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value).strip()


def normalize_bool(value: object) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def is_immutable_image(image: str, repository: str) -> bool:
    prefix = f"{repository}:"
    if not image.startswith(prefix):
        return False
    tag = image[len(prefix) :]
    return bool(IMMUTABLE_TAG_RE.fullmatch(tag))


def is_sha256_image_id(value: str) -> bool:
    return bool(SHA256_ID_RE.fullmatch(value))


def validate_backup_directory(backup_dir: str, backup_root: str) -> Path:
    path = Path(backup_dir)
    if not path.is_absolute():
        raise ValueError("backup_directory must be absolute")
    root = Path(backup_root).resolve()
    resolved = path.resolve()
    releases_root = root
    try:
        resolved.relative_to(releases_root)
    except ValueError as exc:
        raise ValueError("backup_directory must be under configured backup root") from exc
    if not resolved.is_dir():
        raise ValueError("backup_directory does not exist")
    return resolved


def validate_backup_meta(
    backup_meta: dict[str, object],
    *,
    backup_dir: Path,
    backup_root: str,
    target_commit: str,
    target_image: str,
    target_image_id: str,
    running_image_tag: str,
    running_image_id: str,
    image_repository: str,
    resolved_previous_image_id: str,
) -> None:
    if normalize_string(backup_meta.get("component")) != "service":
        raise ValueError("backup component must be service")
    if normalize_string(backup_meta.get("target_commit")) != target_commit:
        raise ValueError("backup target_commit mismatch")
    if normalize_string(backup_meta.get("target_image")) != target_image:
        raise ValueError("backup target_image mismatch")
    if running_image_tag != target_image:
        raise ValueError("running container tag mismatch")
    if running_image_id != target_image_id:
        raise ValueError("running container image ID mismatch")
    if not FULL_SHA_RE.fullmatch(target_commit):
        raise ValueError("target commit must be a 40-character SHA")

    previous_image = normalize_string(backup_meta.get("previous_image"))
    previous_image_id = normalize_string(backup_meta.get("previous_image_id"))
    deployed_at = normalize_string(backup_meta.get("deployed_at"))
    if not is_immutable_image(previous_image, image_repository):
        raise ValueError("backup previous_image must use an immutable commit tag")
    if not is_sha256_image_id(previous_image_id):
        raise ValueError("backup previous_image_id must be a sha256 image ID")
    if resolved_previous_image_id != previous_image_id:
        raise ValueError("backup previous_image does not resolve to previous_image_id")
    if not deployed_at:
        raise ValueError("backup deployed_at must be non-empty")

    meta_backup_dir = normalize_string(backup_meta.get("backup_directory"))
    if meta_backup_dir and Path(meta_backup_dir).resolve() != backup_dir.resolve():
        raise ValueError("backup backup_directory mismatch")

    validate_backup_directory(str(backup_dir), backup_root)


def build_canonical_manifest(
    existing: dict[str, object],
    backup_meta: dict[str, object],
    *,
    backup_dir: Path,
    target_commit: str,
    target_image: str,
    target_image_id: str,
) -> dict[str, object]:
    deployment_status = normalize_string(existing.get("deployment_status")) or "success"
    if deployment_status != "success":
        deployment_status = "success"
    return {
        "service_commit": target_commit,
        "service_image": target_image,
        "service_image_id": target_image_id,
        "deployed_at": normalize_string(backup_meta.get("deployed_at")),
        "backup_directory": str(backup_dir),
        "previous_service_image": normalize_string(backup_meta.get("previous_image")),
        "previous_service_image_id": normalize_string(backup_meta.get("previous_image_id")),
        "adapter_commit": normalize_string(existing.get("adapter_commit")),
        "adapter_sha256": normalize_string(existing.get("adapter_sha256")),
        "adapter_enabled": normalize_bool(existing.get("adapter_enabled")),
        "need_update_to_present": normalize_bool(existing.get("need_update_to_present")),
        "deployment_status": deployment_status,
    }


def canonical_manifest_dict(manifest: dict[str, object]) -> dict[str, object]:
    return {
        field: (
            normalize_bool(manifest.get(field))
            if field in {"adapter_enabled", "need_update_to_present"}
            else normalize_string(manifest.get(field))
        )
        for field in MANIFEST_FIELDS
    }


def manifests_equal(left: dict[str, object], right: dict[str, object]) -> bool:
    return canonical_manifest_dict(left) == canonical_manifest_dict(right)


def default_image_id_resolver(image_ref: str) -> str:
    result = subprocess.run(
        ["docker", "image", "inspect", "-f", "{{.Id}}", image_ref],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise ValueError(f"unable to resolve image ID for {image_ref}")
    image_id = result.stdout.strip()
    if not is_sha256_image_id(image_id):
        raise ValueError(f"resolved image ID for {image_ref} is invalid")
    return image_id


def prepare_reconciliation(
    *,
    manifest_path: Path,
    backup_root: str,
    image_repository: str,
    target_commit: str,
    target_image: str,
    target_image_id: str,
    running_image_tag: str,
    running_image_id: str,
    resolved_previous_image_id: str | None = None,
    image_id_resolver: Callable[[str], str] | None = None,
    canonical_manifest_path: Path | None = None,
) -> bool:
    existing = load_json_file(manifest_path)
    backup_dir_value = normalize_string(existing.get("backup_directory"))
    if not backup_dir_value:
        raise ValueError("existing manifest backup_directory is missing")
    backup_dir = validate_backup_directory(backup_dir_value, backup_root)
    backup_meta = load_json_file(backup_dir / "backup-meta.json")

    previous_image = normalize_string(backup_meta.get("previous_image"))
    resolver = image_id_resolver or default_image_id_resolver
    resolved_previous = resolved_previous_image_id or resolver(previous_image)

    validate_backup_meta(
        backup_meta,
        backup_dir=backup_dir,
        backup_root=backup_root,
        target_commit=target_commit,
        target_image=target_image,
        target_image_id=target_image_id,
        running_image_tag=running_image_tag,
        running_image_id=running_image_id,
        image_repository=image_repository,
        resolved_previous_image_id=resolved_previous,
    )

    canonical = build_canonical_manifest(
        existing,
        backup_meta,
        backup_dir=backup_dir,
        target_commit=target_commit,
        target_image=target_image,
        target_image_id=target_image_id,
    )
    required = not manifests_equal(existing, canonical)
    if required and canonical_manifest_path is not None:
        canonical_manifest_path.write_text(
            json.dumps(canonical, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        os.chmod(canonical_manifest_path, 0o600)
    return required


def main() -> int:
    if len(sys.argv) != 2 or sys.argv[1] != "prepare":
        print("usage: service_manifest_reconcile.py prepare", file=sys.stderr)
        return 2

    manifest_path = Path(os.environ["MANIFEST_PATH"])
    backup_root = os.environ["BACKUP_ROOT"]
    image_repository = os.environ["IMAGE_REPOSITORY"]
    target_commit = os.environ["TARGET_COMMIT"]
    target_image = os.environ["TARGET_IMAGE"]
    target_image_id = os.environ["TARGET_IMAGE_ID"]
    running_image_tag = os.environ["RUNNING_IMAGE_TAG"]
    running_image_id = os.environ["RUNNING_IMAGE_ID"]
    canonical_manifest_path = Path(os.environ.get("CANONICAL_MANIFEST_PATH", ""))
    resolved_previous = os.environ.get("RESOLVED_BACKUP_PREVIOUS_IMAGE_ID") or None
    payload_path = canonical_manifest_path if str(canonical_manifest_path) else None

    try:
        required = prepare_reconciliation(
            manifest_path=manifest_path,
            backup_root=backup_root,
            image_repository=image_repository,
            target_commit=target_commit,
            target_image=target_image,
            target_image_id=target_image_id,
            running_image_tag=running_image_tag,
            running_image_id=running_image_id,
            resolved_previous_image_id=resolved_previous,
            canonical_manifest_path=payload_path,
        )
    except ValueError as exc:
        print("manifest_reconciliation_failed=1", file=sys.stderr)
        print(str(exc), file=sys.stderr)
        return 1

    print(f"manifest_reconciliation_required={1 if required else 0}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
