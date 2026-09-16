"""Requirement-first tests for local_testbench_pool.Pool.

Invariant: one private OS-principal authority atomically reserves bounded slots;
only matching lane/generation operations mutate them, and uncertain cleanup never
frees capacity. Threats: competing processes, stale tokens, foreign lane/principal,
symlink substitution, corrupt state, interrupted deployment and unknown resources.
The stdlib unit/process layer exercises actual flock/atomic files without a runtime
or credentials. Fake adapters prove cleanup decisions, not container isolation,
resource-limit enforcement, port binding, Git correctness or real runtime health.
"""
import dataclasses
import json
import multiprocessing
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from local_testbench_pool import Config, Lane, Pool, PoolError


def competing_claim(root, common, worktree, start, results):
    start.wait(5)
    try:
        with Pool(Config(root, slots=1)) as pool:
            record = pool.claim(Lane(common, worktree))
            results.put(('claimed', record['generation']))
    except PoolError as exc:
        results.put(('denied', str(exc)))


class FakeRuntime:
    def __init__(self, observed='absent', fail=False):
        self.observed = observed
        self.fail = fail
        self.stops = []

    def inspect(self, record):
        return self.observed

    def stop(self, record):
        self.stops.append(record)
        if self.fail:
            raise RuntimeError('injected cleanup failure')
        self.observed = 'absent'


class PoolTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'pool'
        self.root.mkdir(mode=0o700)
        self.common = Path(self.tmp.name) / 'git'
        self.common.mkdir()
        self.worktrees = []
        for i in range(3):
            path = Path(self.tmp.name) / ('worktree' + str(i))
            path.mkdir()
            self.worktrees.append(path)
        self.lane = Lane(str(self.common), str(self.worktrees[0]))
        self.config = Config(str(self.root), slots=1)
        self.pool = Pool(self.config)
        self.addCleanup(self.pool.close)

    def raw(self):
        return (self.root / 'state.json').read_bytes()

    def test_competing_processes_reserve_only_one_slot(self):
        ctx = multiprocessing.get_context('spawn')
        start, results = ctx.Event(), ctx.Queue()
        children = [ctx.Process(target=competing_claim,
                    args=(str(self.root), str(self.common), str(w), start, results))
                    for w in self.worktrees[:2]]
        try:
            for child in children:
                child.start()
            start.set()
            outcomes = [results.get(timeout=10) for _ in children]
            for child in children:
                child.join(10)
                self.assertEqual(child.exitcode, 0)
            self.assertEqual(sorted(x[0] for x in outcomes), ['claimed', 'denied'])
            self.assertEqual(len(json.loads(self.raw())['records']), 1)
        finally:
            for child in children:
                if child.is_alive():
                    child.terminate()
                    child.join(5)
            results.close()
            results.join_thread()

    def test_lane_generation_and_principal_denials_do_not_mutate(self):
        r = self.pool.claim(self.lane)
        before = self.raw()
        other = Lane(str(self.common), str(self.worktrees[1]))
        runtime = FakeRuntime('owned')
        for lane, generation in [(other, r['generation']), (self.lane, '0' * 64)]:
            for operation in (lambda: self.pool.status(lane, generation),
                              lambda: self.pool.touch(lane, generation),
                              lambda: self.pool.stop(lane, generation, runtime)):
                with self.assertRaises(PoolError):
                    operation()
                self.assertEqual(self.raw(), before)
        with mock.patch('local_testbench_pool.os.geteuid', return_value=os.geteuid() + 1):
            with self.assertRaises(PoolError):
                Pool(self.config)
        self.assertEqual(self.raw(), before)
        self.assertEqual(runtime.stops, [])

    def test_restart_cleans_interrupted_deployment_and_rotates_generation(self):
        r = self.pool.claim(self.lane)
        self.pool.transition(self.lane, r['generation'], 'reserved', 'deploying')
        runtime = FakeRuntime('owned')
        with Pool(self.config) as restarted:
            self.assertEqual(restarted.status(self.lane, r['generation'])['state'], 'deploying')
            self.assertEqual(restarted.recover(runtime, restart=True), [{'slot': 1, 'state': 'inactive'}])
            new = restarted.claim(self.lane)
            self.assertNotEqual(new['generation'], r['generation'])
            with self.assertRaises(PoolError):
                restarted.touch(self.lane, r['generation'])
        self.assertEqual(len(runtime.stops), 1)

    def test_unknown_resources_and_failed_cleanup_retain_capacity(self):
        r = self.pool.claim(self.lane)
        for runtime in (FakeRuntime('unknown'), FakeRuntime('owned', fail=True)):
            with self.assertRaises(PoolError):
                self.pool.stop(self.lane, r['generation'], runtime)
            self.assertEqual(self.pool.status(self.lane, r['generation'])['state'], 'failed')
            with self.assertRaisesRegex(PoolError, 'pool full'):
                self.pool.claim(Lane(str(self.common), str(self.worktrees[1])))
            if runtime.observed == 'unknown':
                self.assertEqual(runtime.stops, [])
        self.pool.stop(self.lane, r['generation'], FakeRuntime('absent'))
        self.assertEqual(self.pool.status(self.lane, r['generation'])['state'], 'inactive')

    def test_expired_lease_reaps_but_touch_protects_ready_lane(self):
        with mock.patch('local_testbench_pool.time.time', return_value=100):
            r = self.pool.claim(self.lane)
        self.pool.transition(self.lane, r['generation'], 'reserved', 'deploying')
        self.pool.transition(self.lane, r['generation'], 'deploying', 'ready')
        with mock.patch('local_testbench_pool.time.time', return_value=1800):
            self.pool.touch(self.lane, r['generation'])
        with mock.patch('local_testbench_pool.time.time', return_value=2000):
            self.assertEqual(self.pool.recover(FakeRuntime(), restart=True), [])
        with mock.patch('local_testbench_pool.time.time', return_value=4000):
            self.assertEqual(self.pool.recover(FakeRuntime()), [{'slot': 1, 'state': 'inactive'}])

    def test_config_symlink_and_file_bounds_fail_closed(self):
        for kwargs in ({'root': 'relative'}, {'slots': 33}, {'slots': 3},
                       {'queue_timeout': float('nan')}, {'tasks': True}):
            values = dataclasses.asdict(self.config)
            values.update(kwargs)
            with self.assertRaises(PoolError):
                Config(**values)
        link = Path(self.tmp.name) / 'link'
        link.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(OSError):
            Pool(dataclasses.replace(self.config, root=str(link)))
        (self.root / 'state.json').symlink_to(self.common / 'missing')
        with self.assertRaises(OSError):
            self.pool.claim(self.lane)
        self.assertFalse((self.common / 'missing').exists())
        (self.root / 'state.json').unlink()
        self.pool.claim(self.lane)
        (self.root / 'state.json').write_bytes(b'x' * (Pool.MAX_STATE + 1))
        before = self.raw()
        with self.assertRaisesRegex(PoolError, 'oversized'):
            self.pool.claim(self.lane)
        self.assertEqual(self.raw(), before)

    def test_stale_transition_and_write_failure_preserve_previous_state(self):
        r = self.pool.claim(self.lane)
        before = self.raw()
        with self.assertRaises(PoolError):
            self.pool.transition(self.lane, r['generation'], 'reserved', 'ready')
        with mock.patch('local_testbench_pool.os.replace', side_effect=OSError('injected')):
            with self.assertRaises(OSError):
                self.pool.transition(self.lane, r['generation'], 'reserved', 'deploying')
        self.assertEqual(self.raw(), before)
        self.assertEqual(list(self.root.glob('.state-*')), [])

    def test_lock_and_queue_waits_are_bounded(self):
        import fcntl
        import time
        with Pool(dataclasses.replace(self.config, lock_timeout=0.05)) as bounded:
            with open(self.root / 'pool.lock', 'a') as lock:
                os.chmod(self.root / 'pool.lock', 0o600)
                fcntl.flock(lock, fcntl.LOCK_EX)
                started = time.monotonic()
                with self.assertRaisesRegex(PoolError, 'lock timeout'):
                    bounded.claim(self.lane)
                self.assertLess(time.monotonic() - started, 2)
        config = dataclasses.replace(self.config, queue_timeout=0.05)
        with Pool(config) as bounded:
            bounded.claim(self.lane)
            started = time.monotonic()
            with self.assertRaisesRegex(PoolError, 'pool full'):
                bounded.claim(Lane(str(self.common), str(self.worktrees[1])))
            self.assertLess(time.monotonic() - started, 2)

    def test_config_changes_fail_closed(self):
        self.pool.claim(self.lane)
        before = self.raw()
        with Pool(dataclasses.replace(self.config, lease_seconds=60)) as changed:
            with self.assertRaisesRegex(PoolError, 'config differs'):
                changed.claim(self.lane)
        self.assertEqual(self.raw(), before)


if __name__ == '__main__':
    unittest.main()
