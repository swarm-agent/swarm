package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/lock"
	"swarm/packages/swarmd/internal/sandbox"
)

// Managed transfers contain committed objects only, never repository config,
// credentials, hooks, reflogs or unrelated history. Larger repositories fail
// explicitly rather than falling back to a host checkout.
const MaxManagedPackBytes = 8 << 20

var managedImportQueues sync.Map

// Queue managed imports by common repository, while retaining the existing
// cross-process integration lock as authority. A busy sibling is not a failed
// handoff. Waiting is bounded and never releases somebody else's lock.
func acquireManagedImportLock(ctx context.Context, root string) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	common, err := managedGit(ctx, root, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	dir, err := resolveGitPath(root, strings.TrimSpace(string(common)))
	if err != nil {
		return nil, err
	}
	key := filepath.Join(dir, "swarm-integration.lock")
	value, _ := managedImportQueues.LoadOrStore(key, make(chan struct{}, 1))
	queue := value.(chan struct{})
	select {
	case queue <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for managed Git handoff: %w", ctx.Err())
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			<-queue
			return nil, fmt.Errorf("wait for repository integration lock: %w", err)
		}
		owner, err := lock.Acquire(key, lock.Metadata{PID: os.Getpid(), StartedAt: time.Now().UnixMilli()})
		if err == nil {
			return func() { _ = owner.Release(); <-queue }, nil
		}
		if !errors.Is(err, lock.ErrAlreadyRunning) {
			<-queue
			return nil, fmt.Errorf("acquire repository integration lock: %w", err)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
		}
	}
}

type managedBoundedWriter struct {
	bytes.Buffer
	limit int
}

func (w *managedBoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		return 0, errors.New("managed Git transfer exceeds byte limit")
	}
	return w.Buffer.Write(p)
}
func managedGit(ctx context.Context, root string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	flags := []string{"--no-replace-objects", "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never", "-C", root}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0"}
	cmd.Stdin = bytes.NewReader(input)
	out := &managedBoundedWriter{limit: MaxManagedPackBytes}
	diagnostic := &managedBoundedWriter{limit: 8192}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	sandbox.Prepare(ctx, cmd)
	defer sandbox.ReleaseGit(cmd)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("managed Git %s: %w: %s", args[0], err, diagnostic.String())
	}
	return out.Bytes(), nil
}

func managedTree(ctx context.Context, root, head string) error {
	tree, err := managedGit(ctx, root, nil, "ls-tree", "-rz", "--full-tree", head)
	if err != nil {
		return err
	}
	for _, row := range bytes.Split(tree, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		parts := bytes.SplitN(row, []byte{'\t'}, 2)
		if len(parts) != 2 || !(bytes.HasPrefix(parts[0], []byte("100644 blob ")) || bytes.HasPrefix(parts[0], []byte("100755 blob "))) {
			return errors.New("managed source forbids symlinks, submodules and special files")
		}
		name := string(parts[1])
		if path.Clean(name) != name || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe managed source path")
		}
		for _, component := range strings.Split(strings.ToLower(name), "/") {
			if component == ".git" || component == ".ssh" || component == ".gnupg" || component == ".aws" || component == ".docker" || component == ".npmrc" || component == ".netrc" || strings.HasPrefix(component, ".env") || strings.HasSuffix(component, ".pem") || strings.HasSuffix(component, ".key") || component == "credentials" {
				return errors.New("managed source contains a protected path")
			}
		}
	}
	return nil
}

// ExportManagedCheckout exports exactly one commit and its tree. The remote
// creates a shallow repository at that OID, with no source history or config.
func ExportManagedCheckout(ctx context.Context, root, branch, base string) ([]byte, error) {
	if !validCommitID(base) || branch == "" {
		return nil, errors.New("managed source identity missing")
	}
	if err := checkManagedDestination(ctx, root, branch, base); err != nil {
		return nil, err
	}
	if err := managedTree(ctx, root, base); err != nil {
		return nil, err
	}
	objects, err := managedGit(ctx, root, nil, "rev-list", "--objects", "--no-object-names", base+"^{tree}")
	if err != nil {
		return nil, err
	}
	objects = append(objects, []byte(base+"\n")...)
	return managedGit(ctx, root, objects, "pack-objects", "--stdout")
}
func checkManagedDestination(ctx context.Context, root, branch, head string) error {
	got, err := managedGit(ctx, root, nil, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(got)) != branch {
		return errors.New("managed destination branch changed")
	}
	got, err = managedGit(ctx, root, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(got)) != head {
		return errors.New("managed destination HEAD changed")
	}
	got, err = managedGit(ctx, root, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(got)) != 0 {
		return errors.New("managed destination is dirty; refusing overwrite")
	}
	return nil
}

// ImportManagedCheckout advances only the allocated child branch. The caller
// supplies a lease/ownership fence, rechecked after waiting and before refs move.
// Objects may be retained after a rejected import; refs/files never advance on
// failed identity, scope, source or ancestry checks. Repeating an exact completed
// import is safe. Integration into a parent branch is a separate operation.
func ImportManagedCheckout(ctx context.Context, root, branch, base, head string, pack []byte, scopes []string, fence func() error) error {
	if !validCommitID(base) || !validCommitID(head) || head == base || len(pack) == 0 || len(pack) > MaxManagedPackBytes || fence == nil {
		return errors.New("invalid managed Git handoff")
	}
	release, err := acquireManagedImportLock(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	if err = fence(); err != nil {
		return err
	}
	if err = checkManagedDestination(ctx, root, branch, head); err == nil {
		return nil
	}
	if err = checkManagedDestination(ctx, root, branch, base); err != nil {
		return err
	}
	if _, err = managedGit(ctx, root, pack, "-c", "transfer.fsckObjects=true", "index-pack", "--stdin", "--strict"); err != nil {
		return err
	}
	if _, err = managedGit(ctx, root, nil, "merge-base", "--is-ancestor", base, head); err != nil {
		return errors.New("managed result does not descend from allocated base")
	}
	history, err := managedGit(ctx, root, nil, "rev-list", "--reverse", base+".."+head)
	if err != nil {
		return err
	}
	commits := strings.Fields(string(history))
	if len(commits) == 0 || len(commits) > 256 {
		return errors.New("managed handoff requires 1..256 linear commits")
	}
	previous := base
	for _, commit := range commits {
		parents, err := managedGit(ctx, root, nil, "rev-list", "--parents", "-n", "1", commit)
		if err != nil {
			return err
		}
		row := strings.Fields(string(parents))
		if len(row) != 2 || row[0] != commit || row[1] != previous {
			return errors.New("managed handoff requires linear history from the exact base")
		}
		if err = managedTree(ctx, root, commit); err != nil {
			return err
		}
		changed, err := managedGit(ctx, root, nil, "diff", "--name-only", "-z", previous, commit)
		if err != nil {
			return err
		}
		for _, raw := range bytes.Split(changed, []byte{0}) {
			if len(raw) == 0 {
				continue
			}
			name := string(raw)
			allowed := len(scopes) == 0
			for _, scope := range scopes {
				scope = strings.TrimSuffix(scope, "/")
				if scope == "." || name == scope || strings.HasPrefix(name, scope+"/") {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("managed commit changes %q outside owned scope", name)
			}
		}
		previous = commit
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = fence(); err != nil {
		return err
	}
	if err = checkManagedDestination(ctx, root, branch, base); err != nil {
		return err
	}
	if _, err = managedGit(ctx, root, nil, "merge", "--ff-only", "--no-edit", "--no-stat", "--no-overwrite-ignore", head); err != nil {
		return err
	}
	return checkManagedDestination(ctx, root, branch, head)
}

var _ io.Writer = (*managedBoundedWriter)(nil)
