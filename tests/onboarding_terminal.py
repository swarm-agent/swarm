#!/usr/bin/env python3
"""Real PTY proof: visible Tab/Enter actions, daemon-confirmed setup and preserved content.
The real API fixture owns auth/config/catalog. No provider or host service is used.
"""
import os
from pathlib import Path
import subprocess
import sys
import re
import time
import pexpect

binary, scenario = sys.argv[1:]
root = Path.cwd()
workspace = root / 'launch'
workspace.mkdir()
env = os.environ.copy()
env['TERM'] = 'xterm-256color'
def git(*args, check=True):
    return subprocess.run(['git', '-C', str(workspace), *args], env=env, capture_output=True, text=True, timeout=5, check=check)
if scenario in ('existing', 'unborn', 'cancel', 'cancel-recover'):
    (workspace / 'keep.txt').write_text('preserve source content\n')
if scenario in ('unborn', 'committed'):
    git('init', '-b', 'dev')
if scenario == 'committed':
    git('-c', 'user.name=Example', '-c', 'user.email=example@localhost', 'commit', '--allow-empty', '-m', 'Initial')
launch = root if scenario == 'home' else Path('/') if scenario == 'root' else workspace
child = pexpect.spawn(binary, cwd=str(launch), env=env, encoding='utf-8', timeout=12, maxread=8192, dimensions=(30, 110), echo=False)
child.delaybeforesend = .08
def expect(text):
    gap = r'(?:\s|\x1b\[[0-?]*[ -/]*[@-~]|\x1b\([A-Z])*'
    child.expect(gap.join(re.escape(c) for c in text if not c.isspace()))
def tabs(count):
    child.send('\t' * count)
    time.sleep(.15)
def choose(count):
    tabs(count)
    child.send('\r')
def quit_clean():
    child.sendcontrol('c')
    child.expect(pexpect.EOF, timeout=5)
    child.close()
    assert child.exitstatus == 0 and child.signalstatus is None

def select_location(path, create=True):
    choose(1)  # visible Select another location
    expect('Edit path')
    child.sendcontrol('u')
    child.send(str(path) + '\r')
    expect('Location selected')
    if create:
        choose(2)  # visible Create selected folder
        deadline=time.monotonic()+5
        while not path.is_dir() and time.monotonic()<deadline: time.sleep(.05)
        time.sleep(.2)
        assert path.is_dir() and not (path / '.git').exists()

def finish():
    global child
    expect('workspace ready:')
    assert git('rev-parse', 'HEAD').stdout.strip()
    names = git('ls-tree', '-r', '--name-only', 'HEAD').stdout
    assert names == ('keep.txt\n' if scenario in ('existing','unborn') else '')
    git('worktree','add','--detach',str(root/'managed-proof'),'HEAD')
    assert (root/'managed-proof'/'.git').is_file()
    child.send('/help\r')
    expect('command palette loaded')
    quit_clean()
    # A second client must use durable completion, not replay identity or setup.
    head=git('rev-parse','HEAD').stdout
    child=pexpect.spawn(binary,cwd=str(workspace),env=env,encoding='utf-8',timeout=12,dimensions=(30,110),echo=False)
    time.sleep(.5)
    child.send('/help\r')
    expect('command palette loaded')
    quit_clean()
    assert git('rev-parse','HEAD').stdout==head
try:
    expect('STEP 1 OF 3')
    if scenario == 'identity-exit':
        quit_clean(); sys.exit(0)
    child.send('Example\tExample\r')
    expect('STEP 2 OF 3')
    if scenario == 'provider-exit':
        quit_clean(); sys.exit(0)
    choose(1)  # visible Skip provider
    expect('STEP 3 OF 3')
    if scenario == 'permission':
        denied = root/'denied'; denied.mkdir(mode=0o500)
        select_location(denied/'project', False)
        choose(2); expect('Folder creation failed')
        assert not (denied/'project').exists()
        denied.chmod(0o700)
        # focus is still Create folder, cycle back to Select via Shift-Tab
        child.send('\x1b[Z'); child.send('\r'); expect('Edit path')
        workspace=root/'recovered'; child.sendcontrol('u'); child.send(str(workspace)+'\r'); expect('Location selected')
        choose(2); time.sleep(.5); assert workspace.is_dir()
    elif scenario in ('home','root','new-folder','cancel-recover'):
        workspace=root/('safe-project' if scenario=='cancel-recover' else 'new-project')
        select_location(workspace)
    if scenario == 'cancel':
        quit_clean(); assert not (workspace/'.git').exists(); sys.exit(0)
    # A successful create already inspected the directory and reset focus.
    if scenario not in ('home','root','new-folder','permission','cancel-recover'):
        child.send('\r')
    # Daemon inspection resets focus: Verify, Select, Create, Suggest, then readiness action.
    if scenario == 'committed':
        expect('Save / Open workspace'); choose(4); finish(); sys.exit(0)
    if scenario in ('existing','unborn'):
        expect('Review content'); choose(5 if scenario == 'unborn' else 4); expect('keep.txt')
        child.send('\r')  # explicitly select only this file
        choose(1)  # omissions acknowledgement
        choose(1)  # create baseline
        finish(); assert (workspace/'keep.txt').read_text()=='preserve source content\n'; sys.exit(0)
    if scenario == 'missing-git':
        expect('Administrator repair required')
        child.send('\r')
    expect('Initialize empty Git repository')
    choose(4)
    expect('Confirm empty Git initialization')
    assert not (workspace/'.git').exists()
    child.send('\r')
    if scenario == 'pending-exit':
        deadline=time.monotonic()+4
        while not (root/'pending-request').exists() and time.monotonic()<deadline: time.sleep(.05)
        assert (root/'pending-request').exists()
        quit_clean(); assert not (workspace/'.git').exists()
    else:
        finish()
finally:
    if child.isalive(): child.terminate(force=True)
