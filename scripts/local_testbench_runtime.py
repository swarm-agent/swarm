"""Local nspawn adapter. No credential reuse, installation, or implicit downloads.

The effective UID/private Pool root authenticates management. This executable is
trusted host tooling, not an RPC server and not a sudo entrypoint for untrusted
users. Candidate commands run only inside a private-network, user-namespaced
nspawn guest. Runtime manifests complement (never replace) Pool allocation CAS.
"""
from __future__ import annotations

import argparse
import contextlib
import dataclasses
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import stat
import subprocess
import sys
import time

from local_testbench_pool import Config, Lane, Pool, PoolError

PREFIX = 'SWARM_LOCAL_TESTBENCH_'
TOOLS = ('git', 'systemd-run', 'systemctl', 'systemd-nspawn', 'systemd-socket-activate',
         'mount', 'umount')
PROXY_HELPER = '/usr/lib/systemd/systemd-socket-proxyd'
# No source checkout, home, database, or host manager socket is mounted.
# Build/install is deliberately in the guest; dependency caches must be in base.
GUEST = r'''set -euo pipefail
phase() { current_phase=$1; printf '%s\n' "$1" > /exchange/phase; }
trap 'printf "failed-%s\n" "$current_phase" > /exchange/phase' ERR
phase source
export TMPDIR=/var/tmp
export HOME=/root
export GOMAXPROCS=2 GOROOT=/opt/go
export GOPROXY=off GOTOOLCHAIN=local
export SWARMD_DATA_DIR=/var/lib/swarmd SWARMD_CACHE_DIR=/var/cache/swarmd
export SWARMD_RUNTIME_DIR=/run/swarmd SWARMD_CONFIG_DIR=/etc/swarmd
export SWARMD_LOG_DIR=/var/log/swarmd
mkdir -p /candidate /out /run/swarmd /etc/swarmd /var/lib/swarmd /var/cache/swarmd /var/log/swarmd
chmod 700 /run/swarmd /etc/swarmd /var/lib/swarmd /var/cache/swarmd
cd /candidate
git -c core.hooksPath=/dev/null clone /input/source.bundle /candidate/source
cd /candidate/source
git -c core.hooksPath=/dev/null checkout --detach "$CANDIDATE_HEAD"
test "$(git rev-parse HEAD)" = "$CANDIDATE_HEAD"
cd /candidate/source/swarmd
phase go-build
CGO_ENABLED=1 go build -p 2 -trimpath -o /out/swarmd ./cmd/swarmd
cp internal/fff/lib/linux-amd64-gnu/libfff_c.so /out/
cd /candidate/source/web
phase web-install
cmp pnpm-lock.yaml /cache-manifests/web/pnpm-lock.yaml
cmp package.json /cache-manifests/web/package.json
cmp pnpm-workspace.yaml /cache-manifests/web/pnpm-workspace.yaml
cp -a /cache-manifests/web/node_modules ./node_modules
phase web-build
export RAYON_NUM_THREADS=2 NODE_OPTIONS=--max-old-space-size=3072
pnpm run build
export LD_LIBRARY_PATH=/out SWARM_WEB_DIST_DIR=/candidate/source/web/dist
phase daemon-start
socat UNIX-LISTEN:/exchange/api.sock,fork,mode=0600 TCP:127.0.0.1:7881 &
socat UNIX-LISTEN:/exchange/desktop.sock,fork,mode=0600 TCP:127.0.0.1:5655 &
id swarm >/dev/null
mkdir -p /var/lib/swarm
usermod -d /var/lib/swarm swarm
chown swarm:swarm /var/lib/swarm
chown -R swarm:swarm /candidate /out /run/swarmd /etc/swarmd /var/lib/swarmd /var/cache/swarmd /var/log/swarmd
runuser -u swarm -- env HOME=/var/lib/swarm LD_LIBRARY_PATH=/out SWARM_WEB_DIST_DIR=/candidate/source/web/dist /out/swarmd --listen 127.0.0.1:7881 --desktop-port 5655 --cwd /candidate/source 2>/exchange/startup-error
'''


class Commands:
    """Bounded, scrubbed subprocesses; no candidate output retained on host."""
    def run(self, argv, *, timeout=15, output=False, stdout=None):
        env = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LC_ALL': 'C',
               'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}
        # Output-producing commands here are metadata only; bound output on disk
        # through a pipe reader rather than retaining arbitrary stderr/provider data.
        with subprocess.Popen(argv, env=env, stdin=subprocess.DEVNULL,
                              stdout=subprocess.PIPE if output else (stdout or subprocess.DEVNULL),
                              stderr=subprocess.DEVNULL, start_new_session=True) as child:
            try:
                if output:
                    import selectors
                    data = bytearray()
                    with selectors.DefaultSelector() as selector:
                        selector.register(child.stdout, selectors.EVENT_READ)
                        deadline = time.monotonic() + timeout
                        while selector.get_map():
                            if time.monotonic() >= deadline:
                                raise PoolError('command deadline exceeded')
                            for key, _ in selector.select(min(0.2, max(0, deadline - time.monotonic()))):
                                chunk = os.read(key.fileobj.fileno(), 8192)
                                if not chunk:
                                    selector.unregister(key.fileobj)
                                data.extend(chunk)
                                if len(data) > 65536:
                                    raise PoolError('command output limit exceeded')
                    child.wait(timeout=max(0.01, deadline - time.monotonic()))
                else:
                    child.wait(timeout=timeout)
                if child.returncode:
                    raise PoolError('command failed: ' + Path(argv[0]).name)
                return bytes(data).decode('utf-8').strip() if output else ''
            except BaseException:
                import signal
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait(timeout=5)
                raise


@dataclasses.dataclass(frozen=True)
class Settings:
    pool: Config
    image: str
    digest: str
    port_base: int = 18080
    deadline: int = 600

    def __post_init__(self):
        if not re.fullmatch('[0-9a-f]{64}', self.digest):
            raise PoolError('explicit base SHA256 required')
        if not Path(self.image).is_absolute() or Path(self.image).resolve() != Path(self.image):
            raise PoolError('base image must be an absolute canonical path')
        if not 10000 <= self.port_base <= 65000 - self.pool.slots * 2:
            raise PoolError('invalid high loopback port range')
        if not 30 <= self.deadline <= 600:
            raise PoolError('deployment deadline must be 30..600 seconds')


def load_settings(path):
    """Strict data-only local namespace; unrelated remote .env keys are ignored."""
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise PoolError('configuration must be a regular file')
        raw = stream.read(65537)
    if len(raw) > 65536:
        raise PoolError('oversized configuration')
    values = {}
    fields = {f.name.upper(): f.name for f in dataclasses.fields(Config)}
    allowed = set(fields) | {'BASE_IMAGE', 'BASE_SHA256', 'PORT_BASE', 'DEADLINE'}
    for line in raw.decode('utf-8').splitlines():
        line = line.strip()
        if not line or line.startswith('#'):
            continue
        key, sep, value = line.partition('=')
        if not key.startswith(PREFIX):
            continue
        key = key[len(PREFIX):]
        if not sep or key not in allowed or key in values:
            raise PoolError('invalid or duplicate local config key')
        # No expansion, quotes, multiline values, commands, or shell syntax.
        if not value or re.search(r'[\s\x00\x24`\'";|&<>\\]', value):
            raise PoolError('local values must be literal unquoted data')
        values[key] = value
    try:
        args = {fields[k]: (v if k == 'ROOT' else float(v) if k in {'LOCK_TIMEOUT', 'QUEUE_TIMEOUT'} else int(v))
                for k, v in values.items() if k in fields}
        return Settings(Config(**args), values['BASE_IMAGE'], values['BASE_SHA256'],
                        int(values.get('PORT_BASE', 18080)), int(values.get('DEADLINE', 600)))
    except (KeyError, TypeError, ValueError) as exc:
        raise PoolError('missing or invalid local configuration') from exc


def git_identity(worktree, commands, *, clean=False):
    worktree = str(Path(worktree).resolve(strict=True))
    def git(*args):
        return commands.run(['git', '-c', 'safe.directory=' + worktree, '-C', worktree, *args], output=True)
    top = git('rev-parse', '--show-toplevel')
    if top != worktree:
        raise PoolError('repository worktree root required')
    common = str(Path(git('rev-parse', '--path-format=absolute', '--git-common-dir')).resolve(strict=True))
    head = git('rev-parse', '--verify', 'HEAD^{commit}')
    if not re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', head):
        raise PoolError('full committed HEAD required')
    if clean and git('status', '--porcelain=v1', '--untracked-files=normal'):
        raise PoolError('clean committed worktree required')
    return Lane(common, worktree), head


def validate_location(settings, lane):
    root = Path(settings.pool.root)
    if root.resolve(strict=True) != root:
        raise PoolError('canonical pool root required')
    forbidden = [Path(lane.worktree), Path(lane.common_git_dir), Path('/etc/swarmd'),
                 Path('/var/lib/swarmd'), Path('/var/cache/swarmd'), Path('/run/swarmd'),
                 Path('/var/log/swarmd')]
    for key in ('DATA', 'CACHE', 'RUNTIME', 'CONFIG', 'LOG'):
        value = os.environ.get('SWARMD_' + key + '_DIR')
        if value:
            forbidden.append(Path(value).resolve())
    if any(root == p or root in p.parents or p in root.parents for p in forbidden):
        raise PoolError('pool must be separate from repository and host Swarm storage')
    if Path(settings.image) == root or root in Path(settings.image).parents:
        raise PoolError('base must be outside mutable pool')


def check_ports(ports):
    sockets = []
    try:
        for port in ports:
            sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            sockets.append(sock)
            sock.bind(('127.0.0.1', port))
    except OSError as exc:
        raise PoolError('loopback port collision; no owner will be stopped') from exc
    finally:
        for sock in sockets:
            sock.close()


class NspawnRuntime:
    def __init__(self, pool, settings, commands=None):
        self.pool, self.settings = pool, settings
        self.commands = commands or Commands()

    @contextlib.contextmanager
    def exclusive(self):
        # All CLI mutations share this lock; never acquire it in inspect/stop
        # callbacks, which Pool invokes while holding its own lock.
        fd = self.pool._open('runtime.lock', os.O_RDWR | os.O_CREAT)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise PoolError('another lifecycle operation/build is active') from exc
            yield
        finally:
            os.close(fd)

    def name(self, record):
        return 'slt-' + str(record['uid']) + '-' + record['generation'][:24]

    def manifest_name(self, record):
        return self.name(record) + '.json'

    def identity(self, record):
        return {k: record[k] for k in ('uid', 'lane', 'generation', 'slot')}

    def read_manifest(self, record):
        try:
            fd = self.pool._open(self.manifest_name(record), os.O_RDONLY)
        except FileNotFoundError:
            return None
        with os.fdopen(fd, 'rb') as stream:
            raw = stream.read(8193)
        if len(raw) > 8192:
            raise PoolError('oversized runtime manifest')
        data = json.loads(raw)
        if data.get('identity') != self.identity(record):
            raise PoolError('runtime manifest ownership mismatch')
        return data

    def write_manifest(self, record, head):
        # Creation is immutable and durable BEFORE any image or unit side effect.
        fd = self.pool._open(self.manifest_name(record), os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        with os.fdopen(fd, 'w') as stream:
            json.dump({'identity': self.identity(record), 'head': head,
                       'image_sha256': self.settings.digest}, stream)
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(self.pool.fd)

    def show(self, unit):
        text = self.commands.run(['systemctl', 'show', unit, '--no-pager',
                                  '--property=LoadState,ActiveState,Description,MainPID,ControlGroup'], output=True)
        return dict(line.split('=', 1) for line in text.splitlines() if '=' in line)

    def description(self, record):
        return 'swarm-local:' + record['lane'] + ':' + record['generation'] + ':' + str(record['uid'])

    def units(self, record):
        name = self.name(record)
        return [name + suffix + '.service' for suffix in ('', '-api', '-desktop')]

    def unit_state(self, record, unit):
        values = self.show(unit)
        if values.get('LoadState') == 'not-found':
            return 'absent'
        if values.get('Description') != self.description(record):
            return 'unknown'
        return 'owned'

    def inspect(self, record):
        try:
            manifest = self.read_manifest(record)
            states = [self.unit_state(record, unit) for unit in self.units(record)]
            files = [self.name(record) + suffix for suffix in ('.raw', '.bundle')]
            files.append(self.name(record) + '.exchange')
            present = any(os.path.lexists(Path(self.pool.config.root) / f) for f in files)
            if manifest is None:
                return 'absent' if not present and all(s == 'absent' for s in states) else 'unknown'
            if 'unknown' in states:
                return 'unknown'
            return 'owned'
        except (OSError, ValueError, PoolError):
            return 'unknown'

    def stop(self, record):
        if self.inspect(record) != 'owned':
            raise PoolError('exact runtime ownership required')
        for unit in reversed(self.units(record)):
            observed = self.unit_state(record, unit)
            if observed == 'unknown':
                raise PoolError('unit identity changed; cleanup refused')
            if observed == 'owned':
                self.commands.run(['systemctl', 'stop', unit], timeout=20)
                values = self.show(unit)
                if values.get('ActiveState') not in {'inactive', 'failed'} and values.get('LoadState') != 'not-found':
                    raise PoolError('unit still active')
                if values.get('LoadState') != 'not-found':
                    # Do not swallow reset failure or guess that a loaded resource vanished.
                    self.commands.run(['systemctl', 'reset-failed', unit])
        if any(self.unit_state(record, u) != 'absent' for u in self.units(record)):
            raise PoolError('unit collection pending; retry cleanup')
        for suffix in ('.raw', '.bundle'):
            name = self.name(record) + suffix
            try:
                fd = self.pool._open(name, os.O_RDONLY)
            except FileNotFoundError:
                continue
            os.close(fd)
            os.unlink(name, dir_fd=self.pool.fd)
        exchange = self.name(record) + '.exchange'
        try:
            directory = os.open(exchange, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=self.pool.fd)
        except FileNotFoundError:
            directory = None
        if directory is not None:
            try:
                # Guest can create only bounded exchange entries. Never recursively
                # follow or remove unrecognized content supplied by a candidate.
                names = os.listdir(directory)
                if set(names) - {'api.sock', 'desktop.sock', 'phase', 'startup-error'}:
                    raise PoolError('unknown exchange content; cleanup retained')
                for entry in names:
                    if not (stat.S_ISSOCK(os.stat(entry, dir_fd=directory, follow_symlinks=False).st_mode) or (entry in {'phase', 'startup-error'} and stat.S_ISREG(os.stat(entry, dir_fd=directory, follow_symlinks=False).st_mode))):
                        raise PoolError('unexpected exchange entry type')
                    os.unlink(entry, dir_fd=directory)
            finally:
                os.close(directory)
            path = str(Path(self.pool.config.root) / exchange)
            if os.path.ismount(path):
                self.commands.run(['umount', '--', path])
            os.rmdir(exchange, dir_fd=self.pool.fd)
        os.unlink(self.manifest_name(record), dir_fd=self.pool.fd)
        os.fsync(self.pool.fd)

    def doctor(self, lane):
        validate_location(self.settings, lane)
        if len(os.fsencode(str(Path(self.pool.config.root) / ('slt-0000000000-' + 'a' * 24 + '.exchange/desktop.sock')))) >= 108:
            raise PoolError('pool root is too long for UNIX endpoint paths')
        missing = [tool for tool in TOOLS if not shutil.which(tool)]
        if not os.access(PROXY_HELPER, os.X_OK):
            missing.append(PROXY_HELPER)
        if missing:
            raise PoolError('missing prerequisites: ' + ', '.join(missing))
        if self.pool.config.tasks <= 16:
            raise PoolError('slot task budget must exceed proxy reserve of 16')
        if os.geteuid() != 0:
            raise PoolError('requires explicitly provisioned root-owned pool and trusted root invocation; no automatic sudo')
        if not Path('/sys/fs/cgroup/cgroup.controllers').exists():
            raise PoolError('unified cgroup v2 required')
        if self.pool.config.total_cpus >= (os.cpu_count() or 1):
            raise PoolError('CPU budget must reserve at least one host CPU')
        memory = os.sysconf('SC_PHYS_PAGES') * os.sysconf('SC_PAGE_SIZE') // 1048576
        if self.pool.config.total_memory_mb > memory - 2048:
            raise PoolError('memory budget must reserve 2048 MiB for host')
        if shutil.disk_usage(self.pool.config.root).free < (self.pool.config.total_disk_mb + 2048) * 1048576:
            raise PoolError('insufficient disk budget plus host reserve')
        image = Path(self.settings.image)
        info = image.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise PoolError('base must be a root-owned non-writable regular filesystem image')
        if info.st_size != self.pool.config.disk_mb * 1048576:
            raise PoolError('base image size must equal configured bounded slot disk size')
        with image.open('rb') as stream:
            digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        if digest != self.settings.digest:
            raise PoolError('base image SHA256 mismatch')
        return {'prerequisites': 'checked', 'authentication': 'not configured', 'live_validation': 'not performed'}

    def start_args(self, record, unit):
        c = self.pool.config
        return ['systemd-run', '--quiet', '--collect', '--unit=' + unit,
                '--description=' + self.description(record), '--service-type=exec',
                '--property=CPUQuota=' + str(c.cpus * 100 - 20) + '%',
                '--property=MemoryMax=' + str(c.memory_mb - 64) + 'M',
                '--property=MemorySwapMax=0', '--property=TasksMax=' + str(max(1, c.tasks - 16)),
                '--property=KillMode=control-group', '--property=TimeoutStopSec=15s',
                '--property=RuntimeMaxSec=86400s',
                '--property=StandardOutput=null', '--property=StandardError=null']

    def copy_image(self, source, target, record, lane, deadline):
        """Bound image bytes and elapsed work; renew the durable deployment lease."""
        limit = self.pool.config.disk_mb * 1048576
        copied = 0
        digest = hashlib.sha256()
        next_touch = 0
        while True:
            now = time.monotonic()
            if now >= deadline:
                raise PoolError('image materialization deadline exceeded')
            if now >= next_touch:
                self.pool.touch(lane, record['generation'])
                print('local testbench: materializing candidate image', file=sys.stderr, flush=True)
                next_touch = now + 10
            block = source.read(min(1048576, limit - copied + 1))
            if not block:
                break
            copied += len(block)
            if copied > limit:
                raise PoolError('base image exceeds slot disk bound')
            digest.update(block)
            target.write(block)
        if copied != limit or digest.hexdigest() != self.settings.digest:
            raise PoolError('copied image size or digest mismatch')
        target.flush()
        os.fsync(target.fileno())

    def materialize(self, record, lane, head):
        deadline = time.monotonic() + self.settings.deadline
        name = self.name(record)
        # Bundle exact committed objects; never extract candidate paths on host.
        fd = self.pool._open(name + '.bundle', os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        try:
            # Git bundle output is capped by RLIMIT_FSIZE in a dedicated child.
            # subprocess preexec is safe here: CLI is deliberately single-threaded.
            import resource
            limit = min(512 * 1048576, self.pool.config.disk_mb * 1048576 // 8)
            def cap():
                resource.setrlimit(resource.RLIMIT_FSIZE, (limit, limit))
            if type(self.commands) is Commands:
                with os.fdopen(os.dup(fd), 'wb') as target:
                    result = subprocess.run(['git', '-c', 'safe.directory=' + lane.worktree, '-C', lane.worktree, 'bundle', 'create', '-', 'HEAD'],
                                            stdin=subprocess.DEVNULL, stdout=target, stderr=subprocess.DEVNULL,
                                            env={'PATH': '/usr/bin:/bin', 'GIT_CONFIG_NOSYSTEM': '1',
                                                 'GIT_CONFIG_GLOBAL': '/dev/null'}, preexec_fn=cap, timeout=60)
                    if result.returncode:
                        raise PoolError('source archive failed or exceeded bound')
            else:
                self.commands.run(['git', '-c', 'safe.directory=' + lane.worktree, '-C', lane.worktree, 'bundle', 'create', '-', 'HEAD'], stdout=fd)
            os.fsync(fd)
        finally:
            os.close(fd)
        self.commands.run(['git', '-c', 'safe.directory=' + lane.worktree, '-C', lane.worktree, 'bundle', 'verify',
                           str(Path(self.pool.config.root) / (name + '.bundle'))])
        if git_identity(lane.worktree, self.commands, clean=True) != (lane, head):
            raise PoolError('source identity changed during materialization')
        out = self.pool._open(name + '.raw', os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        try:
            source_fd = os.open(self.settings.image, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(source_fd, 'rb') as source, os.fdopen(os.dup(out), 'wb') as target:
                info = os.fstat(source.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
                    raise PoolError('unsafe base image at materialization')
                self.copy_image(source, target, record, lane, deadline)
        finally:
            os.close(out)

    def supervise(self, stop_event):
        """Foreground broker loop; an external service manager owns its lifetime.

        A busy deployment owns the lifecycle lock and is never reaped mid-launch.
        A crashed deployment releases it; expired records are then reconciled.
        No subprocesses or untracked resources are discovered by this loop.
        """
        while not stop_event.is_set():
            try:
                with self.exclusive():
                    outcomes = self.pool.recover(self, restart=False)
                    if outcomes:
                        print(json.dumps({'reaped': outcomes}), flush=True)
            except PoolError as exc:
                if str(exc) != 'another lifecycle operation/build is active':
                    raise
            stop_event.wait( min(300, max(1, self.pool.config.lease_seconds // 2)))

    def ports(self, record):
        return [self.settings.port_base + (record['slot'] - 1) * 2 + i for i in range(2)]

    def proxy_args(self, record, index):
        endpoint = ('api', 'desktop')[index]
        address = str(Path(self.pool.config.root) / (self.name(record) + '.exchange') / (endpoint + '.sock'))
        # Reserve proxy budgets inside configured slot totals, not in addition.
        args = self.start_args(record, self.units(record)[index + 1])
        args = [value for value in args if not value.startswith(('--property=CPUQuota=', '--property=MemoryMax=', '--property=TasksMax='))]
        args += ['--property=CPUQuota=10%', '--property=MemoryMax=32M', '--property=TasksMax=8']
        return args + [
            'systemd-socket-activate', '--listen=127.0.0.1:' + str(self.ports(record)[index]),
            PROXY_HELPER, '--connections-max=64', address]

    def wait_ready(self, record, lane):
        deadline = time.monotonic() + self.settings.deadline
        next_touch = 0
        while time.monotonic() < deadline:
            now = time.monotonic()
            if now >= next_touch:
                self.pool.touch(lane, record['generation'])
                phase = 'starting'
                try:
                    path = Path(self.pool.config.root) / (self.name(record) + '.exchange') / 'phase'
                    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                    with os.fdopen(fd, 'rb') as stream:
                        if stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                            value = stream.read(64).decode('ascii', errors='ignore').strip()
                            if value.removeprefix('failed-') in {'source', 'go-build', 'web-install', 'web-build', 'daemon-start', 'failed'}:
                                phase = value
                except OSError:
                    pass
                print('local testbench: phase=' + phase, file=sys.stderr, flush=True)
                if phase == 'failed' or phase.startswith('failed-'):
                    raise PoolError('guest build failed at ' + phase)
                next_touch = now + 10
            values = self.show(self.units(record)[0])
            if values.get('Description') != self.description(record) or values.get('ActiveState') != 'active':
                try:
                    with open(Path(self.pool.config.root) / (self.name(record) + '.exchange') / 'phase', 'rb') as stream:
                        last = stream.read(64).decode('ascii', errors='ignore').strip()
                    if re.fullmatch(r'(failed-)?(source|go-build|web-install|web-build|daemon-start)', last):
                        phase = last
                except OSError:
                    pass
                if phase == 'failed-daemon-start':
                    error_path = Path(self.pool.config.root) / (self.name(record) + '.exchange') / 'startup-error'
                    try:
                        fd = os.open(error_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                        with os.fdopen(fd, 'rb') as stream:
                            if stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                                diagnostic = stream.read(4096).decode('utf-8', errors='replace')
                                print('isolated unauthenticated startup diagnostic: ' + diagnostic, file=sys.stderr)
                    except OSError:
                        pass
                raise PoolError('candidate unit stopped at ' + phase + ': load=' + values.get('LoadState', 'missing') + ' active=' + values.get('ActiveState', 'missing'))
            ready = True
            for endpoint in ('api', 'desktop'):
                address = str(Path(self.pool.config.root) / (self.name(record) + '.exchange') / (endpoint + '.sock'))
                try:
                    with socket.socket(socket.AF_UNIX) as connection:
                        connection.settimeout(0.5)
                        connection.connect(address)
                        connection.sendall(b'GET /health HTTP/1.0\r\nHost: localhost\r\n\r\n')
                        response = connection.recv(1024)
                        if not response.startswith(b'HTTP/'):
                            ready = False
                except OSError:
                    ready = False
            if ready:
                return
            time.sleep(0.5)
        raise PoolError('candidate build/start deadline exceeded')

    def deploy(self, lane, head):
        self.doctor(lane)
        with self.exclusive():
            r = self.pool.claim(lane)
            generation = r['generation']
            try:
                r = self.pool.transition(lane, generation, 'reserved', 'deploying')
                self.write_manifest(r, head)
                check_ports(self.ports(r))
                self.materialize(r, lane, head)
                os.mkdir(self.name(r) + '.exchange', mode=0o700, dir_fd=self.pool.fd)
                os.fsync(self.pool.fd)
                self.commands.run(['mount', '-t', 'tmpfs', '-o', 'size=1m,nr_inodes=16,mode=0700,nosuid,nodev,noexec',
                                   self.name(r), str(Path(self.pool.config.root) / (self.name(r) + '.exchange'))])
                name = self.name(r)
                root = self.pool.config.root
                argv = self.start_args(r, self.units(r)[0]) + [
                    'systemd-nspawn', '--quiet', '--settings=no', '--register=no', '--keep-unit',
                    '--machine=' + name, '--image=' + root + '/' + name + '.raw',
                    '--private-network', '--private-users=pick', '--private-users-ownership=map',
                    '--link-journal=no', '--as-pid2', '--console=pipe',
                    '--bind-ro=' + root + '/' + name + '.bundle:/input/source.bundle:idmap',
                    '--setenv=CANDIDATE_HEAD=' + head,
                    '--bind=' + root + '/' + name + '.exchange:/exchange:idmap',
                    '/bin/bash', '-c', GUEST]
                self.commands.run(argv)
                self.wait_ready(r, lane)
                for index in range(2):
                    self.commands.run(self.proxy_args(r, index))
                    values = self.show(self.units(r)[index + 1])
                    if values.get('Description') != self.description(r) or values.get('ActiveState') != 'active':
                        raise PoolError('endpoint unit failed or identity changed')
                r = self.pool.transition(lane, generation, 'deploying', 'ready')
                return dict(r, head=head, api_url='http://127.0.0.1:' + str(self.ports(r)[0]),
                            desktop_url='http://127.0.0.1:' + str(self.ports(r)[1]),
                            authentication='not configured', provider_egress='disabled')
            except BaseException:
                try:
                    self.pool.transition(lane, generation, 'deploying', 'failed')
                except PoolError:
                    pass
                # Stop only resources whose manifest and unit identities still match.
                # A cleanup failure remains failed and retains capacity for recovery.
                try:
                    self.pool.stop(lane, generation, self)
                except PoolError:
                    pass
                raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['doctor', 'deploy', 'status', 'pool-status', 'touch', 'stop', 'reap', 'supervise'])
    parser.add_argument('--env-file', required=True)
    parser.add_argument('--worktree', default=os.getcwd())
    parser.add_argument('--generation')
    args = parser.parse_args(argv)
    try:
        settings = load_settings(args.env_file)
        commands = Commands()
        lane, head = git_identity(args.worktree, commands, clean=args.action == 'deploy')
        validate_location(settings, lane)
        with Pool(settings.pool) as pool:
            runtime = NspawnRuntime(pool, settings, commands)
            if args.action == 'doctor':
                result = runtime.doctor(lane)
            elif args.action == 'deploy':
                result = runtime.deploy(lane, head)
            elif args.action == 'supervise':
                import signal
                import threading
                stopping = threading.Event()
                signal.signal(signal.SIGTERM, lambda *_: stopping.set())
                signal.signal(signal.SIGINT, lambda *_: stopping.set())
                runtime.supervise(stopping)
                result = {'supervisor': 'stopped'}
            else:
                with runtime.exclusive():
                    if args.action == 'pool-status':
                        with pool._locked():
                            result = pool._load()
                    elif args.action == 'reap':
                        result = pool.recover(runtime, restart=False)
                    else:
                        if not args.generation:
                            raise PoolError('--generation required')
                        if args.action == 'stop':
                            result = pool.stop(lane, args.generation, runtime)
                        elif args.action == 'touch':
                            result = pool.touch(lane, args.generation)
                        else:
                            result = pool.status(lane, args.generation)
                            result['runtime'] = runtime.inspect(result)
            print(json.dumps(result, sort_keys=True))
        return 0
    except (PoolError, OSError, ValueError, subprocess.SubprocessError) as exc:
        # Errors are controlled metadata, never captured candidate/provider output.
        print('local testbench: ' + str(exc), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
