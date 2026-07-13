#!/usr/bin/env python3
"""Production-safety tests for deployment helpers and transaction roles."""

from __future__ import annotations

import importlib.util
import io
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

import yaml

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
deploy_lock = load_module("deploy_lock", COMMON / "files/deploy_lock.py")
compose_service_image = load_module(
    "compose_service_image", COMMON / "files/compose_service_image.py"
)
shm_exec_preflight = load_module(
    "shm_exec_preflight", COMMON / "files/shm_exec_preflight.py"
)
service_bootstrap_gate = load_module(
    "service_bootstrap_gate", COMMON / "files/service_bootstrap_gate.py"
)
service_manifest_reconcile = load_module(
    "service_manifest_reconcile", COMMON / "files/service_manifest_reconcile.py"
)
file_mode_gate = load_module(
    "file_mode_gate", COMMON / "files/file_mode_gate.py"
)


def docker_compose_available() -> bool:
    if shutil.which("docker") is None:
        return False
    probe = subprocess.run(
        ["docker", "compose", "version"],
        capture_output=True,
        text=True,
        check=False,
    )
    return probe.returncode == 0


def backup_compose_validation_violations() -> list[str]:
    violations: list[str] = []
    backup_markers = (
        "service_backup_dir",
        "service_rollback_backup_dir",
        "/backups/releases/",
    )
    for path in sorted((ROOT / "ansible").rglob("*.yml")):
        try:
            parsed = yaml.safe_load(path.read_text())
        except yaml.YAMLError:
            continue
        if parsed is None:
            continue
        for task in iter_ansible_task_dicts(parsed):
            argv = None
            shell = task.get("ansible.builtin.shell")
            command = task.get("ansible.builtin.command")
            if isinstance(command, dict) and "argv" in command:
                argv = command["argv"]
            elif isinstance(command, list):
                argv = command
            elif isinstance(shell, str):
                argv = shell.split()
            if not argv:
                continue
            argv_text = " ".join(str(part) for part in argv)
            if "docker" not in argv_text or "compose" not in argv_text:
                continue
            if not any(marker in argv_text for marker in backup_markers):
                continue
            if "vff_fiscal_root" in argv_text and "project-directory" in argv_text:
                continue
            if "{{ vff_fiscal_compose_path }}" in argv_text and "service_backup_dir" not in argv_text:
                continue
            violations.append(f"{path.relative_to(ROOT)}:{task.get('name', '<unnamed>')}")
    return violations


def docker_health_is_ready(rc: int, stdout: str) -> bool:
    return rc == 0 and stdout == "true healthy"


def simulate_docker_health_until(
    readings: list[tuple[int, str]],
    *,
    retries: int,
) -> tuple[bool, int]:
    for attempt in range(retries + 1):
        rc, stdout = readings[min(attempt, len(readings) - 1)]
        if docker_health_is_ready(rc, stdout):
            return True, attempt + 1
    return False, retries + 1


def unsafe_docker_health_assertions() -> list[str]:
    violations: list[str] = []
    for path in sorted((ROOT / "ansible").rglob("*.yml")):
        try:
            parsed = yaml.safe_load(path.read_text())
        except yaml.YAMLError:
            continue
        if parsed is None:
            continue
        for task in iter_ansible_task_dicts(parsed):
            command = task.get("ansible.builtin.command")
            shell = task.get("ansible.builtin.shell")
            body = ""
            if isinstance(command, str):
                body = command
            elif isinstance(command, dict):
                body = " ".join(str(part) for part in command.get("argv", []))
            elif isinstance(shell, str):
                body = shell
            if "Health.Status" not in body and "true healthy" not in str(task.get("failed_when", "")):
                continue
            if task.get("until"):
                continue
            if task.get("failed_when") is False:
                continue
            failed_when = str(task.get("failed_when", ""))
            if "healthy" in failed_when or "Health.Status" in body:
                violations.append(f"{path.relative_to(ROOT)}:{task.get('name', '<unnamed>')}")
    return violations


def iter_ansible_task_dicts(node):
    if isinstance(node, list):
        for item in node:
            yield from iter_ansible_task_dicts(item)
    elif isinstance(node, dict):
        for key in ("tasks", "block", "always", "rescue", "pre_tasks", "post_tasks"):
            if key in node:
                yield from iter_ansible_task_dicts(node[key])
        if any(
            isinstance(key, str) and key.startswith(("ansible.", "community."))
            for key in node
        ):
            yield node


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


class ShmExecPreflightTests(unittest.TestCase):
    STDIN_PROBE = 'print "stdin_exec_ok=1\\n";'

    def run_main(
        self, side_effects: list[subprocess.CompletedProcess[str]]
    ) -> tuple[int, mock.Mock, str]:
        with mock.patch.object(shm_exec_preflight, "run", side_effect=side_effects) as run_mock:
            with mock.patch.dict(
                os.environ,
                {"SHM_CONTAINER": "shm-core-1"},
                clear=False,
            ):
                with mock.patch.object(
                    shm_exec_preflight.sys, "stderr", new_callable=io.StringIO
                ) as stderr_handle:
                    rc = shm_exec_preflight.main()
                    return rc, run_mock, stderr_handle.getvalue()

    def test_successful_preflight_runs_checks_in_order(self) -> None:
        side_effects = [
            subprocess.CompletedProcess([], 1),
            subprocess.CompletedProcess([], 0),
            subprocess.CompletedProcess([], 0, "stdin_exec_ok=1\n"),
        ]
        rc, run_mock, _stderr = self.run_main(side_effects)

        self.assertEqual(rc, 0)
        self.assertEqual(run_mock.call_count, 3)

        deploy_tools_call = run_mock.call_args_list[0]
        self.assertEqual(
            deploy_tools_call.args[0],
            ["docker", "exec", "shm-core-1", "test", "-d", "/opt/vff-fiscal/deploy-tools"],
        )
        self.assertNotIn("input_text", deploy_tools_call.kwargs)

        pay_systems_call = run_mock.call_args_list[1]
        self.assertEqual(
            pay_systems_call.args[0],
            ["docker", "exec", "shm-core-1", "test", "-d", "/app/data/pay_systems"],
        )
        self.assertNotIn("input_text", pay_systems_call.kwargs)

        stdin_probe_call = run_mock.call_args_list[2]
        self.assertEqual(
            stdin_probe_call.args[0],
            ["docker", "exec", "-i", "shm-core-1", "sh", "-ec", "cd /app && exec perl -"],
        )
        self.assertEqual(stdin_probe_call.kwargs.get("input_text"), self.STDIN_PROBE)

    def test_run_forwards_input_text_to_subprocess(self) -> None:
        with mock.patch.object(shm_exec_preflight.subprocess, "run") as subprocess_run:
            subprocess_run.return_value = subprocess.CompletedProcess([], 0)
            shm_exec_preflight.run(["docker", "exec"], input_text=self.STDIN_PROBE)
            subprocess_run.assert_called_once_with(
                ["docker", "exec"],
                input=self.STDIN_PROBE,
                capture_output=True,
                text=True,
                check=False,
            )

    def test_legacy_input_keyword_is_rejected(self) -> None:
        with self.assertRaises(TypeError):
            shm_exec_preflight.run(["docker", "exec"], input=self.STDIN_PROBE)

    def test_deploy_tools_mount_is_rejected(self) -> None:
        rc, run_mock, stderr = self.run_main([subprocess.CompletedProcess([], 0)])
        self.assertEqual(rc, 1)
        self.assertEqual(run_mock.call_count, 1)
        self.assertIn("deploy_tools_mounted_in_container=1", stderr)

    def test_pay_systems_directory_missing_is_rejected(self) -> None:
        rc, run_mock, stderr = self.run_main(
            [
                subprocess.CompletedProcess([], 1),
                subprocess.CompletedProcess([], 1),
            ]
        )
        self.assertEqual(rc, 1)
        self.assertEqual(run_mock.call_count, 2)
        self.assertIn("pay_systems_container_dir_missing=1", stderr)

    def test_stdin_probe_failure_returns_rc_1(self) -> None:
        base = [
            subprocess.CompletedProcess([], 1),
            subprocess.CompletedProcess([], 0),
        ]

        rc, run_mock, stderr = self.run_main(
            base + [subprocess.CompletedProcess([], 1, "")]
        )
        self.assertEqual(rc, 1)
        self.assertEqual(run_mock.call_count, 3)
        self.assertIn("stdin_exec_probe_failed=1", stderr)

        rc, run_mock, stderr = self.run_main(
            base + [subprocess.CompletedProcess([], 0, "unexpected output\n")]
        )
        self.assertEqual(rc, 1)
        self.assertEqual(run_mock.call_count, 3)
        self.assertIn("stdin_exec_probe_failed=1", stderr)


class DeployManifestTransactionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.manifest_tasks_path = COMMON / "tasks/write_deploy_state.yml"
        cls.manifest_tasks = yaml.safe_load(cls.manifest_tasks_path.read_text())

    def test_manifest_transaction_structure(self) -> None:
        self.assertEqual(len(self.manifest_tasks), 1)
        transaction = self.manifest_tasks[0]
        self.assertEqual(transaction["name"], "Write deploy manifest transaction")
        self.assertNotIn("rescue", transaction)
        self.assertNotIn("ignore_errors", transaction)

        block = transaction["block"]
        always = transaction["always"]
        self.assertEqual(len(block), 2)
        self.assertEqual(len(always), 1)

        render, write = block
        cleanup = always[0]

        self.assertEqual(render["name"], "Render deploy manifest payload")
        self.assertEqual(write["name"], "Atomically write deploy manifest")
        self.assertEqual(cleanup["name"], "Remove deploy manifest payload file")

        for task in (render, write, cleanup):
            self.assertTrue(task.get("no_log"), msg=task["name"])

        template = render["ansible.builtin.template"]
        self.assertEqual(template["owner"], "root")
        self.assertEqual(template["group"], "root")
        self.assertEqual(template["mode"], "0600")
        self.assertTrue(str(template["dest"]).endswith(".payload"))

        shell = write["ansible.builtin.shell"]
        self.assertIn("set -euo pipefail", shell)
        self.assertIn("write_manifest.py", shell)
        self.assertEqual(write["args"]["executable"], "/bin/bash")
        self.assertEqual(
            write["environment"]["MANIFEST_PATH"],
            "{{ vff_fiscal_deploy_state_path }}",
        )

        cleanup_file = cleanup["ansible.builtin.file"]
        self.assertEqual(cleanup_file["path"], "{{ vff_fiscal_deploy_state_path }}.payload")
        self.assertEqual(cleanup_file["state"], "absent")

        render_index = self.manifest_tasks_path.read_text().index("Render deploy manifest payload")
        write_index = self.manifest_tasks_path.read_text().index("Atomically write deploy manifest")
        cleanup_index = self.manifest_tasks_path.read_text().index("Remove deploy manifest payload file")
        self.assertLess(render_index, write_index)
        self.assertLess(write_index, cleanup_index)

    def test_pipefail_shell_tasks_use_bash(self) -> None:
        violations: list[str] = []
        for path in sorted((ROOT / "ansible").rglob("*.yml")):
            try:
                parsed = yaml.safe_load(path.read_text())
            except yaml.YAMLError:
                continue
            if parsed is None:
                continue
            for task in iter_ansible_task_dicts(parsed):
                shell = task.get("ansible.builtin.shell")
                if not shell:
                    continue
                body = shell if isinstance(shell, str) else ""
                if "set -euo pipefail" not in body:
                    continue
                args = task.get("args", {})
                if args.get("executable") != "/bin/bash":
                    violations.append(f"{path.relative_to(ROOT)}:{task.get('name', '<unnamed>')}")
        self.assertEqual(violations, [])

    def test_manifest_write_failure_remains_fatal_after_payload_cleanup(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            payload = tmp_path / "deploy-state.json.payload"
            payload.write_text('{"status":"ok"}\n', encoding="utf-8")
            marker = tmp_path / "cleaned"
            playbook = tmp_path / "play.yml"
            playbook.write_text(
                textwrap.dedent(
                    f"""
                    - hosts: localhost
                      gather_facts: false
                      tasks:
                        - name: Write deploy manifest transaction
                          block:
                            - ansible.builtin.copy:
                                dest: {payload}
                                content: '{{"status":"ok"}}'
                            - ansible.builtin.fail:
                                msg: manifest write failed
                          always:
                            - ansible.builtin.file:
                                path: {payload}
                                state: absent
                            - ansible.builtin.copy:
                                dest: {marker}
                                content: cleaned
                    """
                ),
                encoding="utf-8",
            )
            result = subprocess.run(
                [
                    "ansible-playbook",
                    "-i",
                    "localhost,",
                    "-c",
                    "local",
                    str(playbook),
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertFalse(payload.exists())
            self.assertTrue(marker.exists())
            self.assertIn("manifest write failed", result.stdout)


class ServiceBootstrapGateTests(unittest.TestCase):
    def test_no_manifest_allows_bootstrap(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            present, registered = service_bootstrap_gate.evaluate_manifest(manifest)
            self.assertFalse(present)
            self.assertFalse(registered)

    def test_adapter_only_manifest_allows_bootstrap(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            manifest.write_text(
                json.dumps(
                    {
                        "adapter_commit": "a" * 40,
                        "adapter_sha256": "b" * 64,
                        "adapter_enabled": True,
                        "service_commit": "",
                        "service_image": "",
                        "service_image_id": "",
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            present, registered = service_bootstrap_gate.evaluate_manifest(manifest)
            self.assertTrue(present)
            self.assertFalse(registered)

    def test_service_commit_blocks_bootstrap(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            manifest.write_text(
                json.dumps({"service_commit": "c" * 40}) + "\n",
                encoding="utf-8",
            )
            _, registered = service_bootstrap_gate.evaluate_manifest(manifest)
            self.assertTrue(registered)

    def test_service_image_blocks_bootstrap(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            manifest.write_text(
                json.dumps({"service_image": "vff-fiscal:prod"}) + "\n",
                encoding="utf-8",
            )
            _, registered = service_bootstrap_gate.evaluate_manifest(manifest)
            self.assertTrue(registered)

    def test_service_image_id_blocks_bootstrap(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            manifest.write_text(
                json.dumps({"service_image_id": "sha256:deadbeef"}) + "\n",
                encoding="utf-8",
            )
            _, registered = service_bootstrap_gate.evaluate_manifest(manifest)
            self.assertTrue(registered)

    def test_malformed_manifest_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            manifest.write_text("{not-json", encoding="utf-8")
            with self.assertRaises(ValueError):
                service_bootstrap_gate.evaluate_manifest(manifest)

            with mock.patch.dict(os.environ, {"MANIFEST_PATH": str(manifest)}):
                self.assertEqual(service_bootstrap_gate.main(), 1)

    def test_main_reports_gate_facts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "deploy-state.json"
            with mock.patch.dict(os.environ, {"MANIFEST_PATH": str(manifest)}):
                with mock.patch("sys.stdout", new_callable=io.StringIO) as stdout:
                    self.assertEqual(service_bootstrap_gate.main(), 0)
                    output = stdout.getvalue()
            self.assertIn("manifest_present=0", output)
            self.assertIn("service_identity_registered=0", output)


class FileModeGateTests(unittest.TestCase):
    def test_env_mode_0600_passes(self) -> None:
        self.assertTrue(file_mode_gate.env_mode_allowed("0600"))

    def test_env_mode_0400_passes(self) -> None:
        self.assertTrue(file_mode_gate.env_mode_allowed("0400"))

    def test_env_mode_0644_fails(self) -> None:
        self.assertFalse(file_mode_gate.env_mode_allowed("0644"))

    def test_env_mode_0660_fails(self) -> None:
        self.assertFalse(file_mode_gate.env_mode_allowed("0660"))

    def test_env_mode_0604_fails(self) -> None:
        self.assertFalse(file_mode_gate.env_mode_allowed("0604"))

    def test_env_mode_0700_fails(self) -> None:
        self.assertFalse(file_mode_gate.env_mode_allowed("0700"))

    def test_env_mode_missing_or_malformed_fails(self) -> None:
        for mode in (None, "", "600", "00600", "06000", "abcd"):
            self.assertFalse(file_mode_gate.env_mode_allowed(mode))

    def test_state_mode_0600_passes(self) -> None:
        self.assertTrue(file_mode_gate.state_mode_allowed("0600"))

    def test_state_mode_0644_fails(self) -> None:
        self.assertFalse(file_mode_gate.state_mode_allowed("0644"))

    def test_state_mode_0400_fails(self) -> None:
        self.assertFalse(file_mode_gate.state_mode_allowed("0400"))

    def test_state_mode_0660_fails(self) -> None:
        self.assertFalse(file_mode_gate.state_mode_allowed("0660"))

    def test_decimal_int_filter_misinterprets_octal_mode(self) -> None:
        self.assertEqual(int("0600"), 600)
        self.assertNotEqual(int("0600"), 0o600)
        self.assertTrue(file_mode_gate.env_mode_allowed("0600"))

    def test_no_unsafe_stat_mode_int_checks_in_ansible(self) -> None:
        violations: list[str] = []
        decimal_mode_literals = ("384", "420", "448")
        for path in sorted((ROOT / "ansible").rglob("*.yml")):
            content = path.read_text()
            relative = str(path.relative_to(ROOT))
            if "stat.mode | int" in content:
                violations.append(f"{relative}: stat.mode | int")
            for literal in decimal_mode_literals:
                if f"stat.mode" in content and literal in content:
                    violations.append(f"{relative}: stat.mode decimal {literal}")
        self.assertEqual(violations, [])


class ComposeBackupProjectDirectoryTests(unittest.TestCase):
    LEGACY_COMPOSE_FIXTURE = textwrap.dedent(
        """
        services:
          vff-fiscal:
            image: vff-fiscal:test
            env_file:
              - .env
            volumes:
              - ./data:/var/lib/vff-fiscal
        """
    ).strip()

    @classmethod
    def setUpClass(cls) -> None:
        cls.service = (SERVICE / "tasks/main.yml").read_text()
        cls.service_rollback = (
            ROOT / "ansible/roles/vff_fiscal_service_rollback/tasks/main.yml"
        ).read_text()

    def test_legacy_backup_compose_validation_uses_project_directory(self) -> None:
        validation = self._legacy_validation_argv()
        self.assertIn("--project-directory", validation)
        self.assertIn("{{ vff_fiscal_root }}", validation)
        project_dir_index = validation.index("--project-directory")
        compose_file_index = validation.index("-f")
        backup_file_index = validation.index("{{ service_backup_dir }}/docker-compose.yml")
        self.assertLess(project_dir_index, compose_file_index)
        self.assertEqual(validation[compose_file_index + 1], "{{ service_backup_dir }}/docker-compose.yml")
        self.assertEqual(backup_file_index, compose_file_index + 1)

    def test_legacy_backup_validation_precedes_compose_replacement(self) -> None:
        validate_idx = self.service.index("Validate normalized legacy rollback Compose")
        replace_idx = self.service.index("Atomically replace compose file")
        self.assertLess(validate_idx, replace_idx)

    def test_no_env_file_is_copied_into_service_backup(self) -> None:
        parsed = yaml.safe_load(self.service)
        for task in iter_ansible_task_dicts(parsed):
            copy = task.get("ansible.builtin.copy")
            if not copy:
                continue
            dest = str(copy.get("dest", ""))
            src = str(copy.get("src", ""))
            if "service_backup_dir" not in dest:
                continue
            self.assertNotIn(".env", dest, msg=task.get("name"))
            self.assertNotIn("vff_fiscal_env_file", dest, msg=task.get("name"))
            self.assertNotIn("vff_fiscal_env_file", src, msg=task.get("name"))

    def test_deployment_rescue_restores_compose_before_execution(self) -> None:
        rescue = self.service.split("  rescue:", 1)[1]
        restore_idx = rescue.index("Restore previous compose file after failure")
        start_idx = rescue.index("Start previous service image after failure")
        self.assertLess(restore_idx, start_idx)
        self.assertIn('dest: "{{ vff_fiscal_compose_path }}"', rescue)
        self.assertIn("docker compose -f {{ vff_fiscal_compose_path }}", rescue)

    def test_explicit_rollback_stages_candidate_under_project_root(self) -> None:
        parsed = yaml.safe_load(self.service_rollback)
        staged = False
        for task in iter_ansible_task_dicts(parsed):
            copy = task.get("ansible.builtin.copy")
            if not copy:
                continue
            dest = str(copy.get("dest", ""))
            if dest == '{{ vff_fiscal_compose_path }}.rollback-candidate':
                staged = True
        self.assertTrue(staged)
        candidate_validation = self._rollback_candidate_validation_argv()
        self.assertIn("{{ vff_fiscal_compose_path }}.rollback-candidate", candidate_validation)
        self.assertNotIn("service_rollback_backup_dir", " ".join(candidate_validation))
        replace_idx = self.service_rollback.index(
            "Atomically replace production Compose with rollback candidate"
        )
        validate_idx = self.service_rollback.index("Validate rollback Compose candidate")
        self.assertLess(validate_idx, replace_idx)

    def test_no_backup_compose_validation_without_project_directory(self) -> None:
        self.assertEqual(backup_compose_validation_violations(), [])

    @unittest.skipUnless(docker_compose_available(), "docker compose is unavailable")
    def test_backup_compose_validation_requires_original_project_directory(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            project_root = Path(tmp) / "opt" / "vff-fiscal"
            backup_dir = project_root / "backups" / "releases" / "test" / "service"
            backup_dir.mkdir(parents=True)
            (project_root / ".env").write_text("VFF_FISCAL_API_KEY=secret\n", encoding="utf-8")
            (project_root / "data").mkdir()
            compose_path = backup_dir / "docker-compose.yml"
            compose_path.write_text(self.LEGACY_COMPOSE_FIXTURE + "\n", encoding="utf-8")

            without_project = subprocess.run(
                ["docker", "compose", "-f", str(compose_path), "config", "-q"],
                capture_output=True,
                text=True,
                check=False,
            )
            with_project = subprocess.run(
                [
                    "docker",
                    "compose",
                    "--project-directory",
                    str(project_root),
                    "-f",
                    str(compose_path),
                    "config",
                    "-q",
                ],
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertNotEqual(without_project.returncode, 0, without_project.stderr)
            self.assertIn(".env", without_project.stderr)
            self.assertEqual(with_project.returncode, 0, with_project.stderr)

    def _legacy_validation_argv(self) -> list[str]:
        parsed = yaml.safe_load(self.service)
        for task in iter_ansible_task_dicts(parsed):
            if task.get("name") == "Validate normalized legacy rollback Compose":
                command = task["ansible.builtin.command"]
                return command["argv"]
        raise AssertionError("legacy validation task not found")

    def _rollback_candidate_validation_argv(self) -> list[str]:
        parsed = yaml.safe_load(self.service_rollback)
        for task in iter_ansible_task_dicts(parsed):
            if task.get("name") == "Validate rollback Compose candidate":
                command = task["ansible.builtin.command"]
                return command["argv"]
        raise AssertionError("rollback candidate validation task not found")


PRODUCTION_TARGET_COMMIT = "204d0f4c9c4460fa4d6cbde59561ff25b1842345"
PRODUCTION_TARGET_IMAGE = "vff-fiscal:204d0f4c9c44"
PRODUCTION_TARGET_IMAGE_ID = (
    "sha256:3f527a513aa1462ad2edc0348dc026e09fe43eb9f0395d658a00f71bdd2d512f"
)
PRODUCTION_PREVIOUS_IMAGE = "vff-fiscal:7a0fab5b546b"
PRODUCTION_PREVIOUS_IMAGE_ID = (
    "sha256:ff4745b9e41972a78cb0708ff9d36801fd4de8f7846ed68c255c6395a003d451"
)
PRODUCTION_DEPLOYED_AT = "2026-07-09T22:09:51Z"


def write_service_manifest_fixture(
    tmp_path: Path,
    *,
    manifest: dict[str, object],
    backup_meta: dict[str, object],
    backup_root: Path | None = None,
) -> tuple[Path, Path, Path]:
    root = backup_root or (tmp_path / "opt" / "vff-fiscal" / "backups" / "releases")
    backup_dir = root / "20260710T000951-204d0f4c9c44" / "service"
    backup_dir.mkdir(parents=True, exist_ok=True)
    manifest_path = tmp_path / "deploy-state.json"
    backup_meta = {
        **backup_meta,
        "backup_directory": str(backup_dir),
    }
    manifest = {
        **manifest,
        "backup_directory": str(backup_dir),
    }
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    (backup_dir / "backup-meta.json").write_text(
        json.dumps(backup_meta, indent=2) + "\n",
        encoding="utf-8",
    )
    return manifest_path, backup_dir, root


class ServiceManifestReconcileTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.service = (SERVICE / "tasks/main.yml").read_text()

    def reconcile(
        self,
        manifest_path: Path,
        backup_root: Path,
        *,
        manifest: dict[str, object] | None = None,
        backup_meta: dict[str, object] | None = None,
        target_commit: str = PRODUCTION_TARGET_COMMIT,
        target_image: str = PRODUCTION_TARGET_IMAGE,
        target_image_id: str = PRODUCTION_TARGET_IMAGE_ID,
        running_image_tag: str = PRODUCTION_TARGET_IMAGE,
        running_image_id: str = PRODUCTION_TARGET_IMAGE_ID,
        previous_image_id: str = PRODUCTION_PREVIOUS_IMAGE_ID,
        payload_path: Path | None = None,
    ) -> bool:
        return service_manifest_reconcile.prepare_reconciliation(
            manifest_path=manifest_path,
            backup_root=str(backup_root),
            image_repository="vff-fiscal",
            target_commit=target_commit,
            target_image=target_image,
            target_image_id=target_image_id,
            running_image_tag=running_image_tag,
            running_image_id=running_image_id,
            resolved_previous_image_id=previous_image_id,
            image_id_resolver=lambda _image: previous_image_id,
            canonical_manifest_path=payload_path,
        )

    def canonical_backup_meta(self) -> dict[str, object]:
        return {
            "component": "service",
            "target_commit": PRODUCTION_TARGET_COMMIT,
            "target_image": PRODUCTION_TARGET_IMAGE,
            "previous_image": PRODUCTION_PREVIOUS_IMAGE,
            "previous_image_id": PRODUCTION_PREVIOUS_IMAGE_ID,
            "deployed_at": PRODUCTION_DEPLOYED_AT,
        }

    def canonical_manifest(self) -> dict[str, object]:
        return {
            "service_commit": PRODUCTION_TARGET_COMMIT,
            "service_image": PRODUCTION_TARGET_IMAGE,
            "service_image_id": PRODUCTION_TARGET_IMAGE_ID,
            "deployed_at": PRODUCTION_DEPLOYED_AT,
            "previous_service_image": PRODUCTION_PREVIOUS_IMAGE,
            "previous_service_image_id": PRODUCTION_PREVIOUS_IMAGE_ID,
            "adapter_commit": "abc123" * 6 + "abcd",
            "adapter_sha256": "def456" * 10 + "abcd",
            "adapter_enabled": True,
            "need_update_to_present": False,
            "deployment_status": "success",
        }

    def test_correct_manifest_is_not_rewritten(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest = self.canonical_manifest()
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=manifest,
                backup_meta=self.canonical_backup_meta(),
            )
            required = self.reconcile(manifest_path, backup_root)
            self.assertFalse(required)

    def test_production_corruption_is_repaired_from_backup_meta(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            corrupted = self.canonical_manifest()
            corrupted["previous_service_image"] = PRODUCTION_TARGET_IMAGE
            corrupted["previous_service_image_id"] = PRODUCTION_TARGET_IMAGE_ID
            corrupted["deployed_at"] = "2026-07-09T22:12:00Z"
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=corrupted,
                backup_meta=self.canonical_backup_meta(),
            )
            payload = tmp_path / "reconcile.payload"
            required = self.reconcile(manifest_path, backup_root, payload_path=payload)
            self.assertTrue(required)
            repaired = json.loads(payload.read_text(encoding="utf-8"))
            self.assertEqual(repaired["previous_service_image"], PRODUCTION_PREVIOUS_IMAGE)
            self.assertEqual(repaired["previous_service_image_id"], PRODUCTION_PREVIOUS_IMAGE_ID)
            self.assertEqual(repaired["deployed_at"], PRODUCTION_DEPLOYED_AT)
            self.assertEqual(repaired["adapter_commit"], corrupted["adapter_commit"])

    def test_second_run_after_repair_performs_no_write(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=self.canonical_manifest(),
                backup_meta=self.canonical_backup_meta(),
            )
            payload = tmp_path / "reconcile.payload"
            corrupted = json.loads(manifest_path.read_text(encoding="utf-8"))
            corrupted["deployed_at"] = "2026-07-09T22:12:00Z"
            manifest_path.write_text(json.dumps(corrupted, indent=2) + "\n", encoding="utf-8")
            self.assertTrue(self.reconcile(manifest_path, backup_root, payload_path=payload))
            manifest_path.write_text(payload.read_text(encoding="utf-8"), encoding="utf-8")
            self.assertFalse(self.reconcile(manifest_path, backup_root, payload_path=payload))

    def test_mismatched_target_commit_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=self.canonical_manifest(),
                backup_meta=self.canonical_backup_meta(),
            )
            with self.assertRaises(ValueError):
                self.reconcile(
                    manifest_path,
                    backup_root,
                    target_commit="a" * 40,
                )

    def test_mismatched_target_image_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=self.canonical_manifest(),
                backup_meta=self.canonical_backup_meta(),
            )
            with self.assertRaises(ValueError):
                self.reconcile(
                    manifest_path,
                    backup_root,
                    running_image_tag="vff-fiscal:deadbeef0000",
                )

    def test_mismatched_previous_image_id_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=self.canonical_manifest(),
                backup_meta=self.canonical_backup_meta(),
            )
            with self.assertRaises(ValueError):
                self.reconcile(
                    manifest_path,
                    backup_root,
                    previous_image_id="sha256:" + "0" * 64,
                )

    def test_missing_backup_meta_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest_path, backup_dir, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=self.canonical_manifest(),
                backup_meta=self.canonical_backup_meta(),
            )
            (backup_dir / "backup-meta.json").unlink()
            with self.assertRaises(ValueError):
                self.reconcile(manifest_path, backup_root)

    def test_backup_directory_outside_root_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            outside = tmp_path / "outside" / "service"
            outside.mkdir(parents=True)
            manifest_path = tmp_path / "deploy-state.json"
            manifest = self.canonical_manifest()
            manifest["backup_directory"] = str(outside)
            manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
            (outside / "backup-meta.json").write_text(
                json.dumps(self.canonical_backup_meta(), indent=2) + "\n",
                encoding="utf-8",
            )
            backup_root = tmp_path / "opt" / "vff-fiscal" / "backups" / "releases"
            backup_root.mkdir(parents=True)
            with self.assertRaises(ValueError):
                self.reconcile(manifest_path, backup_root)

    def test_adapter_fields_are_preserved_exactly(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            manifest = self.canonical_manifest()
            manifest["deployed_at"] = "2026-07-09T22:12:00Z"
            manifest_path, _, backup_root = write_service_manifest_fixture(
                tmp_path,
                manifest=manifest,
                backup_meta=self.canonical_backup_meta(),
            )
            payload = tmp_path / "reconcile.payload"
            self.reconcile(manifest_path, backup_root, payload_path=payload)
            repaired = json.loads(payload.read_text(encoding="utf-8"))
            self.assertEqual(repaired["adapter_commit"], manifest["adapter_commit"])
            self.assertEqual(repaired["adapter_sha256"], manifest["adapter_sha256"])
            self.assertEqual(repaired["adapter_enabled"], manifest["adapter_enabled"])
            self.assertEqual(repaired["need_update_to_present"], manifest["need_update_to_present"])

    def test_no_cutover_path_does_not_render_candidate_compose(self) -> None:
        render = self.service.index("- name: Render candidate compose file")
        render_section = self.service[render : render + 400]
        self.assertIn("service_cutover_required | bool", render_section)
        self.assertNotIn("not service_cutover_required", render_section)

    def test_real_cutover_renders_candidate_before_spool_pause(self) -> None:
        render_idx = self.service.index("Render candidate compose file")
        validate_idx = self.service.index("Validate candidate compose file")
        cutover_idx = self.service.index("- name: Service cutover block")
        spool_idx = self.service.index("spool_cutover_gate.yml")
        self.assertLess(render_idx, cutover_idx)
        self.assertLess(validate_idx, cutover_idx)
        self.assertLess(cutover_idx, spool_idx)

    def test_idempotent_path_uses_conditional_manifest_reconciliation(self) -> None:
        self.assertIn("service_manifest_reconcile.py", self.service)
        self.assertIn("service_manifest_reconciliation_required", self.service)
        self.assertIn("write_reconciled_deploy_state.yml", self.service)
        self.assertNotIn("Update deploy state after idempotent service verification", self.service)
        self.assertNotIn("- name: Prepare service deploy manifest", self.service)
        reconcile_idx = self.service.index("Prepare idempotent service manifest reconciliation")
        reconcile_section = self.service[reconcile_idx:]
        self.assertNotIn("ansible_date_time.iso8601", reconcile_section)
        self.assertNotIn("previous_service_image if service_previous_image", self.service)


class DockerHealthReadinessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.service = (SERVICE / "tasks/main.yml").read_text()
        cls.group_vars = yaml.safe_load((ROOT / "ansible/group_vars/all.yml").read_text())
        cls.compose_template = (
            SERVICE / "templates/docker-compose.yml.j2"
        ).read_text()
        parsed = yaml.safe_load(cls.service)
        cls.health_task = None
        for task in iter_ansible_task_dicts(parsed):
            if task.get("name") == "Wait for container Docker health to become healthy":
                cls.health_task = task
                break
        if cls.health_task is None:
            raise AssertionError("docker health polling task not found")

    def test_true_starting_is_transient_and_retried(self) -> None:
        self.assertFalse(docker_health_is_ready(0, "true starting"))
        ready, attempts = simulate_docker_health_until(
            [(0, "true starting"), (0, "true starting"), (0, "true healthy")],
            retries=5,
        )
        self.assertTrue(ready)
        self.assertEqual(attempts, 3)

    def test_true_healthy_succeeds(self) -> None:
        ready, attempts = simulate_docker_health_until([(0, "true healthy")], retries=3)
        self.assertTrue(ready)
        self.assertEqual(attempts, 1)

    def test_persistent_starting_fails_after_bounded_retries(self) -> None:
        retries = int(self.group_vars["vff_fiscal_docker_health_poll_retries"])
        ready, attempts = simulate_docker_health_until(
            [(0, "true starting")],
            retries=retries,
        )
        self.assertFalse(ready)
        self.assertEqual(attempts, retries + 1)

    def test_unhealthy_or_missing_container_cannot_succeed(self) -> None:
        for rc, stdout in (
            (0, "true unhealthy"),
            (0, "false healthy"),
            (0, "true"),
            (1, "true healthy"),
        ):
            self.assertFalse(docker_health_is_ready(rc, stdout))
            ready, _ = simulate_docker_health_until([(rc, stdout)], retries=2)
            self.assertFalse(ready)

    def test_poll_timeout_covers_compose_start_period(self) -> None:
        start_period = int(self.group_vars["vff_fiscal_compose_health_start_period"])
        interval = int(self.group_vars["vff_fiscal_compose_health_interval"])
        retries = int(self.group_vars["vff_fiscal_compose_health_retries"])
        poll_retries = int(self.group_vars["vff_fiscal_docker_health_poll_retries"])
        poll_delay = int(self.group_vars["vff_fiscal_docker_health_poll_delay"])
        compose_budget = start_period + interval * retries
        poll_budget = poll_retries * poll_delay
        self.assertGreaterEqual(poll_budget, compose_budget)
        self.assertIn("start_period: 20s", self.compose_template)

    def test_health_task_uses_until_retries_and_delay(self) -> None:
        until = self.health_task["until"]
        self.assertIn("service_container_health.rc == 0", until)
        self.assertIn("service_container_health.stdout == 'true healthy'", until)
        self.assertNotIn("failed_when", self.health_task)
        self.assertEqual(
            str(self.health_task["retries"]),
            "{{ vff_fiscal_docker_health_poll_retries }}",
        )
        self.assertEqual(
            str(self.health_task["delay"]),
            "{{ vff_fiscal_docker_health_poll_delay }}",
        )

    def test_docker_health_polling_ordering(self) -> None:
        start_idx = self.service.index("Start updated vff-fiscal service")
        http_idx = self.service.index("Wait for health endpoint")
        docker_idx = self.service.index("Wait for container Docker health to become healthy")
        auth_idx = self.service.index("Authenticated user smoke test")
        manifest_idx = self.service.index("Write successful service deploy manifest")
        unpause_idx = self.service.index("Unpause SHM spool after service deployment")
        self.assertLess(start_idx, http_idx)
        self.assertLess(http_idx, docker_idx)
        self.assertLess(docker_idx, auth_idx)
        self.assertLess(auth_idx, manifest_idx)
        self.assertLess(manifest_idx, unpause_idx)

    def test_failure_still_enters_rescue_rollback_path(self) -> None:
        cutover = self.service.split("- name: Service cutover block", 1)[1]
        rescue = cutover.split("  rescue:", 1)[1].split("  always:", 1)[0]
        self.assertIn("Restore previous compose file after failure", rescue)
        self.assertIn("Start previous service image after failure", rescue)
        self.assertIn("Fail play after automatic service rollback", rescue)

    def test_no_unsafe_one_shot_docker_health_assertions(self) -> None:
        self.assertEqual(unsafe_docker_health_assertions(), [])


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

    def test_deploy_lock_contention_and_owned_release(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            lock_dir = Path(tmp) / "deploy.lock"
            self.assertEqual(
                deploy_lock.acquire(lock_dir, "deploy", "token-a", "controller-a"),
                0,
            )
            metadata = json.loads((lock_dir / "metadata.json").read_text())
            self.assertEqual(metadata["controller_host"], "controller-a")
            self.assertIn("managed_host", metadata)
            self.assertIn("helper_pid", metadata)
            self.assertNotIn("pid", metadata)
            self.assertEqual(
                deploy_lock.acquire(lock_dir, "deploy", "token-b", "controller-b"),
                1,
            )
            self.assertEqual(
                json.loads((lock_dir / "metadata.json").read_text())["token"],
                "token-a",
            )
            self.assertEqual(deploy_lock.release(lock_dir, "token-b"), 1)
            self.assertTrue(lock_dir.exists())
            self.assertEqual(deploy_lock.release(lock_dir, "token-a"), 0)
            self.assertFalse(lock_dir.exists())

    def test_compose_service_image_resolution(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            compose = Path(tmp) / "docker-compose.yml"
            compose.write_text(
                textwrap.dedent(
                    """
                    services:
                      sidecar:
                        image: redis:7
                      vff-fiscal:
                        image: vff-fiscal:abcdef123456
                        pull_policy: never
                    """
                ),
                encoding="utf-8",
            )
            self.assertEqual(
                compose_service_image.service_image(compose, "vff-fiscal"),
                "vff-fiscal:abcdef123456",
            )
            old = compose_service_image.replace_service_image(
                compose, "vff-fiscal", "vff-fiscal:111111111111"
            )
            self.assertEqual(old, "vff-fiscal:abcdef123456")
            contents = compose.read_text()
            self.assertIn("image: redis:7", contents)
            self.assertIn("image: vff-fiscal:111111111111", contents)

    def test_rescued_restoration_failure_remains_fatal_after_always(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            marker = tmp_path / "finalized"
            playbook = tmp_path / "play.yml"
            playbook.write_text(
                textwrap.dedent(
                    f"""
                    - hosts: localhost
                      gather_facts: false
                      tasks:
                        - name: Simulate restoration protected transaction
                          block:
                            - ansible.builtin.fail:
                                msg: chattr failed
                          rescue:
                            - ansible.builtin.set_fact:
                                adapter_restoration_failed: true
                                vff_fiscal_spool_leave_paused: false
                            - ansible.builtin.fail:
                                msg: restoration remains fatal
                          always:
                            - ansible.builtin.copy:
                                dest: {marker}
                                content: finalized
                    """
                ),
                encoding="utf-8",
            )
            result = subprocess.run(
                [
                    "ansible-playbook",
                    "-i",
                    "localhost,",
                    "-c",
                    "local",
                    str(playbook),
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue(marker.exists())
            self.assertIn("restoration remains fatal", result.stdout)


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

    def test_service_file_mode_validation_uses_octal_strings(self) -> None:
        self.assertIn("service_env_stat.stat.mode in ['0600', '0400']", self.service)
        self.assertIn("service_state_stat.stat.mode == '0600'", self.service)
        self.assertIn("service_state_stat.stat.uid == vff_fiscal_runtime_uid", self.service)
        self.assertIn("service_state_stat.stat.gid == vff_fiscal_runtime_gid", self.service)
        self.assertNotIn("stat.mode | int", self.service)

        group_vars = (ROOT / "ansible/group_vars/all.yml").read_text()
        self.assertIn("vff_fiscal_runtime_uid: 65532", group_vars)
        self.assertIn("vff_fiscal_runtime_gid: 65532", group_vars)

    def test_service_permission_checks_precede_mutating_steps(self) -> None:
        env_mode_idx = self.service.index("Assert service environment file permissions")
        state_mode_idx = self.service.index("Require persistent state file")
        bootstrap_idx = self.service.index(
            "Evaluate deploy manifest service identity for legacy bootstrap"
        )
        tag_idx = self.service.index("Create immutable local alias for legacy running image")
        build_idx = self.service.index("Build target service image")
        candidate_idx = self.service.index("Render candidate compose file")
        spool_idx = self.service.index("spool_cutover_gate.yml")
        compose_idx = self.service.index("Atomically replace compose file")
        restart_idx = self.service.index("up -d --no-deps --pull never {{ vff_fiscal_container_name }}")

        for checkpoint in (
            bootstrap_idx,
            tag_idx,
            build_idx,
            candidate_idx,
            spool_idx,
            compose_idx,
            restart_idx,
        ):
            self.assertLess(env_mode_idx, checkpoint)
            self.assertLess(state_mode_idx, checkpoint)

        self.assertLess(bootstrap_idx, tag_idx)
        self.assertLess(tag_idx, build_idx)
        self.assertLess(spool_idx, compose_idx)
        self.assertLess(compose_idx, restart_idx)

    def test_legacy_bootstrap_is_explicit_and_normalizes_compose(self) -> None:
        self.assertIn("vff_fiscal_allow_legacy_image_bootstrap", self.service)
        self.assertIn("vff_fiscal_legacy_image_commit is match('^[0-9a-f]{40}$')", self.service)
        self.assertIn("service_bootstrap_gate.py", self.service)
        self.assertIn("service_bootstrap_identity_registered", self.service)
        self.assertNotIn("service_bootstrap_deploy_state_stat.stat.exists", self.service)
        self.assertIn("docker", self.service)
        self.assertIn("tag", self.service)
        self.assertIn("Normalize legacy rollback Compose", self.service)
        self.assertIn("Resolve normalized legacy rollback Compose image", self.service)
        self.assertIn("legacy_bootstrap", self.service)

    def test_legacy_bootstrap_validation_precedes_mutating_steps(self) -> None:
        gate_idx = self.service.index(
            "Evaluate deploy manifest service identity for legacy bootstrap"
        )
        assert_idx = self.service.index("Validate explicit legacy image bootstrap request")
        tag_idx = self.service.index("Create immutable local alias for legacy running image")
        build_idx = self.service.index("Build target service image")
        self.assertLess(gate_idx, assert_idx)
        self.assertLess(assert_idx, tag_idx)
        self.assertLess(tag_idx, build_idx)

    def test_service_manifest_preserves_adapter_fields(self) -> None:
        manifest_section = self.service[
            self.service.index("Prepare successful service deploy manifest") :
            self.service.index("Write successful service deploy manifest")
        ]
        for field in (
            "adapter_commit",
            "adapter_sha256",
            "adapter_enabled",
            "need_update_to_present",
        ):
            self.assertIn(f"vff_fiscal_existing_deploy_state.{field}", manifest_section)

    def test_fresh_checkout_skips_fetch_before_clone(self) -> None:
        common = (COMMON / "tasks/main.yml").read_text()
        self.assertIn("Check whether application checkout is already a Git repository", common)
        self.assertIn("service_bootstrap_gate.py", common)
        fetch = common.index("Fetch remote main branch metadata")
        checkout = common.index("Checkout exact repository revision")
        self.assertLess(fetch, checkout)
        self.assertIn("vff_fiscal_app_git_dir.stat.exists", common)

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
        immutable_idx = self.adapter.index("Mark adapter immutable removed")
        clear_marker_idx = self.adapter.index("Clear SHM need_update_to marker via stdin helper")
        verify_marker_idx = self.adapter.index("Verify need_update_to remains cleared")
        files_started_idx = self.adapter.index("Mark adapter file modification started")
        install_helpers_idx = self.adapter.index("Install helper modules atomically")

        self.assertLess(immutable_idx, clear_marker_idx)
        self.assertLess(clear_marker_idx, verify_marker_idx)
        self.assertLess(verify_marker_idx, files_started_idx)
        self.assertLess(files_started_idx, install_helpers_idx)
        self.assertLess(
            self.adapter.index("Assert adapter CGI is mutable before file replacement"),
            install_helpers_idx,
        )
        pre_file_modification = self.adapter[immutable_idx:files_started_idx]
        self.assertIn("adapter_immutable_removed: true", pre_file_modification)
        self.assertNotIn("adapter_files_modification_started: true", pre_file_modification)
        rescue = self.adapter[
            self.adapter.index("rescue:") :
            self.adapter.index("Adapter post-unpause validation transaction")
        ]
        self.assertIn("adapter_files_modification_started | default(false) | bool", rescue)
        self.assertIn("finalize_adapter_immutable.yml", self.adapter)
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

    def test_restore_adapter_failure_rethrows_after_finalization(self) -> None:
        self.assertIn("adapter_restoration_failed: false", self.adapter_restore)
        self.assertIn("adapter_restoration_failed: true", self.adapter_restore)
        self.assertIn("Fail adapter restoration transaction", self.adapter_restore)
        self.assertLess(
            self.adapter_restore.index("Fail adapter restoration transaction"),
            self.adapter_restore.index("Finalize immutable protection for adapter restoration"),
        )
        post_transaction = self.adapter_restore[
            self.adapter_restore.index("Compile restored files in SHM spool after unpause") :
        ]
        self.assertIn("not (adapter_restoration_failed | default(false) | bool)", post_transaction)

    def test_cutover_failure_has_single_restore_cycle(self) -> None:
        self.assertNotIn("Validate automatic rescue through reusable restoration transaction", self.adapter)
        self.assertEqual(
            self.adapter[
                self.adapter.index("rescue:") :
                self.adapter.index("Adapter post-unpause validation transaction")
            ].count("Restore previous live adapter set while spool remains paused"),
            1,
        )

    def test_manual_rollback_has_safety_backup_and_safe_default(self) -> None:
        self.assertIn("pre-rollback adapter safety backup", self.adapter_rollback)
        self.assertIn("default(false)", self.adapter_rollback)
        self.assertGreaterEqual(self.adapter_rollback.count("restore_adapter.yml"), 2)

    def test_manual_rollback_manifest_is_after_fatal_restore_transaction(self) -> None:
        self.assertLess(
            self.adapter_rollback.index("Manual adapter rollback transaction"),
            self.adapter_rollback.index("Load existing deploy manifest after adapter rollback"),
        )
        self.assertIn("Fail manual rollback after restoring pre-rollback adapter", self.adapter_rollback)
        self.assertLess(
            self.adapter_rollback.index("Fail manual rollback after restoring pre-rollback adapter"),
            self.adapter_rollback.index("Load existing deploy manifest after adapter rollback"),
        )

    def test_no_spool_exec_in_paused_cutover_section(self) -> None:
        cutover = self.adapter[
            self.adapter.index("Adapter cutover block") :
            self.adapter.index("Adapter post-unpause validation transaction")
        ]
        self.assertNotIn("docker exec {{ shm_spool_container }}", cutover)

    def test_adapter_rejects_operator_prepaused_before_mutation(self) -> None:
        for content in (self.adapter, self.adapter_restore):
            self.assertIn("vff_fiscal_spool_was_already_paused", content)
            self.assertIn("paused by an operator", content)
        cutover = self.adapter[
            self.adapter.index("Adapter cutover block") :
            self.adapter.index("Back up active adapter CGI")
        ]
        self.assertIn("Reject operator-prepaused spool", cutover)
        self.assertNotIn("chattr", cutover)

    def test_chattr_remove_is_verified_before_replacement(self) -> None:
        for content in (self.adapter, self.adapter_restore):
            self.assertIn("chattr", content)
            self.assertIn("-i", content)
            self.assertIn("parse_lsattr.py", content)
            self.assertIn("immutable=0", content)
        self.assertLess(
            self.adapter_restore.index("Assert adapter CGI is mutable before restoration"),
            self.adapter_restore.index("Restore adapter helper files atomically"),
        )

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

    def test_mutating_playbooks_use_deployment_lock(self) -> None:
        playbooks = [
            "deploy.yml",
            "deploy-service.yml",
            "deploy-adapter.yml",
            "rollback-service.yml",
            "rollback-adapter.yml",
        ]
        for name in playbooks:
            content = (ROOT / "ansible/playbooks" / name).read_text()
            self.assertIn("acquire_deploy_lock.yml", content)
            self.assertIn("release_deploy_lock.yml", content)
            self.assertIn("always:", content)
        status = (ROOT / "ansible/playbooks/deploy-status.yml").read_text()
        self.assertNotIn("deploy_lock", status)

    def test_lock_release_requires_successful_acquire(self) -> None:
        acquire = (COMMON / "tasks/acquire_deploy_lock.yml").read_text()
        release = (COMMON / "tasks/release_deploy_lock.yml").read_text()
        self.assertIn("vff_fiscal_deploy_lock_acquired: false", acquire)
        self.assertIn("vff_fiscal_deploy_lock_acquired: true", acquire)
        self.assertIn("vff_fiscal_deploy_lock_acquire.rc == 0", acquire)
        self.assertIn("vff_fiscal_deploy_lock_acquired | default(false) | bool", release)
        self.assertIn("--controller-host", acquire)

    def test_rollback_compose_image_is_verified_against_metadata(self) -> None:
        self.assertIn("Resolve service image from rollback Compose candidate", self.service_rollback)
        self.assertIn("service_rollback_candidate_image.stdout | trim == service_rollback_image", self.service_rollback)
        self.assertIn("service_rollback_image_id.stdout == service_rollback_meta.previous_image_id", self.service_rollback)


if __name__ == "__main__":
    unittest.main()
