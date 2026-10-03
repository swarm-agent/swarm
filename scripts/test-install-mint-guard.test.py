#!/usr/bin/env python3
"""Requirement: native qualification never starts an unsuppressed test daemon.
Threat: container/sudo/systemd boundaries discard the flag, or verification accepts
an unrelated/invalid PID. Owners: test-install-distro.sh and mint_guard_* shell
functions. Execute these boundaries with temporary files and fake systemctl/runtime
commands: the narrowest hermetic layer, not live systemd or release evidence.
No real installer, daemon, Docker, provider, network, or host unit writes are used.
"""
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent
GUARD = SCRIPTS / "test-install-mint-guard.sh"


class MintGuardTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def shell(self, body, *args, env=None):
        return subprocess.run(
            ["bash", "-c", 'set -euo pipefail; source "$1"; shift; ' + body,
             "fixture", str(GUARD), *map(str, args)],
            env=env, capture_output=True, text=True, timeout=10,
        )

    def test_effective_environment_exact_key_and_no_disclosure(self):
        """The actual-process reader rejects missing/false/conflicting keys quietly."""
        for data, ok in [
            (b"PRIVATE=do-not-print\0SWARM_DISABLE_MINT_REPORT=1\0", True),
            (b"PRIVATE=do-not-print\0", False),
            (b"SWARM_DISABLE_MINT_REPORT=0\0", False),
            (b"SWARM_DISABLE_MINT_REPORT=false\0", False),
            (b"SWARM_DISABLE_MINT_REPORT=1\0SWARM_DISABLE_MINT_REPORT=0\0", False),
            (b"SWARM_DISABLE_MINT_REPORT=1\0SWARM_DISABLE_MINT_REPORT=1\0", False),
        ]:
            with self.subTest(data=data):
                fixture = self.root / "environ"
                fixture.write_bytes(data)
                result = self.shell('mint_guard_environment "$1"', fixture)
                self.assertEqual(result.returncode == 0, ok)
                self.assertNotIn("do-not-print", result.stdout + result.stderr)

    def test_generated_prestart_guard_fails_closed(self):
        """Execute the generated guard after systemd's documented $$ unescaping."""
        unit = self.root / "unit"
        result = self.shell('systemctl() { [[ "$*" == daemon-reload ]]; }; mint_guard_prepare "$1"', unit)
        self.assertEqual(result.returncode, 0, result.stderr)
        text = (unit / "90-no-mint.conf").read_text()
        self.assertIn("Environment=SWARM_DISABLE_MINT_REPORT=1\n", text)
        line = next(line for line in text.splitlines() if line.startswith("ExecStartPre="))
        self.assertIn("$${SWARM_DISABLE_MINT_REPORT:-}", line)
        argv = shlex.split(line.split("=", 1)[1].replace("$$", "$"))
        for value in [None, "", "0", "false", "1"]:
            env = dict(os.environ)
            env.pop("SWARM_DISABLE_MINT_REPORT", None)
            if value is not None:
                env["SWARM_DISABLE_MINT_REPORT"] = value
            result = subprocess.run(argv, env=env, capture_output=True, timeout=5)
            self.assertEqual(result.returncode == 0, value == "1")

    def test_mainpid_and_restart_environment(self):
        """MainPID must identify a readable, suppressed process on every check."""
        proc = self.root / "proc"
        for pid, value in [(123, "1"), (124, "0")]:
            (proc / str(pid)).mkdir(parents=True)
            (proc / str(pid) / "environ").write_bytes(
                f"SWARM_DISABLE_MINT_REPORT={value}\0".encode())
        for pid, ok in [("123", True), ("124", False), ("0", False),
                        ("../123", False), ("123\n124", False), ("999", False)]:
            result = self.shell('pid=$2; systemctl() { if [[ "$1" == show ]]; then printf "%s\\n" "$pid"; fi; }; mint_guard_verify "$1"', proc, pid)
            self.assertEqual(result.returncode == 0, ok, result.stderr)
        result = self.shell('systemctl() { return 1; }; mint_guard_verify "$1"', proc)
        self.assertNotEqual(result.returncode, 0)
        counter = self.root / "pid-read"
        result = self.shell('systemctl() { if [[ "$1" == show ]]; then if [[ -e "$counter" ]]; then echo 124; else touch "$counter"; echo 123; fi; fi; }; counter=$2; mint_guard_verify "$1"', proc, counter)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("mint_suppression=verified", result.stdout)

    def test_prepare_failure_prevents_following_install(self):
        """Write or reload failure must stop the shell before installer mutation."""
        for failure in ["write", "reload"]:
            unit = self.root / failure
            if failure == "write":
                unit.write_text("not a directory")
            marker = self.root / (failure + "-installer")
            result = self.shell('systemctl() { return 1; }; mint_guard_prepare "$1"; touch "$2"', unit, marker)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())

    def test_distro_preparation_failure_never_invokes_installer(self):
        """Execute both runner identities; a failed preparation stops dispatch."""
        archive = self.root / "candidate.tar.gz"
        archive.write_bytes(b"fixture: never extracted")
        checksum = self.root / "candidate.tar.gz.sha256"
        checksum.write_text("a" * 64 + "  candidate.tar.gz\n")
        runtime = self.root / "podman"
        runtime.write_text('''#!/usr/bin/env bash
set -eu
printf '%s\\n' "$*" >> "$CALLS"
case "$1" in
  info) echo true ;;
  exec)
    if [[ "$3" == systemctl ]]; then echo running
    elif [[ "$*" == *"test-install-mint-guard.sh prepare" ]]; then exit 42
    else echo UNEXPECTED_DISPATCH >> "$CALLS"; exit 99
    fi ;;
esac
''')
        runtime.chmod(0o755)
        for identity in ["root", "sudo"]:
            log = self.root / (identity + ".log")
            env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ["PATH"],
                       TMPDIR=str(self.root), CALLS=str(log), SWARM_INSTALL_DISTRO_RUNTIME="podman")
            result = subprocess.run(
                ["bash", str(SCRIPTS / "test-install-distro.sh"), "--archive", str(archive),
                 "--checksum", str(checksum), "--distro", "ubuntu", "--identity", identity],
                env=env, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            calls = log.read_text()
            self.assertIn("-e SWARM_DISABLE_MINT_REPORT=1", calls)
            self.assertIn("test-install-mint-guard.sh prepare", calls)
            self.assertNotIn("UNEXPECTED_DISPATCH", calls)


if __name__ == "__main__":
    unittest.main()
