package main

import (
	"os"
	"os/exec"
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
  if [ "$4" = FSTYPE,FSROOT ]; then
    [ "$SHIM_HOME" = none ] && exit 1
    echo "${SHIM_HOME:-tmpfs /}"; exit 0
  fi
  if [ -n "$SHIM_VISIBLE" ]; then echo "rw,relatime"; exit 0; fi
  exit 1 ;;
sh) [ -n "$SHIM_SH_OK" ] && exit 0; exec "$@" ;;
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
	for _, k := range []string{"SHIM_INACTIVE", "SHIM_BIND_FAIL", "SHIM_VISIBLE", "SHIM_UMOUNT_FAIL", "SHIM_HOME", "SHIM_SH_OK"} {
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
	if err := machineWorkspaceTakeEffect([]string{a, b}, nil, true); err != nil {
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
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil, true); err != nil {
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
	if err := machineWorkspaceTakeEffect([]string{a}, nil, true); err != nil {
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
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil, true); err != nil {
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
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil, true); err != nil {
		t.Fatal(err)
	}
	log := workspaceLiveLog(t, logPath)
	if *restarts != 0 || strings.Contains(log, "nsenter") || strings.Contains(log, "systemctl bind") {
		t.Fatalf("restarts=%d log:\n%s", *restarts, log)
	}
}

func TestWorkspaceRemoveUnmountsLiveOrRestarts(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	if err := machineWorkspaceTakeEffect(nil, []string{"/srv/a"}, true); err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 || !strings.Contains(workspaceLiveLog(t, logPath), "nsenter --target 4242 --mount -- umount /srv/a") {
		t.Fatalf("restarts=%d log:\n%s", *restarts, workspaceLiveLog(t, logPath))
	}
	t.Setenv("SHIM_UMOUNT_FAIL", "1")
	if err := machineWorkspaceTakeEffect(nil, []string{"/srv/a"}, true); err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 {
		t.Fatalf("busy unmount did not fall back to restart: %d", *restarts)
	}
}

// Purpose: a running Swarm started with the installed ProtectHome=yes sees
// home folders as systemd's shared, mode-0000 inaccessible node. Nothing can be
// mounted there for it, and writing a mount point there would leak empty
// folders into every other service using that node. The first add (no earlier
// ProtectHome=tmpfs drop-in) and a namespace whose home is not a private tmpfs
// must both take the idle restart, never claim success, and never enter the
// namespace to write.
func TestWorkspaceAddUnderHiddenHomeRestartsInstead(t *testing.T) {
	logPath, restarts := workspaceLiveShims(t)
	if err := machineWorkspaceTakeEffect([]string{"/root/project"}, nil, false); err != nil {
		t.Fatal(err)
	}
	log := workspaceLiveLog(t, logPath)
	if *restarts != 1 || strings.Contains(log, "nsenter") || strings.Contains(log, "systemctl bind") {
		t.Fatalf("first home add: restarts=%d log:\n%s", *restarts, log)
	}

	// The drop-in existed, but the running namespace still has the
	// inaccessible node at /root (it was never restarted with it).
	t.Setenv("SHIM_HOME", "tmpfs /systemd/inaccessible/dir")
	err := machineWorkspaceLive([]string{"/root/project"}, nil)
	if err == nil || !strings.Contains(err.Error(), "/root is hidden") {
		t.Fatalf("hidden home not refused: %v", err)
	}
	if err := machineWorkspaceTakeEffect([]string{"/root/project"}, nil, true); err != nil {
		t.Fatal(err)
	}
	log = workspaceLiveLog(t, logPath)
	if *restarts != 2 || strings.Contains(log, "sh -eu") || strings.Contains(log, "systemctl bind") {
		t.Fatalf("hidden namespace: restarts=%d log:\n%s", *restarts, log)
	}

	// With a private tmpfs home, the same folder mounts live (the mount
	// point step itself is covered by TestWorkspaceMountPointRefusesSharedNode).
	t.Setenv("SHIM_HOME", "")
	t.Setenv("SHIM_SH_OK", "1")
	if err := machineWorkspaceTakeEffect([]string{"/root/project"}, nil, true); err != nil {
		t.Fatal(err)
	}
	if *restarts != 2 || !strings.Contains(workspaceLiveLog(t, logPath), "systemctl bind "+machineDaemon+" /root/project /root/project") {
		t.Fatalf("private home: restarts=%d log:\n%s", *restarts, workspaceLiveLog(t, logPath))
	}
}

// Folders outside home are unaffected by ProtectHome, so the first add of one
// still needs no restart. (/srv/a: the mount-point step is shimmed; a temp
// dir may itself sit under home.)
func TestWorkspaceFirstAddOutsideHomeStaysLive(t *testing.T) {
	_, restarts := workspaceLiveShims(t)
	t.Setenv("SHIM_SH_OK", "1")
	if err := machineWorkspaceTakeEffect([]string{"/srv/a"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 {
		t.Fatalf("restarted %d times", *restarts)
	}
}

// The in-namespace mount-point script must refuse a tmpfs that is a bind of a
// shared node (FSROOT not /) without creating anything or remounting it.
func TestWorkspaceMountPointRefusesSharedNode(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"findmnt": "#!/bin/sh\ncase \"$*\" in\n*-T*) echo " + root + " ;;\n*FSTYPE,FSROOT*) echo 'tmpfs  /systemd/inaccessible/dir' ;;\n*) echo ro ;;\nesac\n",
		"mount":   "#!/bin/sh\necho mount >> " + filepath.Join(root, "mounts") + "\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "home", "x")
	cmd := exec.Command("sh", "-eu", "-c", machineWorkspaceMountPoint, "swarm-mount-point", target)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "not on Swarm's private folder tree") {
		t.Fatalf("shared node accepted: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "home")); !os.IsNotExist(err) {
		t.Fatalf("mount point was created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mounts")); !os.IsNotExist(err) {
		t.Fatal("the shared node was remounted")
	}
}
