#!/usr/bin/env python3
"""Production-safety tests for deployment helpers and transaction roles."""

from __future__ import annotations

import importlib.util
import json
import os
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
COMMON = ROOT / "ansible/roles/vff_fiscal_common"
ADAPTER = ROOT / "ansible/roles/vff_fiscal_adapter"
SERVICE = ROOT / "ansible/roles/vff_fiscal_service"


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


validate_sha = load_module("validate_sha", COMMON / "files/validate_sha.py")
parse_lsattr = load_module("parse_lsattr", COMMON / "files/parse_lsattr.py")
host_preflight = load_module("host_preflight", COMMON / "files/host_preflight.py")
immutable_recovery = load_module(
    "immutable_recovery", COMMON / "files/immutable_recovery.py"
)
run_shm_perl = load_module("run_shm_perl", COMMON / "files/run_shm_perl.py")


FAKE_DOCKER = r"""#!/usr/bin/env python3
import json
import os
import sys
from pathlib import Path

state_path = Path(os.environ["FAKE_DOCKER_STATE"])
state = json.loads(state_path.read_text()) if state_path.exists() else {
    "paused": os.environ.get("FAKE_INITIAL_PAUSED") == "1",
    "top_calls": 0,
    "pause_calls": 0,
    "unpause_calls": 0,
}
argv = sys.argv[1:]
with Path(os.environ["FAKE_DOCKER_LOG"]).open("a") as log:
    log.write(json.dumps(argv) + "\n")

def save():
    state_path.write_text(json.dumps(state))

if argv[0] == "inspect":
    print("true" if state["paused"] else "false")
elif argv[0] == "pause":
    state["pause_calls"] += 1
    state["paused"] = True
    save()
elif argv[0] == "unpause":
    state["unpause_calls"] += 1
    if os.environ.get("FAKE_UNPAUSE_FAIL") == "1":
        save()
        sys.exit(1)
    state["paused"] = False
    save()
elif argv[0] == "top":
    state["top_calls"] += 1
    scenario = os.environ.get("FAKE_SCENARIO", "quiet")
    active = False
    if scenario == "race_once":
        active = state["paused"] and state["pause_calls"] == 1
    elif scenario == "post_pause_active":
        active = state["paused"]
    elif scenario == "probe_failure":
        save()
        sys.exit(1)
    print("PID COMMAND")
    if active:
        print("4242 /usr/bin/perl /app/data/pay_systems/srv_customlab_nalog.cgi action=send")
    else:
        print("100 /usr/sbin/cron -f")
    save()
elif argv[0] == "exec":
    if state["paused"]:
        print("cannot exec in a paused container", file=sys.stderr)
        sys.exit(1)
    save()
else:
    print("unsupported fake docker command", file=sys.stderr)
    sys.exit(2)
"""


class FakeDockerGateTests(unittest.TestCase):
    def run_gate(
        self,
        scenario: str,
        *,
        initially_paused: bool = False,
        unpause_fails: bool = False,
        attempts: int = 3,
    ) -> tuple[subprocess.CompletedProcess[str], dict, list[list[str]]]:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            docker = tmp_path / "docker"
            docker.write_text(FAKE_DOCKER, encoding="utf-8")
            docker.chmod(docker.stat().st_mode | stat.S_IXUSR)
            state_path = tmp_path / "state.json"
            log_path = tmp_path / "docker.log"
            env = {
                **os.environ,
                "PATH": f"{tmp}:{os.environ['PATH']}",
                "FAKE_DOCKER_STATE": str(state_path),
                "FAKE_DOCKER_LOG": str(log_path),
                "FAKE_SCENARIO": scenario,
                "FAKE_INITIAL_PAUSED": "1" if initially_paused else "0",
                "FAKE_UNPAUSE_FAIL": "1" if unpause_fails else "0",
                "QUIET_RETRIES": "1",
                "QUIET_DELAY": "0",
                "GATE_ATTEMPTS": str(attempts),
                "GATE_RETRY_DELAY": "0",
                "SPOOL_CONTAINER": "shm-spool-1",
            }
            proc = subprocess.run(
                [sys.executable, str(COMMON / "files/spool_cutover_gate.py")],
                text=True,
                capture_output=True,
                check=False,
                env=env,
            )
            state = json.loads(state_path.read_text())
            commands = [
                json.loads(line) for line in log_path.read_text().splitlines()
            ]
            return proc, state, commands

    def test_uses_docker_top_before_and_after_pause(self) -> None:
        proc, state, commands = self.run_gate("quiet")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("spool_gate_acquired=1", proc.stdout)
        self.assertIn("spool_paused_by_gate=1", proc.stdout)
        self.assertIn("spool_was_already_paused=0", proc.stdout)
        self.assertEqual(sum(command[0] == "top" for command in commands), 2)
        self.assertFalse(any(command[0] == "exec" for command in commands))
        self.assertTrue(state["paused"])

    def test_process_race_retries_complete_gate(self) -> None:
        proc, state, commands = self.run_gate("race_once")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(state["pause_calls"], 2)
        self.assertEqual(state["unpause_calls"], 1)
        self.assertGreaterEqual(sum(c[0] == "top" for c in commands), 4)

    def test_gate_exhaustion_unpauses_operation_owned_spool(self) -> None:
        proc, state, _ = self.run_gate("post_pause_active", attempts=2)
        self.assertNotEqual(proc.returncode, 0)
        self.assertFalse(state["paused"])
        self.assertEqual(state["unpause_calls"], 2)

    def test_operator_prepaused_spool_is_never_unpaused(self) -> None:
        proc, state, commands = self.run_gate(
            "post_pause_active", initially_paused=True, attempts=2
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertTrue(state["paused"])
        self.assertFalse(any(command[0] == "unpause" for command in commands))

    def test_unpause_failure_during_retry_is_fatal(self) -> None:
        proc, state, _ = self.run_gate(
            "post_pause_active", unpause_fails=True, attempts=2
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertTrue(state["paused"])
        self.assertIn("gate_retry_unpause_failed=1", proc.stderr)

    def test_failed_top_probe_is_unsafe(self) -> None:
        proc, _, _ = self.run_gate("probe_failure")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("spool_probe_failed=1", proc.stderr)


class HelperSafetyTests(unittest.TestCase):
    def test_lsattr_parser_reads_attribute_column_only(self) -> None:
        self.assertTrue(
            parse_lsattr.immutable_from_lsattr(
                "----i---------e------- /opt/shm/pay_systems/adapter.cgi\n"
            )
        )
        self.assertFalse(
            parse_lsattr.immutable_from_lsattr(
                "--------------e------- /tmp/file-with-i-in-name\n"
            )
        )
        with self.assertRaises(ValueError):
            parse_lsattr.immutable_from_lsattr("malformed")

    def test_shm_helper_is_streamed_over_stdin(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, "shm-config.pl").write_text("print 1;", encoding="utf-8")
            with mock.patch.object(run_shm_perl.subprocess, "run") as run_mock:
                run_mock.return_value = subprocess.CompletedProcess([], 0, b"{}", b"")
                with mock.patch.object(sys, "argv", ["run_shm_perl.py"]), mock.patch.dict(
                    os.environ,
                    {
                        "SHM_CONTAINER": "shm-core-1",
                        "DEPLOY_TOOLS_DIR": tmp,
                        "SHM_SCRIPT": "shm-config.pl",
                    },
                ):
                    self.assertEqual(run_shm_perl.main(), 0)
            argv = run_mock.call_args.args[0]
            self.assertEqual(argv[:3], ["docker", "exec", "-i"])
            self.assertIn("exec perl - status", argv[-1])
            self.assertNotIn("/opt/vff-fiscal", argv[-1])

    def test_preflight_uses_shutil_which(self) -> None:
        with mock.patch.object(host_preflight.shutil, "which", return_value="/bin/tool"):
            self.assertEqual(host_preflight.missing_commands(), [])
        common_tasks = (COMMON / "tasks/main.yml").read_text()
        self.assertNotIn("command -v", common_tasks)

    def test_exact_sha_validation(self) -> None:
        self.assertTrue(validate_sha.is_full_sha("a" * 40))
        self.assertFalse(validate_sha.is_full_sha("A" * 40))

    def test_immutable_failure_message_is_safe(self) -> None:
        message = immutable_recovery.build_recovery_message(
            "shm-spool-1", "/opt/shm/pay_systems/srv_customlab_nalog.cgi"
        )
        self.assertIn("remains PAUSED intentionally", message)
        self.assertIn("chattr +i /opt/shm/pay_systems/srv_customlab_nalog.cgi", message)
        self.assertIn("docker unpause shm-spool-1", message)
        self.assertNotIn("refresh_token", message)


class TransactionRoleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.service = (SERVICE / "tasks/main.yml").read_text()
        cls.service_rollback = (
            ROOT / "ansible/roles/vff_fiscal_service_rollback/tasks/main.yml"
        ).read_text()
        cls.adapter = (ADAPTER / "tasks/main.yml").read_text()
        cls.adapter_restore = (ADAPTER / "tasks/restore_adapter.yml").read_text()
        cls.adapter_rollback = (
            ROOT / "ansible/roles/vff_fiscal_adapter_rollback/tasks/main.yml"
        ).read_text()

    def test_service_transaction_flags_and_rescue_guards(self) -> None:
        for fact in (
            "service_cutover_started",
            "service_backup_completed",
            "service_compose_replaced",
            "service_new_container_started",
        ):
            self.assertIn(fact, self.service)
        self.assertIn("not service_compose_replaced", self.service)
        self.assertIn("service_new_container_started", self.service)

    def test_service_state_gate_precedes_compose_replacement(self) -> None:
        self.assertLess(
            self.service.index("assert_no_creating_receipts.yml"),
            self.service.index("Atomically replace compose file"),
        )

    def test_service_rollback_candidate_precedes_gate_and_replace(self) -> None:
        self.assertLess(
            self.service_rollback.index("Validate rollback Compose candidate"),
            self.service_rollback.index("spool_cutover_gate.yml"),
        )
        self.assertLess(
            self.service_rollback.index("spool_cutover_gate.yml"),
            self.service_rollback.index("Atomically replace production Compose"),
        )
        auth_task = self.service_rollback[
            self.service_rollback.index("Authenticated user smoke test") :
        ]
        self.assertNotIn("failed_when: false", auth_task.split("rescue:", 1)[0])

    def test_adapter_flags_cover_first_helper_failure(self) -> None:
        self.assertLess(
            self.adapter.index("adapter_files_modification_started: true"),
            self.adapter.index("Install helper modules atomically"),
        )
        for fact in (
            "adapter_helpers_replaced",
            "adapter_cgi_replaced",
            "adapter_immutable_removed",
            "adapter_post_unpause_validation_started",
        ):
            self.assertIn(fact, self.adapter)

    def test_restoration_validates_all_three_checksums(self) -> None:
        for name in (
            "srv_customlab_nalog.cgi",
            "lib/VFFFiscal/AdapterConfig.pm",
            "lib/VFFFiscal/PaymentTimestamp.pm",
        ):
            self.assertIn(name, self.adapter_restore)

    def test_post_unpause_failure_restores_previous_files(self) -> None:
        post = self.adapter.index("Adapter post-unpause validation transaction")
        self.assertIn("restore_adapter.yml", self.adapter[post:])
        self.assertIn("spool_cutover_gate.yml", self.adapter_restore)

    def test_manual_rollback_has_safety_backup_and_safe_default(self) -> None:
        self.assertIn("pre-rollback adapter safety backup", self.adapter_rollback)
        self.assertIn("default(false)", self.adapter_rollback)
        self.assertGreaterEqual(self.adapter_rollback.count("restore_adapter.yml"), 2)

    def test_no_spool_exec_in_paused_cutover_section(self) -> None:
        cutover = self.adapter[
            self.adapter.index("Adapter cutover block") :
            self.adapter.index("Adapter post-unpause validation transaction")
        ]
        self.assertNotIn("docker exec {{ shm_spool_container }}", cutover)

    def test_metadata_distinguishes_previous_and_target(self) -> None:
        self.assertIn("target_commit", self.service)
        self.assertIn("previous_commit", self.service)
        self.assertIn("target_commit", self.adapter)
        self.assertIn("previous_cgi_sha256", self.adapter)
        self.assertIn("unknown-pre-manifest", self.service)

    def test_check_mode_guards_mutating_role_work(self) -> None:
        self.assertIn("when: not ansible_check_mode | bool", self.service)
        self.assertIn("when: not ansible_check_mode | bool", self.adapter)

    def test_host_key_checking_is_enabled(self) -> None:
        config = (ROOT / "ansible/ansible.cfg").read_text()
        self.assertIn("host_key_checking = True", config)
        self.assertNotIn("host_key_checking = False", config)

    def test_unpause_is_owned_and_verified(self) -> None:
        unpause = (COMMON / "tasks/unpause_spool.yml").read_text()
        self.assertIn("vff_fiscal_spool_paused_by_operation", unpause)
        self.assertIn("vff_fiscal_spool_unpause_verify.stdout != 'false'", unpause)
        self.assertNotIn("failed_when: false", unpause)

    def test_immutable_finalization_is_fail_closed(self) -> None:
        finalizer = (COMMON / "tasks/finalize_adapter_immutable.yml").read_text()
        self.assertIn("vff_fiscal_spool_leave_paused", finalizer)
        self.assertIn("Fail closed when immutable protection cannot be verified", finalizer)
        self.assertIn("parse_lsattr.py", finalizer)


if __name__ == "__main__":
    unittest.main()
