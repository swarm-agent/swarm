"""Single-UID local testbench allocation authority (no runtime implementation).

The private root and effective OS UID are the authentication boundary. Lane hashes
and generation tokens provide routing/CAS, not isolation from malicious same-UID
processes. Do not expose this object directly as an unauthenticated RPC service.
Runtime adapters must independently verify exact resource ownership, use bounded
operations, and never stop unknown resources. All adapter calls run under the pool
lock; callbacks must not reenter Pool. Git verification belongs to the adapter.
"""
from __future__ import annotations

import contextlib
import dataclasses
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import secrets
import stat
import time
from typing import Protocol


class PoolError(RuntimeError):
    pass


@dataclasses.dataclass(frozen=True)
class Config:
    root: str
    slots: int = 2
    cpus: int = 2
    memory_mb: int = 4096
    tasks: int = 512
    disk_mb: int = 16384
    total_cpus: int = 4
    total_memory_mb: int = 8192
    total_tasks: int = 1024
    total_disk_mb: int = 32768
    lease_seconds: int = 1800
    lock_timeout: float = 5
    queue_timeout: float = 0

    def __post_init__(self):
        _absolute(self.root)
        bounds = {'slots': (1, 32), 'cpus': (1, 128),
                  'memory_mb': (128, 1048576), 'tasks': (16, 65536),
                  'disk_mb': (128, 10485760), 'total_cpus': (1, 4096),
                  'total_memory_mb': (128, 33554432),
                  'total_tasks': (16, 2097152), 'total_disk_mb': (128, 335544320),
                  'lease_seconds': (1, 86400)}
        for key, (low, high) in bounds.items():
            value = getattr(self, key)
            if type(value) is not int or not low <= value <= high:
                raise PoolError('invalid config: ' + key)
        for key in ('lock_timeout', 'queue_timeout'):
            value = getattr(self, key)
            if type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= 300:
                raise PoolError('invalid config: ' + key)
        for key in ('cpus', 'memory_mb', 'tasks', 'disk_mb'):
            if self.slots * getattr(self, key) > getattr(self, 'total_' + key):
                raise PoolError('resource budget exceeded: ' + key)


def _absolute(value):
    if not isinstance(value, str) or not 1 <= len(value) <= 4096 or '\x00' in value:
        raise PoolError('invalid path')
    p = Path(value)
    if not p.is_absolute() or '..' in p.parts or str(p) != value or value == '/':
        raise PoolError('explicit normalized absolute path required')
    return p


def _directory(path, *, private=False):
    """Walk without following symlinks; return a pinned directory descriptor."""
    parts = _absolute(path).parts
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in parts[1:]:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = nxt
            if private:
                info = os.fstat(fd)
                # Trusted ancestors or sticky shared scratch are required so a
                # different UID cannot rename/replace the broker root.
                if (info.st_uid not in (0, os.geteuid())
                        or (info.st_mode & 0o022 and not info.st_mode & stat.S_ISVTX)):
                    raise PoolError('unsafe pool ancestor')
        return fd
    except BaseException:
        os.close(fd)
        raise


@dataclasses.dataclass(frozen=True)
class Lane:
    common_git_dir: str
    worktree: str

    def __post_init__(self):
        for path in (self.common_git_dir, self.worktree):
            fd = _directory(path)
            os.close(fd)

    @property
    def key(self):
        return hashlib.sha256((self.common_git_dir + '\x00' + self.worktree).encode()).hexdigest()


class Runtime(Protocol):
    def inspect(self, record: dict) -> str:
        """Return absent, owned, or unknown after verifying generation/UID/lane.

        'absent' proves no resources remain, including partial deployment output.
        All other results fail closed. Must be bounded; must not mutate resources.
        """
        ...

    def stop(self, record: dict) -> None:
        """Boundedly stop/remove only exact owned resources; raise on failure."""
        ...


class Pool:
    MAX_STATE = 131072
    STATES = {'reserved', 'deploying', 'ready', 'stopping', 'inactive', 'failed'}

    def __init__(self, config: Config):
        self.config = config
        self.uid = os.geteuid()  # Never supplied by callers or environment.
        self.fd = _directory(config.root, private=True)  # Root must be provisioned explicitly.
        info = os.fstat(self.fd)
        if info.st_uid != self.uid or stat.S_IMODE(info.st_mode) != 0o700:
            self.close()
            raise PoolError('pool root must be owned by effective UID with mode 0700')

    def close(self):
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def _open(self, name, flags):
        fd = os.open(name, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=self.fd)
        info = os.fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != self.uid
                or stat.S_IMODE(info.st_mode) != 0o600 or info.st_nlink != 1):
            os.close(fd)
            raise PoolError('unsafe pool file')
        return fd

    @contextlib.contextmanager
    def _locked(self):
        fd = self._open('pool.lock', os.O_RDWR | os.O_CREAT)
        deadline = time.monotonic() + self.config.lock_timeout
        try:
            while True:
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise PoolError('pool lock timeout')
                    time.sleep(0.02)
            yield
        finally:
            os.close(fd)

    def _load(self):
        try:
            fd = self._open('state.json', os.O_RDONLY)
        except FileNotFoundError:
            return []
        with os.fdopen(fd, 'rb') as stream:
            raw = stream.read(self.MAX_STATE + 1)
        if len(raw) > self.MAX_STATE:
            raise PoolError('oversized pool state')
        try:
            state = json.loads(raw)
            if set(state) != {'version', 'config', 'records'} or state['version'] != 1:
                raise ValueError()
            if state['config'] != dataclasses.asdict(self.config):
                raise PoolError('pool config differs from durable authority')
            records = state['records']
            if not isinstance(records, list) or len(records) > self.config.slots:
                raise ValueError()
            slots, lanes = set(), set()
            for r in records:
                if set(r) != {'slot', 'lane', 'common_git_dir', 'worktree', 'uid', 'generation', 'state', 'expires_at'}:
                    raise ValueError()
                if type(r['slot']) is not int or not 1 <= r['slot'] <= self.config.slots or r['slot'] in slots:
                    raise ValueError()
                if type(r['uid']) is not int or r['uid'] != self.uid or r['state'] not in self.STATES:
                    raise ValueError()
                for key in ('generation', 'lane'):
                    if not isinstance(r[key], str) or not re.fullmatch('[0-9a-f]{64}', r[key]):
                        raise ValueError()
                for key in ('common_git_dir', 'worktree'):
                    _absolute(r[key])
                digest = hashlib.sha256((r['common_git_dir'] + '\x00' + r['worktree']).encode()).hexdigest()
                if digest != r['lane'] or r['lane'] in lanes:
                    raise ValueError()
                if type(r['expires_at']) not in (int, float) or not math.isfinite(r['expires_at']):
                    raise ValueError()
                slots.add(r['slot'])
                lanes.add(r['lane'])
            return records
        except (ValueError, TypeError, KeyError) as exc:
            raise PoolError('invalid pool state') from exc

    def _save(self, records):
        raw = json.dumps({'version': 1, 'config': dataclasses.asdict(self.config), 'records': records}, sort_keys=True).encode()
        if len(raw) > self.MAX_STATE:
            raise PoolError('oversized pool state')
        name = '.state-' + secrets.token_hex(16)
        fd = self._open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        try:
            with os.fdopen(fd, 'wb') as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(name, 'state.json', src_dir_fd=self.fd, dst_dir_fd=self.fd)
            os.fsync(self.fd)
        finally:
            try:
                os.unlink(name, dir_fd=self.fd)
            except FileNotFoundError:
                pass

    def claim(self, lane: Lane):
        deadline = time.monotonic() + self.config.queue_timeout
        while True:
            with self._locked():
                records = self._load()
                existing = next((r for r in records if r['lane'] == lane.key), None)
                if existing and existing['state'] != 'inactive':
                    raise PoolError('lane already allocated; recover existing generation explicitly')
                free = existing or next((r for r in records if r['state'] == 'inactive'), None)
                if free or len(records) < self.config.slots:
                    slot = free['slot'] if free else next(s for s in range(1, self.config.slots + 1) if all(r['slot'] != s for r in records))
                    if free:
                        records.remove(free)
                    record = dict(slot=slot, lane=lane.key, common_git_dir=lane.common_git_dir,
                                  worktree=lane.worktree, uid=self.uid, generation=secrets.token_hex(32),
                                  state='reserved', expires_at=time.time() + self.config.lease_seconds)
                    records.append(record)
                    self._save(records)
                    return record.copy()
            if time.monotonic() >= deadline:
                raise PoolError('pool full')
            time.sleep(min(0.05, max(0, deadline - time.monotonic())))

    def _owned(self, records, lane, generation):
        for r in records:
            if (r['lane'] == lane.key and r['common_git_dir'] == lane.common_git_dir
                    and r['worktree'] == lane.worktree and r['uid'] == self.uid
                    and r['generation'] == generation):
                return r
        raise PoolError('lane/generation ownership denied')

    def status(self, lane, generation):
        with self._locked():
            return self._owned(self._load(), lane, generation).copy()

    def touch(self, lane, generation):
        with self._locked():
            records = self._load()
            r = self._owned(records, lane, generation)
            if r['state'] not in {'reserved', 'deploying', 'ready'}:
                raise PoolError('lease is not active')
            r['expires_at'] = time.time() + self.config.lease_seconds
            self._save(records)
            return r.copy()

    def transition(self, lane, generation, expected, target):
        """Runtime attests readiness only after verified deployment/health checks.

        Persist deploying BEFORE side effects. No public transition frees capacity;
        stop/recover alone can mark inactive after proving resource absence.
        """
        allowed = {('reserved', 'deploying'), ('deploying', 'ready'),
                   ('reserved', 'failed'), ('deploying', 'failed'), ('ready', 'failed')}
        with self._locked():
            records = self._load()
            r = self._owned(records, lane, generation)
            if r['state'] != expected or (expected, target) not in allowed:
                raise PoolError('invalid or stale transition')
            r['state'] = target
            self._save(records)
            return r.copy()

    def _cleanup(self, records, record, runtime):
        record['state'] = 'stopping'
        self._save(records)  # Durable intent precedes runtime mutation.
        try:
            observed = runtime.inspect(record.copy())
            if observed == 'owned':
                runtime.stop(record.copy())
                observed = runtime.inspect(record.copy())
            if observed != 'absent':
                raise PoolError('runtime ownership unknown or cleanup incomplete')
        except Exception as exc:
            record['state'] = 'failed'
            self._save(records)
            raise PoolError('cleanup failed; capacity retained') from exc
        record['state'] = 'inactive'
        self._save(records)
        return record.copy()

    def stop(self, lane, generation, runtime: Runtime):
        with self._locked():
            records = self._load()
            r = self._owned(records, lane, generation)
            return self._cleanup(records, r, runtime)

    def recover(self, runtime: Runtime, *, restart=False):
        """Broker-only reconciliation; never discovers or destroys untracked objects.

        restart=True requires exclusive runtime supervisor startup (no deployment
        worker alive): interrupted reserved/deploying/stopping records are cleaned.
        Failed records retain capacity until a later successful cleanup attempt.
        Returns per-slot outcomes without leaking exception/provider text.
        """
        outcomes = []
        with self._locked():
            records = self._load()
            for r in records:
                if r['state'] == 'inactive':
                    continue
                if (r['expires_at'] <= time.time() or r['state'] == 'failed'
                        or (restart and r['state'] in {'reserved', 'deploying', 'stopping'})):
                    try:
                        self._cleanup(records, r, runtime)
                    except PoolError:
                        pass  # Persisted failed record is the explicit outcome.
                    outcomes.append({'slot': r['slot'], 'state': r['state']})
        return outcomes
