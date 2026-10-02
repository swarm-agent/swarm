#!/usr/bin/env python3
"""Prove the Dockerfile package boundary updates inherited packages fail-closed.

Regression: installing Git alone leaves vulnerable base packages unchanged.
Execute only the Dockerfile package prefix with stub apt: no image execution,
root access, network, or host package mutation. Image security is separately
verified by the release scanner against the actual image, not this unit test.
"""
from pathlib import Path
import os
import subprocess
import tempfile
import unittest


class RuntimePackages(unittest.TestCase):
    def run_packages(self, apt_failure=''):
        dockerfile = (Path(__file__).resolve().parents[1] / 'Dockerfile').read_text()
        command = dockerfile.split('RUN apt-get update', 1)[1].split('rm -rf /var/lib/apt/lists/*', 1)[0]
        command = 'apt-get update' + command.replace('\\\n', '')
        command = command.rstrip().removesuffix('&&').rstrip()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            log = root / 'calls'
            path = root / 'apt-get'
            path.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALLS"\n[ "$1" != "$APT_FAILURE" ]\n')
            path.chmod(0o755)
            env = dict(os.environ, PATH=tmp + ':' + os.environ['PATH'],
                       CALLS=str(log), APT_FAILURE=apt_failure)
            result = subprocess.run(['sh', '-c', command], env=env, capture_output=True, timeout=5)
            return result.returncode, log.read_text().splitlines()

    def test_upgrade_precedes_install(self):
        code, calls = self.run_packages()
        self.assertEqual(code, 0)
        self.assertEqual(calls[0], 'update')
        self.assertEqual(calls[1], 'upgrade -y --no-install-recommends')
        self.assertTrue(calls[2].startswith('install -y --no-install-recommends '))
        for package in ('git', 'bash', 'ca-certificates', 'libpcre2-8-0'):
            self.assertIn(package, calls[2].split())

    def test_package_failure_stops_build(self):
        for step, count in [('update', 1), ('upgrade', 2), ('install', 3)]:
            with self.subTest(step=step):
                code, calls = self.run_packages(step)
                self.assertNotEqual(code, 0)
                self.assertEqual(len(calls), count)


if __name__ == '__main__':
    unittest.main()
