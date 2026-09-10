"""Requirement-first runtime adapter tests; no root commands or live containers.

Authority: NspawnRuntime/Pool. Invariant: uncertain or foreign runtime identity
must retain capacity and never stop another service. Unit tests inject command
metadata to prove decisions, not Linux namespace or cgroup enforcement.
"""
import dataclasses
import json
import os
from pathlib import Path
import socket
import tempfile
import unittest
from unittest import mock

from local_testbench_pool import Config, Lane, Pool, PoolError
from local_testbench_runtime import NspawnRuntime, Settings, check_ports, load_settings, validate_location


class FakeCommands:
    def __init__(self):
        self.units = {}
        self.calls = []
        self.fail_stop = False

    def run(self, argv, **kwargs):
        self.calls.append(argv)
        if argv[:2] == ['systemctl', 'show']:
            return self.units.get(argv[2], 'LoadState=not-found')
        if argv[:2] == ['systemctl', 'stop']:
            if self.fail_stop:
                raise PoolError('injected stop failure')
            self.units.pop(argv[2], None)
        return ''


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / 'pool'
        self.root.mkdir(mode=0o700)
        self.worktree = self.base / 'repo'
        self.worktree.mkdir()
        self.common = self.worktree / '.git'
        self.common.mkdir()
        self.lane = Lane(str(self.common), str(self.worktree))
        self.pool = Pool(Config(str(self.root), slots=1))
        self.addCleanup(self.pool.close)
        self.settings = Settings(self.pool.config, str(self.base / 'base.raw'), 'a' * 64)
        self.commands = FakeCommands()
        self.runtime = NspawnRuntime(self.pool, self.settings, self.commands)

    def owned(self):
        r = self.pool.claim(self.lane)
        self.pool.transition(self.lane, r['generation'], 'reserved', 'deploying')
        self.runtime.write_manifest(r, 'b' * 40)
        unit = self.runtime.units(r)[0]
        self.commands.units[unit] = 'LoadState=loaded\nActiveState=active\nDescription=' + self.runtime.description(r)
        return r

    def test_collision_preserves_listener(self):
        """Port-check authority rejects collisions without killing incumbent processes."""
        with socket.socket() as incumbent:
            incumbent.bind(('127.0.0.1', 0))
            incumbent.listen()
            with self.assertRaisesRegex(PoolError, 'collision'):
                check_ports([incumbent.getsockname()[1]])
            self.assertGreater(incumbent.fileno(), -1)
        self.assertEqual(self.commands.calls, [])

    def test_foreign_unit_retains_capacity_without_stop(self):
        """Runtime.inspect detects stale unit Description before Pool cleanup mutation."""
        r = self.owned()
        self.commands.units[self.runtime.units(r)[0]] = 'LoadState=loaded\nDescription=foreign'
        with self.assertRaises(PoolError):
            self.pool.stop(self.lane, r['generation'], self.runtime)
        self.assertEqual(self.pool.status(self.lane, r['generation'])['state'], 'failed')
        self.assertFalse(any(c[:2] == ['systemctl', 'stop'] for c in self.commands.calls))
        self.assertTrue((self.root / self.runtime.manifest_name(r)).exists())

    def test_interrupted_manifest_only_cleanup(self):
        """Persisted prelaunch intent is recoverable without inventing a unit owner."""
        r = self.pool.claim(self.lane)
        self.pool.transition(self.lane, r['generation'], 'reserved', 'deploying')
        self.runtime.write_manifest(r, 'b' * 40)
        self.assertEqual(self.pool.recover(self.runtime, restart=True), [{'slot': 1, 'state': 'inactive'}])
        self.assertFalse((self.root / self.runtime.manifest_name(r)).exists())

    def test_cleanup_failure_preserves_manifest_and_capacity(self):
        """Pool/NspawnRuntime stop failures cannot erase recovery evidence or free a slot."""
        r = self.owned()
        self.commands.fail_stop = True
        with self.assertRaises(PoolError):
            self.pool.stop(self.lane, r['generation'], self.runtime)
        self.assertEqual(self.pool.status(self.lane, r['generation'])['state'], 'failed')
        self.assertTrue((self.root / self.runtime.manifest_name(r)).exists())
        self.commands.fail_stop = False
        self.assertEqual(self.pool.stop(self.lane, r['generation'], self.runtime)['state'], 'inactive')

    def test_untracked_image_is_unknown(self):
        """Absent manifests cannot authorize deletion of leftover or foreign image bytes."""
        r = self.pool.claim(self.lane)
        image = self.root / (self.runtime.name(r) + '.raw')
        image.write_bytes(b'unknown')
        self.assertEqual(self.runtime.inspect(r), 'unknown')
        with self.assertRaises(PoolError):
            self.pool.stop(self.lane, r['generation'], self.runtime)
        self.assertEqual(image.read_bytes(), b'unknown')

    def test_generation_manifest_mismatch_is_unknown(self):
        """Runtime manifest identity independently rejects stale generation substitution."""
        r = self.owned()
        path = self.root / self.runtime.manifest_name(r)
        data = json.loads(path.read_text())
        data['identity']['generation'] = '0' * 64
        path.write_text(json.dumps(data))
        self.assertEqual(self.runtime.inspect(r), 'unknown')
        with self.assertRaises(PoolError):
            self.runtime.stop(r)
        self.assertFalse(any(c[:2] == ['systemctl', 'stop'] for c in self.commands.calls))

    def test_lifecycle_lock_excludes_second_builder(self):
        """Runtime lifecycle lock excludes reaping and competing builds, preventing launch-after-cleanup."""
        with self.runtime.exclusive():
            with self.assertRaisesRegex(PoolError, 'active'):
                with self.runtime.exclusive():
                    self.fail('second lifecycle operation admitted')

    def test_limits_are_explicit_and_no_host_service_target(self):
        """Command constructor binds descendant limits to a generation-specific transient unit."""
        r = self.pool.claim(self.lane)
        args = self.runtime.start_args(r, self.runtime.units(r)[0])
        for value in ('--property=CPUQuota=180%', '--property=MemoryMax=4032M',
                      '--property=MemorySwapMax=0', '--property=TasksMax=496',
                      '--property=KillMode=control-group', '--property=StandardOutput=null'):
            self.assertIn(value, args)
        self.assertNotIn('swarm.service', ' '.join(args))
        self.assertIn(r['generation'], ' '.join(args))

    def test_config_never_executes_shell_and_rejects_unknown_local_key(self):
        """Data-only load_settings rejects expansion/duplicate/unknown local settings, preserving remote namespace."""
        path = self.base / 'settings.env'
        prefix = 'SWARM_LOCAL_TESTBENCH_'
        valid = (f'{prefix}ROOT={self.root}\n{prefix}BASE_IMAGE={self.base}/base.raw\n'
                 f'{prefix}BASE_SHA256={"a" * 64}\nREMOTE_SETTING=$(ignored)\n')
        path.write_text(valid)
        self.assertEqual(load_settings(path).pool.root, str(self.root))
        for extra in ('ROOT=$(id)', 'ROOT=/duplicate', 'SHELL=anything'):
            path.write_text(valid + prefix + extra + '\n')
            with self.assertRaises(PoolError):
                load_settings(path)

    def test_host_storage_and_repository_overlap_rejected(self):
        """Location authority rejects storage/repository aliasing before any runtime mutation."""
        bad = dataclasses.replace(self.settings, pool=dataclasses.replace(self.pool.config, root=str(self.worktree)))
        with self.assertRaises(PoolError):
            validate_location(bad, self.lane)
        with mock.patch.dict(os.environ, {'SWARMD_DATA_DIR': str(self.root)}):
            with self.assertRaises(PoolError):
                validate_location(self.settings, self.lane)
        self.assertEqual(self.commands.calls, [])

    def test_failed_build_retains_recoverable_generation(self):
        """Deployment deadline cannot mark failed candidate ready or free owned capacity."""
        with mock.patch.object(self.runtime, 'doctor', return_value={}), \
             mock.patch.object(self.runtime, 'materialize'), \
             mock.patch('local_testbench_runtime.check_ports'), \
             mock.patch.object(self.runtime, 'stop', side_effect=PoolError('cleanup failed')), \
             mock.patch.object(self.runtime, 'wait_ready', side_effect=PoolError('deadline')):
            with self.assertRaisesRegex(PoolError, 'deadline'):
                self.runtime.deploy(self.lane, 'b' * 40)
        record = json.loads((self.root / 'state.json').read_text())['records'][0]
        self.assertEqual(record['state'], 'failed')
        self.assertTrue((self.root / self.runtime.manifest_name(record)).exists())
        self.assertFalse(any('systemd-socket-activate' in call for call in self.commands.calls))

    def test_successful_adapter_deployment_requires_owned_units(self):
        """Only verified build completion plus all owned active units admits ready."""
        original = self.commands.run
        def run(argv, **kwargs):
            result = original(argv, **kwargs)
            if argv[0] == 'systemd-run':
                unit = next(v.split('=', 1)[1] for v in argv if v.startswith('--unit='))
                description = next(v.split('=', 1)[1] for v in argv if v.startswith('--description='))
                self.commands.units[unit] = 'LoadState=loaded\nActiveState=active\nDescription=' + description
            return result
        with mock.patch.object(self.commands, 'run', side_effect=run), \
             mock.patch.object(self.runtime, 'doctor', return_value={}), \
             mock.patch.object(self.runtime, 'materialize'), \
             mock.patch('local_testbench_runtime.check_ports'), \
             mock.patch.object(self.runtime, 'wait_ready'):
            result = self.runtime.deploy(self.lane, 'b' * 40)
        self.assertEqual(result['state'], 'ready')
        self.assertEqual(result['head'], 'b' * 40)
        self.assertEqual(result['provider_egress'], 'disabled')
        self.assertEqual(result['authentication'], 'not configured')
        self.assertEqual(len(self.commands.units), 3)

    def test_image_copy_deadline_precedes_reads_or_writes(self):
        """Materialization authority rejects expired work without consuming source or changing target."""
        record = self.pool.claim(self.lane)
        source, target = mock.Mock(), mock.Mock()
        with mock.patch('local_testbench_runtime.time.monotonic', return_value=20):
            with self.assertRaisesRegex(PoolError, 'deadline'):
                self.runtime.copy_image(source, target, record, self.lane, 10)
        source.read.assert_not_called()
        target.write.assert_not_called()

    def test_image_copy_rejects_truncated_input_and_renews_lease(self):
        """A truncated image cannot finish materialization; copying renews the owning generation only."""
        import io
        record = self.pool.claim(self.lane)
        target = mock.Mock()
        with mock.patch.object(self.pool, 'touch', wraps=self.pool.touch) as touch, \
             mock.patch('local_testbench_runtime.time.monotonic', return_value=1):
            with self.assertRaisesRegex(PoolError, 'size or digest'):
                self.runtime.copy_image(io.BytesIO(b'short'), target, record, self.lane, 10)
        touch.assert_called_once_with(self.lane, record['generation'])
        target.flush.assert_not_called()
        self.assertEqual(self.pool.status(self.lane, record['generation'])['state'], 'reserved')

    def test_supervisor_skips_busy_deployment_and_bounds_sweep(self):
        """Automatic cleanup must not race an active lifecycle operation or spin."""
        event = mock.Mock()
        event.is_set.side_effect = [False, True]
        with self.runtime.exclusive(), mock.patch.object(self.pool, 'recover') as recover:
            self.runtime.supervise(event)
        recover.assert_not_called()
        event.wait.assert_called_once_with(300)

    def test_supervisor_recovers_expired_owned_records(self):
        """Periodic supervisor calls only durable pool recovery, not process discovery."""
        record = self.pool.claim(self.lane)
        event = mock.Mock()
        event.is_set.side_effect = [False, True]
        with mock.patch('local_testbench_pool.time.time', return_value=record['expires_at'] + 1):
            self.runtime.supervise(event)
        self.assertEqual(self.pool.status(self.lane, record['generation'])['state'], 'inactive')
        self.assertFalse(any(c[:2] == ['systemctl', 'stop'] for c in self.commands.calls))

    def test_proxy_is_loopback_and_generation_socket_only(self):
        """No namespace PID lookup or host-network guest exposure is needed for the socket relay."""
        record = self.pool.claim(self.lane)
        argv = self.runtime.proxy_args(record, 0)
        self.assertIn('--listen=127.0.0.1:18080', argv)
        self.assertTrue(argv[-1].endswith(record['generation'][:24] + '.exchange/api.sock'))
        self.assertNotIn('nsenter', argv)
        for value in ('--property=CPUQuota=10%', '--property=MemoryMax=32M', '--property=TasksMax=8'):
            self.assertIn(value, argv)

    def test_config_symlink_and_oversize_are_rejected(self):
        """Config reads must be bounded and refuse symlink substitution before allocation."""
        target = self.base / 'config'
        target.write_bytes(b'x' * 65537)
        with self.assertRaisesRegex(PoolError, 'oversized'):
            load_settings(target)
        link = self.base / 'config-link'
        link.symlink_to(target)
        with self.assertRaises(OSError):
            load_settings(link)


if __name__ == '__main__':
    unittest.main()
