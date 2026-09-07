#!/usr/bin/env python3
"""Purpose: prove launch supervisor lifecycle, not product correctness.
Threats: hangs/escaped descendants, noisy output, lost siblings, cancellation and
false pass. Authority: launch-prerun-supervisor.py run(); actual subprocesses are
the narrowest layer proving overlap and OS termination. No network/providers.
"""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
SUPERVISOR = ROOT / 'scripts/launch-prerun-supervisor.py'


def command(name, code):
    return {'id': name, 'argv': [sys.executable, '-u', '-c', code]}


class Lifecycle(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=os.environ['TMPDIR'])
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def start(self, commands, jobs=2, wall=3, stall=3):
        env = dict(os.environ, SWARM_LAUNCH_WALL_SECONDS=str(wall),
                   SWARM_LAUNCH_STALL_SECONDS=str(stall), SWARM_LAUNCH_OUTPUT_BYTES='4096')
        p = subprocess.Popen([sys.executable, str(SUPERVISOR), str(self.root/'run'), str(jobs)],
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
        p.stdin.write(json.dumps(commands).encode())
        p.stdin.close()
        p.stdin = None
        self.addCleanup(lambda: p.poll() is None and p.kill())
        return p

    def result(self, p):
        out, err = p.communicate(timeout=10)
        self.assertLess(len(out)+len(err), 16384)
        data = json.loads((self.root/'run/results.json').read_text())
        return data, out.decode()

    def assert_dead(self, pid):
        for _ in range(30):
            if not Path(f'/proc/{pid}').exists():
                return
            time.sleep(.05)
        self.fail(f'owned descendant remains: {pid}')

    def test_overlap_limit_and_nonzero_sibling_preservation(self):
        cmds = [command('first', 'import time; time.sleep(.4)'), command('second', 'import time; time.sleep(.4)'),
                command('failure', 'raise SystemExit(7)'), command('last', 'print("retained")')]
        p = self.start(cmds)
        data, _ = self.result(p)
        self.assertEqual(p.returncode, 1)
        self.assertEqual(data['counts'], {'pass': 3, 'fail': 1, 'not-run': 0})
        rows = {r['id']: r for r in data['results']}
        self.assertLess(rows['second']['started_at'], rows['first']['finished_at'])
        self.assertGreaterEqual(rows['failure']['started_at'], min(rows['first']['finished_at'], rows['second']['finished_at']))
        self.assertEqual(rows['failure']['exit_code'], 7)
        self.assertEqual((self.root/'run/logs/last.log').read_text(), 'retained\n')

    def test_stall_kills_setsid_grandchild_and_preserves_sibling(self):
        marker = self.root/'pid'
        child = f'import os,signal,time; os.setsid(); signal.signal(signal.SIGTERM,signal.SIG_IGN); open({str(marker)!r},"w").write(str(os.getpid())); time.sleep(60)'
        parent = f'import subprocess,sys,time; subprocess.Popen([sys.executable,"-c",{child!r}]); time.sleep(60)'
        p = self.start([command('hang', parent), command('success', 'print("done")')], wall=3, stall=1)
        data, out = self.result(p)
        rows = {r['id']:r for r in data['results']}
        self.assertEqual(rows['hang']['reason'], 'stalled')
        self.assertEqual(rows['success']['outcome'], 'pass')
        self.assertLess(rows['hang']['duration_seconds'], 4)
        self.assertIn('[WAIT]', out)
        self.assert_dead(int(marker.read_text()))

    def test_wall_deadline_even_with_progress(self):
        p = self.start([command('busy', 'import time\nwhile True: print("progress",flush=True); time.sleep(.1)')], wall=1, stall=1)
        data, _ = self.result(p)
        self.assertEqual(data['results'][0]['reason'], 'wall_timeout')
        self.assertEqual(data['results'][0]['exit_code'], 124)

    def test_output_cap_and_success_retained(self):
        p = self.start([command('noisy', 'import os\nwhile True: os.write(1,b"x"*8192)'), command('success', 'print("done")')])
        data, _ = self.result(p)
        rows = {r['id']:r for r in data['results']}
        self.assertEqual(rows['noisy']['reason'], 'output_limit')
        self.assertEqual((self.root/'run/logs/noisy.log').stat().st_size, 4096)
        self.assertEqual(rows['success']['outcome'], 'pass')

    def test_cancel_retains_completed_and_marks_unstarted(self):
        marker = self.root/'pid'
        child = f'import os,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); open({str(marker)!r},"w").write(str(os.getpid())); time.sleep(60)'
        p = self.start([command('done', 'print("done")'), command('held', child), command('queued', 'print("must not run")')], jobs=1)
        for _ in range(100):
            if marker.exists(): break
            time.sleep(.02)
        self.assertTrue(marker.exists())
        p.send_signal(signal.SIGTERM)
        data, _ = self.result(p)
        self.assertEqual(p.returncode, 130)
        self.assertEqual(data['counts'], {'pass': 1, 'fail': 1, 'not-run': 1})
        self.assertFalse((self.root/'run/logs/queued.log').exists())
        self.assert_dead(int(marker.read_text()))

    def test_spawn_failure_and_invalid_manifest(self):
        p = self.start([{'id': 'missing', 'argv': [str(self.root/'missing-executable')]}, command('ok', 'print("ok")')])
        data, _ = self.result(p)
        rows = {r['id']: r for r in data['results']}
        self.assertEqual(rows['missing']['reason'], 'spawn_error')
        self.assertEqual(rows['missing']['exit_code'], 127)
        self.assertEqual(rows['ok']['outcome'], 'pass')
        bad = subprocess.run([sys.executable, str(SUPERVISOR), str(self.root/'invalid'), '9'],
                             input=json.dumps([command('bad', 'print("bad")')]), capture_output=True, text=True, timeout=5)
        self.assertNotEqual(bad.returncode, 0)
        self.assertFalse((self.root/'invalid').exists())

    def test_shell_adapter_cancellation_reaches_owned_descendants(self):
        marker = self.root/'adapter-pid'
        body = f'import os,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); open({str(marker)!r},"w").write(str(os.getpid())); time.sleep(60)'
        env = dict(os.environ, BODY=body, DEST=str(self.root/'run'), REPO=str(ROOT))
        script = 'source "$REPO/scripts/lib-launch-prerun.sh"; swarm_launch_prerun_lane_command() { local -n out="$2"; out=(python3 -u -c "$BODY"); }; swarm_launch_prerun_run_parallel "$DEST" 1 held queued'
        p = subprocess.Popen(['bash', '-c', script], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.addCleanup(lambda: p.poll() is None and p.kill())
        for _ in range(100):
            if marker.exists(): break
            time.sleep(.02)
        self.assertTrue(marker.exists())
        p.send_signal(signal.SIGTERM)
        p.communicate(timeout=8)
        self.assertEqual(p.returncode, 130)
        data = json.loads((self.root/'run/results.json').read_text())
        self.assertEqual(data['counts'], {'pass':0,'fail':1,'not-run':1})
        self.assert_dead(int(marker.read_text()))

    def test_cleanup_grace_allows_ack_but_never_turns_cancel_into_pass(self):
        # Requirement: a provider runner needs bounded time to acknowledge remote
        # stop after TERM. Exercise real signals beyond the former .5s KILL bound.
        marker = self.root/'ready'
        ack = self.root/'ack'
        body = (f'import signal,time,pathlib\n'
                f'def stop(*args):\n time.sleep(.8)\n pathlib.Path({str(ack)!r}).write_text("stopped")\n raise SystemExit(0)\n'
                f'signal.signal(signal.SIGTERM,stop)\npathlib.Path({str(marker)!r}).write_text("ready")\ntime.sleep(60)')
        cmd = command('remote', body)
        cmd['cleanup_seconds'] = 2
        p = self.start([cmd, command('sibling', 'print("preserved")')], wall=4, stall=4)
        for _ in range(100):
            if marker.exists() and (self.root/'run/status/sibling.exit').exists(): break
            time.sleep(.02)
        self.assertTrue(marker.exists())
        self.assertTrue((self.root/'run/status/sibling.exit').exists())
        p.send_signal(signal.SIGTERM)
        data, _ = self.result(p)
        self.assertEqual(ack.read_text(), 'stopped')
        rows = {r['id']:r for r in data['results']}
        self.assertEqual(rows['remote']['reason'], 'cancelled')
        self.assertEqual(rows['remote']['outcome'], 'fail')
        self.assertEqual(rows['sibling']['outcome'], 'pass')
        self.assertEqual(rows['remote']['remaining_pids'], [])

    def test_cleanup_grace_is_bounded_and_rejects_before_spawn(self):
        for i, grace in enumerate([0, 26, True, '25', float('nan')]):
            cmd = command('invalid', 'raise SystemExit(99)')
            cmd['cleanup_seconds'] = grace
            target = self.root/f'invalid-{i}'
            p = subprocess.run([sys.executable, str(SUPERVISOR), str(target), '1'],
                               input=json.dumps([cmd]), capture_output=True, text=True, timeout=5)
            self.assertNotEqual(p.returncode, 0)
            self.assertFalse(target.exists())

    def test_orphan_cannot_report_success(self):
        marker = self.root/'pid'
        child = f'import os,time; open({str(marker)!r},"w").write(str(os.getpid())); time.sleep(60)'
        parent = f'import subprocess,sys,time; subprocess.Popen([sys.executable,"-c",{child!r}]); time.sleep(.3)'
        p = self.start([command('orphan', parent)])
        data, _ = self.result(p)
        self.assertEqual(data['results'][0]['reason'], 'orphaned_descendants')
        self.assertEqual(data['results'][0]['outcome'], 'fail')
        self.assert_dead(int(marker.read_text()))


if __name__ == '__main__':
    unittest.main()
