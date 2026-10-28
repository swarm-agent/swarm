package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: `swarmctl workspace add|remove` must change the running Swarm in
// place (bind-mount into, or unmount from, the service's mount namespace) and
// restart Swarm only when that cannot be done. The regression it prevents is
// the old behaviour where every added folder restarted Swarm (or was refused
// while it worked). machineWorkspaceTakeEffect / machineWorkspaceLive own the
// decision; the privileged commands (systemctl, nsenter) are replaced by PATH
// shims that log their argv, as the shared-Git grant test does, because the
// real ones need root and a running unit. The restart itself is replaced by a
// recorder: its wait-for-idle loop talks to the live daemon socket.
func workspaceLiveShims(t *testing.T) (logPath string, restarts *int) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	shims := map[string]string{
		"systemctl": `#!/bin/sh
echo "systemctl $*" >> "$SHIM_LOG"
case "$1" in
daemon-reload) exit 0 ;;
is-active) exit "${SHIM_INACTIVE:-0}" ;;
show) echo "${SHIM_PID-4242}"; exit 0 ;;
bind)
  if [ -n "$SHIM_BIND_FAIL" ]; then echo "Failed to bind mount: $SHIM_BIND_FAIL" >&2; exit 1; fi
  exit 0 ;;
esac
exit 90
`,
		"nsenter": `#!/bin/sh
echo "nsenter $*" >> "$SHIM_LOG"
[ "$1 $2 $3 $4" = "--target 4242 --mount --" ] || exit 91
shift 4
case "$1" in
findmnt)
  if [ -n "$SHIM_VISIBLE" ]; then echo "rw,relatime"; exit 0; fi
  exit 1 ;;
sh) exec "$@" ;;
umount)
  if [ -n "$SHIM_UMOUNT_FAIL" ]; then echo "umount: $2: target is busy." >&2; exit 32; fi
  exit 0 ;;
esac
exit 92
`,
	}
	for name, body := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath = filepath.Join(root, "log")
	t.Setenv("SHIM_LOG", logPath)
	for _, k := range []string{"SHIM_INACTIVE", "SHIM_BIND_FAIL", "SHIM_VISIBLE", "SHIM_UMOUNT_FAIL"} {
		t.Setenv(k, "")
	}
	n := 0
	restarts = &n
	saved := machineWorkspaceRestart
	machineWorkspaceRestart = func() error { n++; return nil }
	t.Cleanup(func() { machineWorkspaceRestart = saved })
	return logPath, restarts
}

func workspaceLiveLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestWorkspaceAddMountsLiveWithoutRestart(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	a, b := t.TempDir(), t.TempDir()
	if err := machineWorkspaceTakeEffect([]string{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 {
		t.Fatalf("restarted %d times", *restarts)
	}
	log := workspaceLiveLog(t, logPath)
	for _, want := range []string{
		"systemctl daemon-reload",
		"systemctl show -p MainPID --value " + machineDaemon,
		"nsenter --target 4242 --mount -- findmnt -n -o OPTIONS --mountpoint " + a,
		"nsenter --target 4242 --mount -- sh -eu -c",
		"systemctl bind " + machineDaemon + " " + a + " " + a,
		"systemctl bind " + machineDaemon + " " + b + " " + b,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in:\n%s", want, log)
		}
	}
}

func TestWorkspaceAddSkipsFolderAlreadyMounted(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	t.Setenv("SHIM_VISIBLE", "1")
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil); err != nil {
		t.Fatal(err)
	}
	log := workspaceLiveLog(t, logPath)
	if *restarts != 0 || strings.Contains(log, "systemctl bind") || strings.Contains(log, "sh -eu") {
		t.Fatalf("restarts=%d log:\n%s", *restarts, log)
	}
}

func TestWorkspaceAddFallsBackToIdleRestart(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	t.Setenv("SHIM_BIND_FAIL", "Operation not supported")
	a := t.TempDir()
	if err := machineWorkspaceTakeEffect([]string{a}, nil); err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 {
		t.Fatalf("restarts=%d log:\n%s", *restarts, workspaceLiveLog(t, logPath))
	}
	err := machineWorkspaceLive([]string{a}, nil)
	if err == nil || !strings.Contains(err.Error(), "Operation not supported") {
		t.Fatalf("reason not reported: %v", err)
	}
}

func TestWorkspaceAddUnknownProcessRestarts(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	t.Setenv("SHIM_PID", "0")
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil); err != nil {
		t.Fatal(err)
	}
	log := workspaceLiveLog(t, logPath)
	if *restarts != 1 || strings.Contains(log, "nsenter") || strings.Contains(log, "systemctl bind") {
		t.Fatalf("restarts=%d log:\n%s", *restarts, log)
	}
}

func TestWorkspaceChangeWhileStoppedNeitherMountsNorRestarts(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	t.Setenv("SHIM_INACTIVE", "3")
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil); err != nil {
		t.Fatal(err)
	}
	log := workspaceLiveLog(t, logPath)
	if *restarts != 0 || strings.Contains(log, "nsenter") || strings.Contains(log, "systemctl bind") {
		t.Fatalf("restarts=%d log:\n%s", *restarts, log)
	}
}

func TestWorkspaceRemoveUnmountsLiveOrRestarts(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	if err := machineWorkspaceTakeEffect(nil, []string{"/srv/a"}); err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 || !strings.Contains(workspaceLiveLog(t, logPath), "nsenter --target 4242 --mount -- umount /srv/a") {
		t.Fatalf("restarts=%d log:\n%s", *restarts, workspaceLiveLog(t, logPath))
	}
	t.Setenv("SHIM_UMOUNT_FAIL", "1")
	if err := machineWorkspaceTakeEffect(nil, []string{"/srv/a"}); err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 {
		t.Fatalf("busy unmount did not fall back to restart: %d", *restarts)
	}
}
