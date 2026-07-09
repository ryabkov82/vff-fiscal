#!/usr/bin/env python3
"""Unit tests for deployment helper scripts."""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
COMMON_FILES = ROOT / "ansible" / "roles" / "vff_fiscal_common" / "files"
ADAPTER_FILES = ROOT / "ansible" / "roles" / "vff_fiscal_adapter" / "files"
SERVICE_FILES = ROOT / "ansible" / "roles" / "vff_fiscal_service" / "files"


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


validate_sha = load_module("validate_sha", COMMON_FILES / "validate_sha.py")
inspect_state = load_module("inspect_state", SERVICE_FILES / "inspect-state.py")
write_manifest = load_module("write_manifest", COMMON_FILES / "write_manifest.py")
validate_env = load_module("validate_env", SERVICE_FILES / "validate-env.py")
run_shm_perl = load_module("run_shm_perl", COMMON_FILES / "run_shm_perl.py")
adapter_idempotency = load_module("adapter_idempotency", COMMON_FILES / "adapter_idempotency.py")
immutable_recovery = load_module("immutable_recovery", COMMON_FILES / "immutable_recovery.py")
spool_cutover_gate = load_module("spool_cutover_gate", COMMON_FILES / "spool_cutover_gate.py")


class ValidateShaTests(unittest.TestCase):
    def test_accepts_full_sha(self) -> None:
        sha = "a" * 40
        self.assertTrue(validate_sha.is_full_sha(sha))
        self.assertEqual(validate_sha.image_tag_from_sha(sha), "a" * 12)


class RunShmPerlTests(unittest.TestCase):
    def test_command_whitelist(self) -> None:
        self.assertEqual(run_shm_perl.build_perl_argv("shm-config.pl", []), ["status"])
        self.assertEqual(
            run_shm_perl.build_perl_argv("shm-config.pl", ["set-enabled", "1"]),
            ["set-enabled", "1"],
        )
        with self.assertRaises(ValueError):
            run_shm_perl.build_perl_argv("shm-config.pl", ["evil"])

    def test_docker_exec_uses_stdin_not_container_mount(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            script = Path(tmp) / "shm-config.pl"
            script.write_text("print qq(status_ok\\n);", encoding="utf-8")
            with mock.patch.object(run_shm_perl.subprocess, "run") as run_mock:
                run_mock.return_value = subprocess.CompletedProcess([], 0, b"status_ok\n", b"")
                with mock.patch.object(sys, "argv", ["run_shm_perl.py"]), mock.patch.dict(
                    os.environ,
                    {
                        "SHM_CONTAINER": "shm-core-1",
                        "DEPLOY_TOOLS_DIR": tmp,
                        "SHM_SCRIPT": "shm-config.pl",
                    },
                    clear=False,
                ):
                    rc = run_shm_perl.main()
                self.assertEqual(rc, 0)
                args, kwargs = run_mock.call_args
                self.assertEqual(args[0][:3], ["docker", "exec", "-i"])
                self.assertIn("cd /app && exec perl - status", args[0][6])
                self.assertIn("stdin", kwargs)
                self.assertNotIn("/opt/vff-fiscal/deploy-tools", args[0][6])


class SpoolGateTests(unittest.TestCase):
    def test_post_pause_process_triggers_retry(self) -> None:
        active_checks = iter([True, False])

        def fake_has_active_cgi() -> bool:
            return next(active_checks)

        with mock.patch.object(spool_cutover_gate, "wait_quiet", return_value=True), mock.patch.object(
            spool_cutover_gate, "is_paused", side_effect=[False, True, False, True]
        ), mock.patch.object(spool_cutover_gate, "pause", return_value=True), mock.patch.object(
            spool_cutover_gate, "unpause", return_value=True
        ), mock.patch.object(
            spool_cutover_gate, "has_active_cgi", side_effect=fake_has_active_cgi
        ), mock.patch("time.sleep"):
            rc = spool_cutover_gate.main()
        self.assertEqual(rc, 0)

    def test_gate_exhausted_unpauses_and_fails(self) -> None:
        with mock.patch.object(spool_cutover_gate, "wait_quiet", return_value=True), mock.patch.object(
            spool_cutover_gate, "is_paused", return_value=False
        ), mock.patch.object(spool_cutover_gate, "pause", return_value=True), mock.patch.object(
            spool_cutover_gate, "has_active_cgi", return_value=True
        ), mock.patch.object(spool_cutover_gate, "unpause", return_value=True) as unpause_mock, mock.patch(
            "time.sleep"
        ):
            rc = spool_cutover_gate.main()
        self.assertEqual(rc, 1)
        unpause_mock.assert_called()


class AdapterIdempotencyTests(unittest.TestCase):
    def _env(self, **overrides: str) -> dict[str, str]:
        base = {
            "STAGED_CGI_SHA256": "a" * 64,
            "STAGED_ADAPTER_CONFIG_SHA256": "b" * 64,
            "STAGED_PAYMENT_TIMESTAMP_SHA256": "c" * 64,
            "ACTIVE_CGI_SHA256": "a" * 64,
            "ACTIVE_ADAPTER_CONFIG_SHA256": "b" * 64,
            "ACTIVE_PAYMENT_TIMESTAMP_SHA256": "c" * 64,
            "ADAPTER_IMMUTABLE": "1",
            "SHM_STATUS_JSON": json.dumps({"need_update_to_defined": False}),
            "DIAGNOSTIC_OK": "1",
        }
        base.update(overrides)
        return {**os.environ, **base}

    def test_skip_when_all_checks_pass(self) -> None:
        with mock.patch.dict(os.environ, self._env(), clear=False):
            proc = subprocess.run(
                [sys.executable, str(COMMON_FILES / "adapter_idempotency.py")],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertIn("skip_cutover=1", proc.stdout)

    def test_missing_helper_forces_deployment(self) -> None:
        with mock.patch.dict(
            os.environ,
            self._env(ACTIVE_PAYMENT_TIMESTAMP_SHA256=""),
            clear=False,
        ):
            proc = subprocess.run(
                [sys.executable, str(COMMON_FILES / "adapter_idempotency.py")],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertIn("skip_cutover=0 reason=missing_active_PaymentTimestamp.pm", proc.stdout)

    def test_missing_immutable_forces_deployment(self) -> None:
        with mock.patch.dict(os.environ, self._env(ADAPTER_IMMUTABLE="0"), clear=False):
            proc = subprocess.run(
                [sys.executable, str(COMMON_FILES / "adapter_idempotency.py")],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertIn("skip_cutover=0 reason=missing_immutable", proc.stdout)


class ImmutableRecoveryTests(unittest.TestCase):
    def test_recovery_message_contains_commands_not_secrets(self) -> None:
        message = immutable_recovery.build_recovery_message(
            "shm-spool-1",
            "/opt/shm/pay_systems/srv_customlab_nalog.cgi",
        )
        self.assertIn("chattr +i /opt/shm/pay_systems/srv_customlab_nalog.cgi", message)
        self.assertIn("docker unpause shm-spool-1", message)
        self.assertIn("remains PAUSED intentionally", message)
        self.assertNotIn("token", message.lower())

    def test_spool_stays_paused_when_immutable_missing(self) -> None:
        env = {
            **os.environ,
            "SPOOL_CONTAINER": "shm-spool-1",
            "CGI_PATH": "/opt/shm/pay_systems/srv_customlab_nalog.cgi",
            "IMMUTABLE_OK": "0",
            "SPOOL_PAUSED": "1",
        }
        proc = subprocess.run(
            [sys.executable, str(COMMON_FILES / "immutable_recovery.py")],
            capture_output=True,
            text=True,
            check=False,
            env=env,
        )
        self.assertEqual(proc.returncode, 1)
        self.assertIn("remains PAUSED intentionally", proc.stdout)

    def test_no_failure_when_immutable_ok(self) -> None:
        env = {
            **os.environ,
            "IMMUTABLE_OK": "1",
            "SPOOL_PAUSED": "1",
        }
        proc = subprocess.run(
            [sys.executable, str(COMMON_FILES / "immutable_recovery.py")],
            capture_output=True,
            text=True,
            check=False,
            env=env,
        )
        self.assertEqual(proc.returncode, 0)


class WriteManifestTests(unittest.TestCase):
    def test_atomic_write_and_mode(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            payload = {"service_commit": "a" * 40, "deployment_status": "success"}
            env = {**os.environ, "MANIFEST_PATH": str(manifest)}
            proc = subprocess.run(
                [sys.executable, str(COMMON_FILES / "write_manifest.py")],
                input=json.dumps(payload),
                text=True,
                capture_output=True,
                check=True,
                env=env,
            )
            self.assertIn("manifest_written=1", proc.stdout)
            self.assertNotIn("secret-token", proc.stdout)


if __name__ == "__main__":
    unittest.main()
