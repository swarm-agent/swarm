#!/usr/bin/env python3
"""Opt-in runner-only Codex extension; the existing supervisor stays unchanged."""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import time

import local_testbench_runtime as runtime
from local_testbench_pool import Pool, PoolError


class CodexCommands(runtime.Commands):
    def __init__(self, socket):
        self.socket = socket

    def run(self, argv, **kwargs):
        if 'systemd-nspawn' in argv:
            index = argv.index('/bin/bash')
            argv = argv[:index] + ['--bind-ro=' + self.socket + ':/run/testbench-codex.sock:idmap'] + argv[index:]
        return super().run(argv, **kwargs)


class CodexRuntime(runtime.NspawnRuntime):
    def materialize(self, record, lane, head):
        # Preserve the base runtime's exact Commands type check and RLIMIT_FSIZE
        # source-bundle guard; injection applies only to the nspawn start command.
        commands = self.commands
        self.commands = runtime.Commands()
        try:
            return super().materialize(record, lane, head)
        finally:
            self.commands = commands

    def write_manifest(self, record, head):
        # Same immutable creation-before-side-effects ordering as base runtime.
        fd = self.pool._open(self.manifest_name(record), os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        with os.fdopen(fd, 'w') as stream:
            json.dump({'identity': self.identity(record), 'head': head,
                       'image_sha256': self.settings.digest,
                       'provider_profile': 'dedicated-codex-luna-medium-v1'}, stream)
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(self.pool.fd)


def broker_socket(path):
    path = Path(path)
    if not path.is_absolute() or path.resolve(strict=True) != path:
        raise PoolError('explicit canonical broker socket required')
    info = path.lstat()
    if not stat.S_ISSOCK(info.st_mode):
        raise PoolError('broker endpoint must be a UNIX socket')
    # Only trusted provisioning can expose a lane's socket. Host parent must
    # remain root-owned and non-writable; guest gets this one endpoint only.
    for parent in path.parents:
        info = parent.stat()
        if info.st_uid != 0 or info.st_mode & 0o022:
            raise PoolError('broker socket ancestors must be root-owned and non-writable')
    return str(path)


def guest():
    text = runtime.GUEST
    anchor = 'CGO_ENABLED=1 go build -p 2 -trimpath -o /out/swarmd ./cmd/swarmd'
    if text.count(anchor) != 1:
        raise PoolError('guest build anchor changed')
    text = text.replace(anchor, 'python3 /candidate/source/scripts/testbench-codex-overlay.py --source /candidate/source --output /out\nCGO_ENABLED=1 go build -overlay /out/codex-overlay.json -p 2 -trimpath -o /out/swarmd ./cmd/swarmd')
    # No startup/provider output reaches host exchange or daemon logs.
    text = text.replace('2>/exchange/startup-error', '>/dev/null 2>&1')
    return text


def inspect_ready(pool, rt, lane, generation, head):
    record = pool.status(lane, generation)
    manifest = rt.read_manifest(record)
    if record['state'] != 'ready' or rt.inspect(record) != 'owned' or not manifest or manifest['head'] != head or manifest.get('provider_profile') != 'dedicated-codex-luna-medium-v1':
        raise PoolError('owned exact-HEAD ready candidate required')
    for unit in rt.units(record):
        if rt.show(unit).get('ActiveState') != 'active':
            raise PoolError('candidate endpoint is inactive')
    return dict(record, head=head, api_url='http://127.0.0.1:' + str(rt.ports(record)[0]),
                desktop_url='http://127.0.0.1:' + str(rt.ports(record)[1]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['deploy', 'check', 'run'])
    parser.add_argument('--env-file', required=True)
    parser.add_argument('--worktree', required=True)
    parser.add_argument('--socket')
    parser.add_argument('--generation')
    raw = sys.argv[1:]
    split = raw.index('--') if '--' in raw else len(raw)
    args = parser.parse_args(raw[:split])
    args.command = raw[split + 1:]
    settings = runtime.load_settings(args.env_file)
    commands = runtime.Commands()
    lane, head = runtime.git_identity(args.worktree, commands, clean=True)
    runtime.validate_location(settings, lane)
    with Pool(settings.pool) as pool:
        if args.action == 'deploy':
            if not args.socket:
                raise PoolError('dedicated per-lane broker socket required')
            socket = broker_socket(args.socket)
            runtime.GUEST = guest()
            rt = CodexRuntime(pool, settings, CodexCommands(socket))
            record = rt.deploy(lane, head)
            record['authentication'] = 'dedicated-broker (login readiness not verified)'
            record['provider_egress'] = 'broker-only'
            print(json.dumps(record))
            return
        if not args.generation:
            raise PoolError('exact generation required; no automatic lane replacement')
        rt = runtime.NspawnRuntime(pool, settings, commands)
        with rt.exclusive():
            result = inspect_ready(pool, rt, lane, args.generation, head)
            pool.touch(lane, args.generation)
        if args.action == 'check':
            print(json.dumps(result))
            return
        argv = args.command
        if argv and argv[0] == '--':
            argv = argv[1:]
        if not argv:
            raise PoolError('runner argv required')
        env = dict(os.environ, SWARM_DESKTOP_URL=result['desktop_url'],
                   SWARM_PRIMARY_API_URL=result['api_url'], SWARM_RUNNER_API_URL=result['desktop_url'])
        argv = [result['desktop_url'] if v == '__SWARM_DESKTOP_URL__' else v for v in argv]
        # Trusted explicit sudo invocation owns pool metadata, but arbitrary
        # scenario commands drop all root/supplementary privileges first.
        import pwd
        import signal
        uid = int(os.environ.get('SUDO_UID', '-1'))
        if os.geteuid() != 0 or uid <= 0:
            raise PoolError('explicit sudo invocation by an unprivileged runner user required')
        user = pwd.getpwuid(uid)
        env = {k: v for k, v in env.items() if k.startswith('SWARM_') or k in {'PATH', 'TMPDIR'}}
        env.update(HOME=user.pw_dir, USER=user.pw_name, LOGNAME=user.pw_name)
        child = subprocess.Popen(argv, env=env, user=uid, group=user.pw_gid, extra_groups=[], start_new_session=True)
        try:
            deadline = time.monotonic() + 600
            while child.poll() is None:
                if time.monotonic() >= deadline:
                    os.killpg(child.pid, signal.SIGTERM)
                    raise PoolError('runner deadline exceeded')
                pool.touch(lane, args.generation)
                print('local runner: active', file=sys.stderr, flush=True)
                time.sleep(10)
            if child.returncode:
                raise PoolError('runner failed')
        finally:
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGTERM)
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait()


if __name__ == '__main__':
    try:
        main()
    except (PoolError, OSError, ValueError) as exc:
        print('local Codex testbench: ' + str(exc), file=sys.stderr)
        sys.exit(1)
