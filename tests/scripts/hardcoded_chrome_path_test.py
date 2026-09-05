#!/usr/bin/env python3
"""Requirement: allow only the reviewed fixed Chrome declarations, not arbitrary
absolute paths in the testbench wrapper. Authority: check-hardcoded-paths.sh
filter_allowed_runtime_paths, run-testbench-runner.sh, htmlcapture.SystemChromePath.
Run the real guard against isolated file fixtures to prove rejection status and
unchanged bytes; no Chrome, provider, tunnel, or host mutation is needed. The
existing routing test separately checks the wrapper's argument forwarding.
"""
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
RUNNER = 'scripts/run-testbench-runner.sh'
CHROME = '/opt/google/chrome/chrome'
DECLARATION = f"readonly SYSTEM_CHROME_PATH='{CHROME}'"


class ChromePathGuardTests(unittest.TestCase):
    def scan(self, files, accepted):
        with tempfile.TemporaryDirectory(dir=os.environ['TMPDIR']) as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            guard = root / 'scripts/check-hardcoded-paths.sh'
            shutil.copyfile(ROOT / 'scripts/check-hardcoded-paths.sh', guard)
            for name, content in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content)
            before = {p.relative_to(root): p.read_bytes()
                      for p in root.rglob('*') if p.is_file()}
            result = subprocess.run(['bash', str(guard)], text=True,
                                    capture_output=True, timeout=15)
            self.assertEqual(result.returncode, 0 if accepted else 1,
                             result.stdout + result.stderr)
            self.assertIn('[path-check] PASS' if accepted else
                          'FAIL: disallowed absolute path literals found:', result.stdout)
            after = {p.relative_to(root): p.read_bytes()
                     for p in root.rglob('*') if p.is_file()}
            self.assertEqual(before, after, 'path guard must not modify source')

    def test_fixed_constants_and_current_wrapper_pass(self):
        runner = (ROOT / RUNNER).read_text()
        renderer = (ROOT / 'swarmd/internal/htmlcapture/renderer.go').read_text()
        match = re.search(r'^\s*SystemChromePath\s*=\s*"([^"]+)"$', renderer, re.M)
        self.assertIsNotNone(match)
        self.assertEqual(match.group(1), CHROME)
        self.assertIn(DECLARATION + '\n', runner)
        self.assertIn('[[ -x "${SYSTEM_CHROME_PATH}" ]] ||', runner)
        self.assertIn('browser_args+=(--browser-executable "${SYSTEM_CHROME_PATH}")', runner)
        self.scan({RUNNER: runner,
                   'swarmd/internal/htmlcapture/renderer.go': match.group(0) + '\n',
                   'scripts/test-testbench-container-routing.sh':
                       (ROOT / 'scripts/test-testbench-container-routing.sh').read_text()}, True)

    def test_near_misses_and_extra_paths_remain_rejected(self):
        cases = [
            (RUNNER, DECLARATION.replace('/chrome/chrome', '/chrome/other')),
            (RUNNER, DECLARATION + '; cat /etc/shadow'),
            (RUNNER, DECLARATION + '\ncat /opt/unreviewed/browser'),
            (RUNNER, DECLARATION.replace('readonly ', '')),
            (RUNNER, DECLARATION.replace("'", '"')),
            (RUNNER, '  ' + DECLARATION),
            ('scripts/unrelated.sh', DECLARATION),
            ('scripts/run-testbench-runner.sh.go', DECLARATION),
            ('scripts/nested/run-testbench-runner.sh', DECLARATION),
            ('swarmd/internal/htmlcapture/renderer.go',
             f'SystemChromePath = "{CHROME}-other"'),
        ]
        for name, text in cases:
            with self.subTest(name=name, text=text):
                self.scan({name: text + '\n'}, False)


if __name__ == '__main__':
    unittest.main()
