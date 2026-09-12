#!/usr/bin/env python3
"""Requirement: installed onboarding is mandatory in the canonical manifest.
Threat: identity-only false pass, lost argv, swallowed failure, timeout or leaked
child. Authority: run-testbench-launch-prerun.sh -> lib-launch-prerun.sh -> real
supervisor. Fake proof processes exercise dispatch, not installed product behavior.
No containers, SSH, providers or shared services are used.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
ENTRY = ROOT / 'scripts/run-testbench-launch-prerun.sh'


class Dispatch(unittest.TestCase):
    def test_required_inputs_and_attach_refusal(self):
        env = dict(os.environ, SWARM_TESTBENCH_ENV_FILE='/nonexistent-test-config')
        for args in [
            ['--suite', 'installed-new-user', '--dry-run'],
            ['--attach-only', 'http://127.0.0.1:12345', '--suite', 'installed-new-user'],
        ]:
            result = subprocess.run(['bash', str(ENTRY), *args], env=env,
                                    capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn('Preflight:', result.stdout)
        listed = subprocess.check_output(['bash', str(ENTRY), '--list-suites'], text=True, timeout=5)
        for case in ('new-user', 'existing-user', 'normal-user'):
            self.assertIn('installed-' + case, listed.splitlines())

    def test_dispatch_failure_and_timeout_keep_all_results(self):
        with tempfile.TemporaryDirectory(dir=os.environ['TMPDIR']) as tmp:
            root = Path(tmp)
            proof = root / 'proof with spaces.py'
            proof.write_text('''import json, os, sys, time
from pathlib import Path
args = sys.argv[1:]
case = args[args.index('--case') + 1]
Path(os.environ['RECORD'], case + '.json').write_text(json.dumps(args))
print('phase=fixture', flush=True)
if case == 'existing-user': sys.exit(7)
if case == 'normal-user': time.sleep(20)
''')
            archive, checksum = root / 'candidate archive.tar.gz', root / 'candidate archive.tar.gz.sha256'
            env = dict(os.environ, TMPDIR=tmp, RECORD=tmp,
                       SWARM_TESTBENCH_ENV_FILE=str(root / 'absent.env'))
            args = ['bash', str(ENTRY), '--jobs', '2', '--wall-seconds', '2', '--stall-seconds', '1',
                    '--installed-onboarding-runner', str(proof), '--candidate-archive', str(archive),
                    '--candidate-checksum', str(checksum)]
            for case in ('new-user', 'existing-user', 'normal-user'):
                args += ['--suite', 'installed-' + case]
            result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=45)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertNotIn('Preflight:', result.stdout)
            summaries = list(root.glob('swarm-launch-prerun.*/summary.tsv'))
            self.assertEqual(len(summaries), 1, result.stdout + result.stderr)
            rows = {row.split('\t')[0]: row.split('\t')[1] for row in summaries[0].read_text().splitlines()}
            self.assertEqual(rows['installed-new-user'], '0')
            self.assertEqual(rows['installed-existing-user'], '7')
            self.assertNotEqual(rows['installed-normal-user'], '0')
            for case in ('new-user', 'existing-user', 'normal-user'):
                self.assertEqual(json.loads((root / (case + '.json')).read_text()),
                                 ['--archive', str(archive), '--checksum', str(checksum), '--case', case])


if __name__ == '__main__':
    unittest.main()
