#!/usr/bin/env python3
"""Requirement: the exact-candidate Ubuntu gate must stop on incomplete or stalled
APT acquisition before any candidate installation, without changing repository
trust or provisioning Git early. Authority: test-install-distro.sh and its
install-distro-ubuntu-bootstrap.sh image helper. Execute both shell boundaries
with fake package/runtime commands: no Docker, network, privileges, or host APT
writes. Real GNU timeout proves process termination under an accelerated budget;
this narrow layer does not claim live mirror or systemd installation evidence.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "scripts/install-distro-ubuntu-bootstrap.sh"
RUNNER = ROOT / "scripts/test-install-distro.sh"


class UbuntuBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ["TMPDIR"])
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = {
            "PATH": str(self.bin) + os.pathsep + os.defpath,
            "TMPDIR": str(self.root),
            "TEST_ROOT": str(self.root),
            "REAL_TIMEOUT": shutil.which("timeout"),
        }
        self.command("install", '''cat > "$TEST_ROOT/apt.conf"
printf '%s\\n' "$*" > "$TEST_ROOT/install.args"
''')
        self.command("apt-get", '''printf '%s\\n' "$*" >> "$TEST_ROOT/apt.calls"
case "$1" in
  update) action="${UPDATE_ACTION:-pass}" ;;
  install) action="${INSTALL_ACTION:-pass}" ;;
  *) exit 99 ;;
esac
case "$action" in
  fail) echo 'synthetic acquisition failure' >&2; exit 100 ;;
  hang) exec sleep 30 ;;
esac
''')
        self.command("timeout", '''printf '%s\\n' "$*" >> "$TEST_ROOT/timeout.calls"
shift 2
budget="$1"; shift
if [ "${SHORT_BUDGET:-}" = yes ]; then budget=0.15s; fi
exec "$REAL_TIMEOUT" --verbose --kill-after=0.1s "$budget" "$@"
''')

    def command(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\nset -eu\n" + body)
        path.chmod(0o755)

    def run_helper(self, **env):
        return subprocess.run(
            ["bash", str(HELPER)], env=self.env | env,
            capture_output=True, text=True, timeout=5,
        )

    def text(self, name):
        return (self.root / name).read_text()

    def test_success_preserves_trust_and_git_absence(self):
        result = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.text("apt.calls").splitlines(), [
            "update", "install -y --no-install-recommends ca-certificates curl sudo systemd",
        ])
        self.assertEqual(self.text("apt.conf"),
                         'Acquire::Retries "0";\nAcquire::http::Timeout "30";\n'
                         'Acquire::https::Timeout "30";\nAPT::Update::Error-Mode "any";\n')
        self.assertEqual(self.text("install.args").strip(),
                         "-m 0644 /dev/stdin /etc/apt/apt.conf.d/99swarm-install-test")
        calls = self.text("timeout.calls").splitlines()
        self.assertEqual(calls[0], "--verbose --kill-after=5s 180s apt-get update")
        self.assertEqual(calls[1], "--verbose --kill-after=5s 240s env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl sudo systemd")
        self.assertIn("stage=apt-install passed", result.stdout)

    def test_update_failure_never_installs(self):
        result = self.run_helper(UPDATE_ACTION="fail")
        self.assertEqual(result.returncode, 100)
        self.assertEqual(self.text("apt.calls"), "update\n")
        self.assertIn("stage=apt-update failed exit=100", result.stderr)
        self.assertNotIn("passed", result.stdout)

    def test_install_failure_is_not_success(self):
        result = self.run_helper(INSTALL_ACTION="fail")
        self.assertEqual(result.returncode, 100)
        self.assertEqual(len(self.text("apt.calls").splitlines()), 2)
        self.assertIn("stage=apt-install failed exit=100", result.stderr)
        self.assertNotIn("stage=apt-install passed", result.stdout)

    def test_update_deadline_stops_before_install(self):
        result = self.run_helper(UPDATE_ACTION="hang", SHORT_BUDGET="yes")
        self.assertEqual(result.returncode, 124, result.stderr)
        self.assertEqual(self.text("apt.calls"), "update\n")
        self.assertIn("stage=apt-update failed exit=124 budget=180s", result.stderr)

    def test_install_deadline_stops_without_retry(self):
        result = self.run_helper(INSTALL_ACTION="hang", SHORT_BUDGET="yes")
        self.assertEqual(result.returncode, 124, result.stderr)
        self.assertEqual(len(self.text("apt.calls").splitlines()), 2)
        self.assertIn("stage=apt-install failed exit=124 budget=240s", result.stderr)

    def test_runner_copies_exact_helper_and_stops_on_build_failure(self):
        self.command("fake-runtime", '''printf '%s\\n' "$1" >> "$TEST_ROOT/runtime.calls"
if [ "$1" = build ]; then
  for arg in "$@"; do context="$arg"; done
  cp "$context/Containerfile" "$TEST_ROOT/Containerfile"
  cp "$context/bootstrap-ubuntu.sh" "$TEST_ROOT/helper"
  exit 42
fi
''')
        archive = self.root / "candidate.tar.gz"
        archive.write_text("synthetic candidate")
        checksum = self.root / "candidate.tar.gz.sha256"
        checksum.write_text("a" * 64 + "  candidate.tar.gz\n")
        result = subprocess.run(
            ["bash", str(RUNNER), "--archive", str(archive), "--checksum", str(checksum), "--distro", "ubuntu"],
            env=self.env | {"SWARM_INSTALL_DISTRO_RUNTIME": "fake-runtime"},
            capture_output=True, text=True, timeout=5,
        )
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertEqual(self.text("helper"), HELPER.read_text())
        self.assertEqual(self.text("Containerfile"),
                         'FROM docker.io/library/ubuntu:24.04\n'
                         'COPY bootstrap-ubuntu.sh /bootstrap-ubuntu.sh\n'
                         'RUN bash /bootstrap-ubuntu.sh\nSTOPSIGNAL SIGRTMIN+3\n'
                         'CMD ["/usr/lib/systemd/systemd"]\n')
        self.assertEqual(self.text("runtime.calls").splitlines(), ["build", "rm", "image"])
        self.assertNotIn("install=passed", result.stdout)
        self.assertEqual(list(self.root.glob("swarm-install-image.*")), [])


if __name__ == "__main__":
    unittest.main()
