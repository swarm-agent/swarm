#!/usr/bin/env python3
"""Bounded process supervisor; suite selection belongs to launch-prerun.sh only."""
import ctypes
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import time


def run(root, jobs, commands, wall=600, stall=120, cap=1048576, heartbeat=15):
    if not (1 <= jobs <= 8 and 1 <= wall <= 600 and 1 <= stall <= wall
            and 1024 <= cap <= 4194304 and 1 <= heartbeat <= 15):
        raise ValueError('invalid lifecycle bounds')
    if not commands or len(commands) > 32:
        raise ValueError('select 1..32 unique suites')
    names = [item['id'] for item in commands]
    if len(set(names)) != len(names) or any(not name or any(c not in 'abcdefghijklmnopqrstuvwxyz0123456789-' for c in name) for name in names):
        raise ValueError('invalid or duplicate suite ID')
    for item in commands:
        if not item['argv'] or not all(isinstance(v, str) and '\0' not in v for v in item['argv']):
            raise ValueError('invalid argv')
        grace = item.get('cleanup_seconds', 0.5)
        if isinstance(grace, bool) or not isinstance(grace, (int, float)) or not 0.5 <= grace <= 25:
            raise ValueError('invalid cleanup grace')
    # Linux subreaper retains orphaned grandchildren, including setsid descendants.
    if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
        raise OSError('cannot establish owned descendant reaping')
    os.umask(0o077)
    root = Path(root)
    if (root / 'results.json').exists() or (root / 'logs').exists():
        raise ValueError('run evidence already exists; use a fresh directory')
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    for name in ('logs', 'status'):
        (root / name).mkdir(mode=0o700)
    selector = selectors.DefaultSelector()
    active, results = {}, []
    cancelled = False
    old_handlers = {}

    def cancel(_sig, _frame):
        nonlocal cancelled
        cancelled = True

    for sig in (signal.SIGINT, signal.SIGTERM):
        old_handlers[sig] = signal.signal(sig, cancel)

    def snapshot():
        table = {}
        for entry in Path('/proc').iterdir():
            if not entry.name.isdigit():
                continue
            try:
                fields = (entry / 'stat').read_text().rsplit(')', 1)[1].split()
                table[int(entry.name)] = (int(fields[1]), fields[0], fields[19])
            except (OSError, ValueError, IndexError):
                pass
        return table

    def descendants(state, table):
        # Identity includes start ticks to avoid signalling a reused PID.
        known = state['owned']
        changed = True
        while changed:
            changed = False
            for pid, (parent, _status, ticks) in table.items():
                if parent in known and parent in table and table[parent][2] == known[parent] and pid not in known:
                    known[pid] = ticks
                    changed = True
        return [pid for pid, ticks in known.items() if pid in table and table[pid][2] == ticks and table[pid][1] != 'Z']

    def terminate(state, sig, table):
        for pid in reversed(descendants(state, table)):
            try:
                os.kill(pid, sig)
            except ProcessLookupError:
                pass
        # Covers children forked between scans that have not escaped their group.
        try:
            os.killpg(state['process'].pid, sig)
        except ProcessLookupError:
            pass

    def persist():
        counts = {key: sum(r['outcome'] == key for r in results) for key in ('pass', 'fail', 'not-run')}
        payload = {'version': 1, 'counts': counts, 'results': results}
        temp = root / 'results.pending'
        temp.write_text(json.dumps(payload, indent=2) + '\n')
        temp.replace(root / 'results.json')
        (root / 'summary.tsv').write_text(''.join(f"{r['id']}\t{r['exit_code']}\t{r['started_at']}\t{r['finished_at']}\n" for r in results))

    def finish(state, code):
        now = time.time()
        reason = state['reason'] or ('completed' if code == 0 else 'nonzero_exit')
        result = {'id': state['id'], 'outcome': 'pass' if code == 0 else 'fail', 'exit_code': code,
                  'reason': reason, 'started_at': state['started_at'], 'finished_at': now,
                  'duration_seconds': round(time.monotonic() - state['start'], 3), 'output_bytes': state['bytes'],
                  'remaining_pids': state.get('remaining_pids', [])}
        state['log'].close()
        results.append(result)
        (root / 'status' / (state['id'] + '.exit')).write_text(str(code) + '\n')
        persist()
        print(f"[{result['outcome'].upper()}] {state['id']} reason={reason} bytes={state['bytes']}", flush=True)

    queue = list(commands)
    last_heartbeat = 0
    try:
        while active or queue:
            while queue and len(active) < jobs and not cancelled:
                item = queue.pop(0)
                env = dict(os.environ)
                env['SWARM_LAUNCH_CASE_ID'] = item['id']
                env['SWARM_LAUNCH_CLEANUP_SECONDS'] = str(item.get('cleanup_seconds', 0.5))
                now = time.monotonic()
                state = {'id': item['id'], 'start': now, 'last_output': now, 'started_at': time.time(),
                         'bytes': 0, 'reason': '', 'stop_at': None, 'owned': {}, 'eof': False,
                         'cleanup_seconds': item.get('cleanup_seconds', 0.5),
                         'log': open(root / 'logs' / (item['id'] + '.log'), 'xb')}
                try:
                    p = subprocess.Popen(item['argv'], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                         stdin=subprocess.DEVNULL, start_new_session=True, env=env)
                except OSError:
                    state['reason'] = 'spawn_error'
                    finish(state, 127)
                    continue
                state['process'] = p
                table = snapshot()
                if p.pid in table:
                    state['owned'][p.pid] = table[p.pid][2]
                os.set_blocking(p.stdout.fileno(), False)
                selector.register(p.stdout, selectors.EVENT_READ, state)
                active[p.pid] = state
                print(f"[START] {item['id']}", flush=True)
            for key, _mask in selector.select(0.05):
                state = key.data
                data = os.read(key.fileobj.fileno(), 65536)
                if not data:
                    selector.unregister(key.fileobj)
                    key.fileobj.close()
                    state['eof'] = True
                else:
                    remaining = cap - state['bytes']
                    state['log'].write(data[:remaining])
                    state['log'].flush()
                    state['bytes'] += min(remaining, len(data))
                    state['last_output'] = time.monotonic()
                    if len(data) > remaining and not state['reason']:
                        state['reason'] = 'output_limit'
            now = time.monotonic()
            table = snapshot()
            # An orphan adopted before it could be attributed is still ours. Stop
            # it globally; never let a fast double-fork escape cancellation.
            for state in active.values():
                descendants(state, table)
            # Exited, attributed children still belong to their suite until reaped.
            # Excluding zombies here misclassifies ordinary child teardown as an
            # escaped double-fork and cancels unrelated concurrent suites.
            tracked = {pid for state in active.values() for pid, ticks in state['owned'].items()
                       if pid in table and table[pid][2] == ticks}
            for pid, (parent, status, _ticks) in table.items():
                if parent == os.getpid() and pid not in active and pid not in tracked:
                    # Adoption proves ownership; an inherited suite label only
                    # attributes that already-owned process (e.g. Chrome's
                    # double-forked crash handler). Never use labels to adopt
                    # arbitrary host processes. Dead/unlabelled orphans still
                    # fail closed when no prior identity was recorded.
                    try:
                        with open(f'/proc/{pid}/environ', 'rb') as environment:
                            fields = environment.read(65537)
                        labels = [field.split(b'=', 1)[1] for field in fields.split(b'\0')
                                  if field.startswith(b'SWARM_LAUNCH_CASE_ID=')]
                        matching = [state for state in active.values()
                                    if labels == [state['id'].encode()]
                                    and len(fields) <= 65536
                                    and int(_ticks) >= int(state['owned'].get(state['process'].pid, _ticks))]
                    except (OSError, ValueError):
                        matching = []
                    if len(matching) == 1:
                        matching[0]['owned'][pid] = _ticks
                        continue
                    # A dead adopted child cannot execute or retain descendants.
                    # Fast helpers can exit before the first /proc sample, making
                    # their environment unavailable. Reap only this exact child;
                    # live unattributed processes still fail every active suite.
                    if status == 'Z':
                        try:
                            os.waitpid(pid, os.WNOHANG)
                        except ChildProcessError:
                            pass
                        continue
                    # Attribution was lost during a double-fork. Fail active work
                    # conservatively instead of silently accepting an escaped task.
                    for state in active.values():
                        if not state['reason']:
                            state['reason'] = 'unattributed_descendant'
                    if status != 'Z':
                        try:
                            os.kill(pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    try:
                        os.waitpid(pid, os.WNOHANG)
                    except ChildProcessError:
                        pass
            for pid, state in list(active.items()):
                p = state['process']
                code = p.poll()
                alive = [child for child in descendants(state, table) if child != pid or code is None]
                if code is not None:
                    state.setdefault('exited_at', now)
                if not state['reason']:
                    if len(alive) > 64:
                        state['reason'] = 'descendant_limit'
                    elif cancelled:
                        state['reason'] = 'cancelled'
                    elif now - state['start'] >= wall:
                        state['reason'] = 'wall_timeout'
                    elif now - state['last_output'] >= stall and code is None:
                        state['reason'] = 'stalled'
                    elif code is not None and (alive or (not state['eof'] and now - state['exited_at'] >= 0.2)):
                        state['reason'] = 'orphaned_descendants'
                if state['reason'] and state['stop_at'] is None:
                    state['stop_at'] = now
                    terminate(state, signal.SIGTERM, table)
                if state['stop_at'] is not None and now - state['stop_at'] >= state['cleanup_seconds']:
                    terminate(state, signal.SIGKILL, table)
                cleanup_expired = state['stop_at'] is not None and now - state['stop_at'] >= state['cleanup_seconds'] + 2.5
                if cleanup_expired and (code is None or alive or not state['eof']):
                    state['reason'] = 'cleanup_incomplete'
                    if not state['eof']:
                        selector.unregister(p.stdout)
                        p.stdout.close()
                        state['eof'] = True
                    state['remaining_pids'] = alive
                if (code is not None and state['eof'] and not alive) or cleanup_expired:
                    # Reap owned adopted descendants without stealing Popen exits.
                    for child in state['owned']:
                        if child != pid:
                            try:
                                os.waitpid(child, os.WNOHANG)
                            except ChildProcessError:
                                pass
                    code = {'cancelled': 130, 'wall_timeout': 124, 'stalled': 124,
                            'output_limit': 125, 'orphaned_descendants': 125,
                            'unattributed_descendant': 125, 'descendant_limit': 125,
                            'cleanup_incomplete': 126}.get(state['reason'], code)
                    finish(state, code)
                    del active[pid]
            if now - last_heartbeat >= heartbeat and active:
                progress = ','.join(f"{s['id']}:elapsed={int(now-s['start'])}s,idle={int(now-s['last_output'])}s,bytes={s['bytes']},state={s['reason'] or 'running'}" for s in active.values())
                print(f'[WAIT] active={len(active)} {progress}', flush=True)
                last_heartbeat = now
            if cancelled and queue:
                for item in queue:
                    results.append({'id': item['id'], 'outcome': 'not-run', 'exit_code': 130,
                                    'reason': 'cancelled_before_start', 'started_at': None, 'finished_at': time.time()})
                queue.clear()
                persist()
        return 130 if cancelled else int(any(r['outcome'] != 'pass' for r in results))
    finally:
        table = snapshot()
        for state in active.values():
            terminate(state, signal.SIGKILL, table)
            state['process'].wait(timeout=2)
            state['log'].close()
        selector.close()
        for sig, handler in old_handlers.items():
            signal.signal(sig, handler)


if __name__ == '__main__':
    manifest = json.load(sys.stdin)
    sys.exit(run(sys.argv[1], int(sys.argv[2]), manifest,
                 wall=int(os.environ.get('SWARM_LAUNCH_WALL_SECONDS', '600')),
                 stall=int(os.environ.get('SWARM_LAUNCH_STALL_SECONDS', '120')),
                 cap=int(os.environ.get('SWARM_LAUNCH_OUTPUT_BYTES', '1048576'))))
