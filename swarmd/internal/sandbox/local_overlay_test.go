package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func overlayFixture(t *testing.T) (string, string, string, []string) {
	t.Helper()
	base := t.TempDir()
	source, checkout := filepath.Join(base, "source"), filepath.Join(base, "job")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, source, "init", "-b", "dev")
	localFixtureGit(t, source, "commit", "--allow-empty", "-m", "base")
	start := localFixtureGit(t, source, "rev-parse", "HEAD")
	localFixtureGit(t, source, "worktree", "add", "-b", "agent/job", checkout)
	var picks []string
	for _, name := range []string{"A", "B", "C"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		localFixtureGit(t, source, "add", name)
		localFixtureGit(t, source, "commit", "-m", name)
		picks = append(picks, localFixtureGit(t, source, "rev-parse", "HEAD"))
	}
	localFixtureGit(t, source, "pack-refs", "--all", "--no-prune")
	return source, checkout, start, picks
}

func TestLocalLinkedOverlayMounts(t *testing.T) {
	// Purpose: localGitMounts must never expose the real common top level for
	// writes. Argument-level checks are the narrowest proof of mount ordering,
	// readonly preservation, and fail-closed behavior on older bubblewrap.
	source, checkout, _, _ := overlayFixture(t)
	m := &Manager{localOverlay: true}
	common := filepath.Join(source, ".git")
	gitdir, err := localGitAdmin(checkout)
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.localGitMounts(checkout, source, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\x00" + strings.Join(args, "\x00") + "\x00"
	contains := func(parts ...string) bool { return strings.Contains(joined, "\x00"+strings.Join(parts, "\x00")+"\x00") }
	if !contains("--overlay-src", common, "--tmp-overlay", common) {
		t.Fatalf("missing overlay: %v", args)
	}
	if contains("--bind", common, common) {
		t.Fatal("real common directory writable")
	}
	for _, p := range []string{gitdir, filepath.Join(common, "objects"), filepath.Join(common, "refs/heads/agent"), filepath.Join(common, "logs/refs/heads/agent")} {
		if !contains("--bind", p, p) {
			t.Fatalf("missing real bind %s", p)
		}
		if strings.Index(joined, "--tmp-overlay") > strings.Index(joined, "--bind\x00"+p+"\x00") {
			t.Fatalf("bind precedes overlay: %s", p)
		}
	}
	m.localOverlay = false
	if args, err := m.localGitMounts(checkout, source, false); err == nil || args != nil || !strings.Contains(err.Error(), "requires bubblewrap overlay support") {
		t.Fatalf("unsupported overlay did not fail closed: %v %v", args, err)
	}
	args, err = m.localGitMounts(checkout, source, true)
	want := []string{"--ro-bind", filepath.Join(checkout, ".git"), filepath.Join(checkout, ".git"), "--ro-bind", common, common}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("readonly mounts changed: %v %v", args, err)
	}
}

func TestLocalLinkedOverlayUnboundSymlinks(t *testing.T) {
	// Purpose: localGitMounts must tolerate unmounted file/directory symlinks
	// without binding their targets. Argument checks directly prove this mount
	// boundary, while objects still must reject a symlink before a real bind.
	source, checkout, _, _ := overlayFixture(t)
	common := filepath.Join(source, ".git")
	m := &Manager{localOverlay: true}
	for _, target := range []string{filepath.Join(source, "A"), source} {
		name := filepath.Join(common, "linked-"+filepath.Base(target))
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
		args, err := m.localGitMounts(checkout, source, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range args {
			if arg == name {
				t.Fatalf("symlink bind: %v", args)
			}
		}
	}
	objects := filepath.Join(common, "objects")
	if err := os.Rename(objects, objects+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(objects+"-saved", objects); err != nil {
		t.Fatal(err)
	}
	if args, err := m.localGitMounts(checkout, source, false); err == nil || args != nil {
		t.Fatalf("symlinked real objects bind accepted: %v %v", args, err)
	}
}

func TestLocalLinkedOverlayCherryPickIsolation(t *testing.T) {
	// Purpose: real Git and bubblewrap must complete multi-pick pseudoref
	// deletions without permitting real peer ref/packed-refs mutations. Owners:
	// localGitMounts/localArgv. Only an OS integration test proves copy-on-write
	// isolation and durable own-branch commits; unavailable namespaces skip.
	m := localTestManager(t)
	if !m.localOverlay {
		t.Skip("installed bubblewrap lacks --overlay-src/--tmp-overlay support")
	}
	source, checkout, start, picks := overlayFixture(t)
	common := filepath.Join(source, ".git")
	gitdir, err := localGitAdmin(checkout)
	if err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	devPath, packedPath := filepath.Join(common, "refs/heads/dev"), filepath.Join(common, "packed-refs")
	devBefore, packedBefore := read(devPath), read(packedPath)
	// Probe the actual overlay independently: a host may provide the flags but
	// disable unprivileged overlayfs. Never mistake a Git failure for this skip.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe := append(localBaseArgs(), "--overlay-src", common, "--tmp-overlay", common, "--", "/usr/bin/true")
	if out, err := exec.CommandContext(ctx, m.cfg.Engine, probe...).CombinedOutput(); err != nil {
		t.Skipf("bubblewrap overlay mount unavailable: %v: %s", err, out)
	}
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{source}, nil }})
	// Positional parameters keep all fixture paths out of shell syntax.
	script := `set -eu
common=$1; admin=$2; start=$3; shift 3
git cherry-pick "$@"
test ! -e "$admin/CHERRY_PICK_HEAD"
if git update-ref refs/heads/dev "$start"; then exit 41; fi
if git update-ref -d refs/heads/dev; then exit 42; fi
printf 'private packed rewrite\n' > "$common/packed-refs"
test "$(cat "$common/packed-refs")" = 'private packed rewrite'
printf private > "$common/packed-refs.lock"
`
	command := append([]string{"bash", "-c", script, "overlay-test", common, gitdir, start}, picks...)
	result, err := m.execLocal(ctx, Scope{Root: source, JobCheckout: checkout, daemonGit: true}, ExecRequest{WorkingDir: checkout, Command: command, Timeout: 20 * time.Second, Env: map[string]string{"GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("sandbox exit %d: %s", result.ExitCode, result.Stdout)
	}
	if read(devPath) != devBefore || read(packedPath) != packedBefore {
		t.Fatal("real peer ref or packed refs changed")
	}
	for _, p := range []string{filepath.Join(common, "packed-refs.lock"), filepath.Join(gitdir, "CHERRY_PICK_HEAD")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("stale lock/pseudoref %s: %v", p, err)
		}
	}
	if got := localFixtureGit(t, source, "rev-list", "--count", start+"..refs/heads/agent/job"); got != "3" {
		t.Fatalf("expected three real commits, got %s", got)
	}
	for _, name := range []string{"A", "B", "C"} {
		if read(filepath.Join(checkout, name)) != name {
			t.Fatalf("pick %s missing", name)
		}
	}
}

func TestLocalLinkedGitPackingPolicy(t *testing.T) {
	// Purpose: routeLocalGit's argument policy must reject explicit ref packing
	// and override caller auto-maintenance settings. This pure policy test needs
	// no namespaces and proves rejection before any command can mutate refs.
	for _, args := range [][]string{{"pack-refs", "--all", "--prune"}, {"-c", "gc.auto=1", "gc", "--auto"}, {"--no-pager", "maintenance", "run", "--task=pack-refs"}, {"--git-dir=x", "pack-refs"}} {
		if got, err := localLinkedGitArgs(args); err == nil || got != nil {
			t.Fatalf("accepted packing %v: %v %v", args, got, err)
		}
	}
	args, err := localLinkedGitArgs([]string{"-c", "gc.auto=1", "-c", "maintenance.auto=true", "-c", "gc.packRefs=true", "commit", "--allow-empty", "-m", "test"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "gc.auto=1", "-c", "maintenance.auto=true", "-c", "gc.packRefs=true", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "gc.packRefs=false", "commit", "--allow-empty", "-m", "test"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("policy precedence: %v", args)
	}
}

func TestLocalLinkedGitPackingPreservesBranch(t *testing.T) {
	// Purpose: routeLocalGit must preserve the real own loose ref after a commit
	// and explicit pack/gc attempts. Real routing is the narrowest integration
	// layer proving policy wiring and unchanged durable refs on rejection.
	m := localTestManager(t)
	if !m.localOverlay {
		t.Skip("bubblewrap overlay support unavailable")
	}
	source, checkout, _, _ := overlayFixture(t)
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{source}, nil }})
	localRoutedGit(t, m, checkout, "-c", "gc.auto=1", "-c", "maintenance.auto=true", "commit", "--allow-empty", "-m", "keep loose branch")
	own := filepath.Join(source, ".git", "refs", "heads", "agent", "job")
	before, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	head := localFixtureGit(t, checkout, "rev-parse", "HEAD")
	if strings.TrimSpace(string(before)) != head {
		t.Fatal("own loose ref does not match committed HEAD")
	}
	packed := filepath.Join(source, ".git", "packed-refs")
	packedBefore, err := os.ReadFile(packed)
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"pack-refs", "--all", "--prune"}, {"gc", "--auto"}, {"maintenance", "run", "--task=pack-refs"}} {
		cmd := exec.Command("git", argv...)
		cmd.Dir = checkout
		if err := m.RouteGit(context.Background(), cmd); err == nil {
			ReleaseGit(cmd)
			t.Fatalf("packing admitted: %v", argv)
		} else if !strings.Contains(err.Error(), "private packed refs") {
			t.Fatal(err)
		}
		after, err := os.ReadFile(own)
		if err != nil || string(after) != string(before) {
			t.Fatalf("own loose ref changed: %q %v", after, err)
		}
		after, err = os.ReadFile(packed)
		if err != nil || string(after) != string(packedBefore) {
			t.Fatalf("real packed refs changed: %v", err)
		}
	}
}
