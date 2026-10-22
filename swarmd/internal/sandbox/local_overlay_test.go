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
	if err := os.Symlink(filepath.Join(source, "A"), filepath.Join(common, "external")); err != nil {
		t.Fatal(err)
	}
	m.localOverlay = true
	if args, err := m.localGitMounts(checkout, source, false); err == nil || args != nil {
		t.Fatalf("symlink metadata accepted: %v %v", args, err)
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
