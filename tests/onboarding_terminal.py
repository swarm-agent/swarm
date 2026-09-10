#!/usr/bin/env python3
"""Bounded real-PTY driver for TestOnboardingTerminalRepositoryRecovery.

Requirement: Enter is not consent; cancellation cannot stage files or finish
onboarding. Success requires an empty committed tree and a usable home screen.
All fixtures live under the Go test's temporary HOME; no provider is contacted.
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
workspace = root / "launch"
workspace.mkdir()
env = os.environ.copy()
env["TERM"] = "xterm-256color"

def git(*args, check=True):
    return subprocess.run(["git", "-C", str(workspace), *args], env=env,
                          capture_output=True, text=True, timeout=5, check=check)

assert git("rev-parse", "--show-toplevel", check=False).returncode != 0
if scenario in ("existing", "cancel", "unborn"):
    (workspace / "keep.txt").write_text("must remain untracked\n")
if scenario == "unborn":
    git("init", "-b", "dev")
    assert git("rev-parse", "HEAD", check=False).returncode != 0
original_path = env["PATH"]
if scenario == "missing-git":
    empty_bin = root / "empty-bin"
    empty_bin.mkdir()
    env["PATH"] = str(empty_bin)

child = pexpect.spawn(binary, cwd=str(workspace), env=env, encoding="utf-8",
                      timeout=12, maxread=8192, dimensions=(48, 180), echo=False)
child.delaybeforesend = .05

def expect(text):
    # tcell emits cursor movement instead of spaces, including between letters.
    gap = r"(?:\s|\x1b\[[0-?]*[ -/]*[@-~]|\x1b\([A-Z])*"
    child.expect(gap.join(re.escape(c) for c in text if not c.isspace()))

def quit_clean():
    child.sendcontrol("c")
    child.expect(pexpect.EOF, timeout=5)
    child.close()
    assert child.exitstatus == 0 and child.signalstatus is None

try:
    expect("STEP 1 OF 3")
    if scenario == "identity-exit":
        quit_clean()
    else:
        child.send("Example\tExample\r")
        expect("STEP 2 OF 3")
        if scenario == "provider-exit":
            quit_clean()
        else:
            child.send("s")
            expect("STEP 3 OF 3")
            child.send("\r")
            if scenario == "missing-git":
                expect("Install Git, then press Enter to retry")
                # Restore only this process's command lookup via a symlink in
                # its private PATH. The host Git installation is never changed.
                import shutil
                (empty_bin / "git").symlink_to(shutil.which("git", path=original_path))
                child.send("\r")
            expect("Press y to initialize Git")
            assert not (workspace / ".git").exists() or scenario == "unborn"
            if scenario == "cancel":
                quit_clean()
                assert not (workspace / ".git").exists()
            elif scenario == "pending-exit":
                child.send("y")
                expect("Setting up Git")
                deadline = time.monotonic() + 3
                while not (root / "pending-request").exists() and time.monotonic() < deadline:
                    time.sleep(.02)
                assert (root / "pending-request").exists(), "request never reached pending API"
                quit_clean()
                assert not (workspace / ".git").exists()
            elif scenario == "existing":
                child.send("y")
                expect("Existing files were not staged or committed")
                assert not (workspace / ".git").exists()
                quit_clean()
            else:
                child.send("y")
                expect("workspace ready:")
                assert git("rev-parse", "HEAD").stdout.strip()
                assert git("ls-tree", "-r", "--name-only", "HEAD").stdout == ""
                assert git("diff", "--cached", "--name-only").stdout == ""
                if scenario in ("existing", "unborn"):
                    assert git("status", "--porcelain").stdout == "?? keep.txt\n"
                child.send("/help\r")
                expect("command palette loaded")
                quit_clean()
    if (workspace / "keep.txt").exists():
        assert (workspace / "keep.txt").read_text() == "must remain untracked\n"
except Exception:
    # Synthetic fixture output only; bound diagnostics and never log the token.
    print("scenario=" + scenario + " terminal_tail=" + str(child.before)[-4000:], file=sys.stderr)
    raise
finally:
    child.close(force=True)
