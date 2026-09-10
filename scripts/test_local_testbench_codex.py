"""Requirement: local Codex routing must not alter the existing supervisor/remote
runtime, leak guest logs, or silently fall back. Tests target guest(), render()
and shell data validation, the narrowest runner-only configuration boundaries.
"""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from local_testbench_codex import guest
import local_testbench_runtime as runtime

ROOT = Path(__file__).resolve().parent.parent


class CodexTests(unittest.TestCase):
    def test_guest_overlay_and_scrubbing(self):
        original = runtime.GUEST
        value = guest()
        self.assertIn('go build -overlay /out/codex-overlay.json', value)
        self.assertNotIn('2>/exchange/startup-error', value)
        self.assertEqual(runtime.GUEST, original)
        self.assertNotIn('credentials', value)

    def test_overlay_is_external_and_explicit(self):
        spec = importlib.util.spec_from_file_location('overlay', ROOT / 'scripts/testbench-codex-overlay.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            result = module.render(ROOT, directory)
            self.assertTrue(result.exists())
            text = (Path(directory) / 'testbench-daemon.go').read_text()
            self.assertIn('testbenchcodex.Configure(identitySvc', text)
        with self.assertRaises(ValueError):
            module.render(ROOT, ROOT)

    def test_attachment_rejects_legacy_profile_without_mutation(self):
        from local_testbench_codex import inspect_ready
        from local_testbench_pool import PoolError
        class FakePool:
            def status(self, lane, generation):
                return {'state': 'ready'}
        class FakeRuntime:
            def read_manifest(self, record):
                return {'head': 'exact'}
            def inspect(self, record):
                return 'owned'
            def units(self, record):
                raise AssertionError('legacy profile reached endpoint attachment')
        with self.assertRaises(PoolError):
            inspect_ready(FakePool(), FakeRuntime(), 'lane', 'generation', 'exact')

    def test_local_posture_and_remote_rejection(self):
        command = '''source scripts/lib-testbench-e2e.sh
SWARM_TESTBENCH_TARGET=local
SWARM_TESTBENCH_PROVIDER=codex
SWARM_TESTBENCH_REVERSE_LOCAL_PORT= SWARM_TESTBENCH_REVERSE_REMOTE_PORT=
for role in MODEL ACTION_MODEL PLAN_MODEL CODER_MODEL DESIGNER_MODEL; do printf -v "SWARM_TESTBENCH_$role" %s gpt-5.6-luna; done
for role in THINKING ACTION_THINKING PLAN_THINKING CODER_THINKING DESIGNER_THINKING; do printf -v "SWARM_TESTBENCH_$role" %s medium; done
swarm_testbench_validate_env || exit 10
SWARM_TESTBENCH_CODER_MODEL=unexpected
if swarm_testbench_validate_env; then exit 11; fi
SWARM_TESTBENCH_CODER_MODEL=gpt-5.6-luna
SWARM_TESTBENCH_PROVIDER=fireworks
if swarm_testbench_validate_env; then exit 12; fi
'''
        result = subprocess.run(['bash', '-c', command], cwd=ROOT, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr.decode())


if __name__ == '__main__':
    unittest.main()
