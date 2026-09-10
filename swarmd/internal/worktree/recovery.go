package worktree

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const recoveryByteLimit = 16 << 20
const recoveryFileLimit = 2048

// RecoveryIdentity is Git evidence, never account authorization or ownership.
// Callers must authorize the saved repository and candidate before inspecting it.
type RecoveryIdentity struct {
	Path, CommonDir, GitDir, HEAD, Fingerprint string
	Branch                                     string
}

// RecoverySelection accepts exact relative files only. Commits and arbitrary
// patches are deliberately rejected rather than silently importing other work.
type RecoverySelection struct {
	Files   []string
	Commits []string
	Patch   []byte
}

type recoveryFile struct {
	Name string
	Data []byte
	Mode os.FileMode
}

// RecoverySnapshot is immutable to callers; patches retain index/worktree layers.
type RecoverySnapshot struct {
	Identity         RecoveryIdentity
	repository       string
	selection        []string
	staged, unstaged []byte
	untracked        []recoveryFile
}

// RecoveryResult is pre-publication evidence. On failure Allocation is retained
// for explicit recovery; no source cleanup, reset, stash or implicit commit occurs.
type RecoveryResult struct {
	Allocation  Allocation
	Source      RecoveryIdentity
	Destination RecoveryIdentity
	Diagnostic  string
}

type recoveryOutput struct{ bytes.Buffer }

func (b *recoveryOutput) Write(p []byte) (int, error) {
	if len(p) > recoveryByteLimit-b.Len() {
		return 0, errors.New("recovery Git output exceeds byte limit")
	}
	return b.Buffer.Write(p)
}

// recoveryGit disables optional index writes, external diff/textconv and hooks.
// It bounds each subprocess and output and ignores ambient Git routing variables.
func recoveryGit(path string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-C", path}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1")
	cmd.Stdin = bytes.NewReader(input)
	var out, stderr recoveryOutput
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("recovery git %s: %w: %s", args[0], err, stderr.String())
	}
	return out.Bytes(), nil
}

func recoveryExactPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("recovery requires a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("recovery refuses symlink path components")
	}
	return nil
}

func recoveryIdentity(repository, candidate string) (RecoveryIdentity, error) {
	var id RecoveryIdentity
	for _, path := range []string{repository, candidate} {
		if err := recoveryExactPath(path); err != nil {
			return id, err
		}
	}
	resolve := func(path, flag string) (string, error) {
		out, err := recoveryGit(path, nil, "rev-parse", "--path-format=absolute", flag)
		if err != nil {
			return "", err
		}
		value := strings.TrimSuffix(string(out), "\n")
		if err := recoveryExactPath(value); err != nil {
			return "", err
		}
		return value, nil
	}
	for _, path := range []string{repository, candidate} {
		if err := recoveryExactPath(filepath.Join(path, ".git")); err != nil {
			return id, err
		}
	}
	common, err := resolve(repository, "--git-common-dir")
	if err != nil {
		return id, err
	}
	top, err := resolve(candidate, "--show-toplevel")
	if err != nil || top != candidate {
		return id, errors.New("recovery candidate is not an exact checkout root")
	}
	candidateCommon, err := resolve(candidate, "--git-common-dir")
	if err != nil || candidateCommon != common {
		return id, errors.New("recovery candidate common directory mismatch")
	}
	admin, err := resolve(candidate, "--absolute-git-dir")
	if err != nil {
		return id, err
	}
	if admin != common && !pathWithinRoot(filepath.Join(common, "worktrees"), admin) {
		return id, errors.New("recovery admin directory outside repository authority")
	}
	out, err := recoveryGit(repository, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return id, err
	}
	registered := false
	for _, field := range bytes.Split(out, []byte{0}) {
		if string(field) == "worktree "+candidate {
			registered = true
		}
	}
	if !registered {
		return id, errors.New("recovery candidate is not Git registered")
	}
	if admin != common {
		back, err := recoveryRead(admin, "gitdir")
		if err != nil || strings.TrimSuffix(string(back.Data), "\n") != filepath.Join(candidate, ".git") {
			return id, errors.New("recovery admin backlink mismatch")
		}
	}
	head, err := recoveryGit(candidate, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return id, err
	}
	id = RecoveryIdentity{Path: candidate, CommonDir: common, GitDir: admin, HEAD: strings.TrimSpace(string(head))}
	adminHead, err := recoveryRead(admin, "HEAD")
	if err != nil {
		return id, err
	}
	ref := strings.TrimSpace(string(adminHead.Data))
	if strings.HasPrefix(ref, "ref: refs/heads/") {
		id.Branch = strings.TrimPrefix(ref, "ref: refs/heads/")
	}
	return id, nil
}

// DiscoverRecoveryPaths lists registration metadata only. It never opens a lane's
// index or working files; callers must authorize each path before inspection.
func DiscoverRecoveryPaths(repository string) ([]string, error) {
	if err := recoveryExactPath(repository); err != nil {
		return nil, err
	}
	out, err := recoveryGit(repository, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, field := range bytes.Split(out, []byte{0}) {
		if !bytes.HasPrefix(field, []byte("worktree ")) {
			continue
		}
		if len(paths) >= 100 {
			return nil, errors.New("recovery inventory exceeds 100 worktrees")
		}
		path := string(field[len("worktree "):])
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("invalid registered worktree path")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// InspectRecoveryWorktree fingerprints one already-authorized registered lane.
// It is not an authorization boundary: callers must authorize before calling.
func InspectRecoveryWorktree(repository, candidate string) (RecoveryIdentity, error) {
	return inspectRecovery(repository, candidate)
}

// DiscoverRecoveryWorktrees returns a complete bounded registered inventory, or
// an error. It does not walk arbitrary directories or infer abandoned ownership.
func DiscoverRecoveryWorktrees(repository string) ([]RecoveryIdentity, error) {
	if err := recoveryExactPath(repository); err != nil {
		return nil, err
	}
	out, err := recoveryGit(repository, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var result []RecoveryIdentity
	for _, field := range bytes.Split(out, []byte{0}) {
		if !bytes.HasPrefix(field, []byte("worktree ")) {
			continue
		}
		if len(result) >= 128 {
			return nil, errors.New("recovery inventory exceeds 128 worktrees")
		}
		id, err := inspectRecovery(repository, string(field[len("worktree "):]))
		if err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, nil
}

func recoveryName(name string) error {
	if name == "" || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\x00\\") {
		return errors.New("recovery requires exact relative file names")
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return errors.New("recovery refuses Git administration paths")
		}
	}
	return nil
}

func recoveryRead(root, name string) (recoveryFile, error) {
	f := recoveryFile{Name: name}
	if err := recoveryName(name); err != nil {
		return f, err
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	// Check every existing component before opening; never follow an existing link.
	for current := path; current != root; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return f, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return f, errors.New("recovery refuses symlinks")
		}
	}
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if !st.Mode().IsRegular() {
		return f, errors.New("recovery refuses special files and submodules")
	}
	if st.Size() > recoveryByteLimit {
		return f, errors.New("recovery file exceeds byte limit")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return f, err
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(name))
	if err != nil {
		return f, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(st, opened) {
		return f, errors.New("recovery file identity changed")
	}
	f.Data, err = io.ReadAll(io.LimitReader(file, recoveryByteLimit+1))
	if err != nil {
		return f, err
	}
	if len(f.Data) > recoveryByteLimit {
		return f, errors.New("recovery file exceeds byte limit")
	}
	f.Mode = st.Mode()
	return f, nil
}

func inspectRecovery(repository, candidate string) (RecoveryIdentity, error) {
	id, err := recoveryIdentity(repository, candidate)
	if err != nil {
		return id, err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%q%q%q%q", id.Path, id.CommonDir, id.GitDir, id.HEAD)
	for _, name := range []string{"HEAD", "index"} {
		f, err := recoveryRead(id.GitDir, name)
		if err != nil {
			return id, err
		}
		fmt.Fprintf(h, "%q:%d:", name, len(f.Data))
		h.Write(f.Data)
	}
	for _, args := range [][]string{{"ls-files", "--stage", "-z"}, {"diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-renames", "--cached", "HEAD", "--"}, {"diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-renames", "--"}} {
		out, err := recoveryGit(candidate, nil, args...)
		if err != nil {
			return id, err
		}
		if args[0] == "ls-files" {
			for _, entry := range bytes.Split(out, []byte{0}) {
				if len(entry) == 0 {
					continue
				}
				header := strings.SplitN(string(entry), "\t", 2)[0]
				if (!strings.HasPrefix(header, "100644 ") && !strings.HasPrefix(header, "100755 ")) || !strings.HasSuffix(header, " 0") {
					return id, errors.New("recovery refuses unmerged index, symlinks and submodules")
				}
			}
		}
		h.Write(out)
	}
	out, err := recoveryGit(candidate, nil, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return id, err
	}
	names := bytes.Split(out, []byte{0})
	if len(names) > recoveryFileLimit+1 {
		return id, errors.New("recovery file count exceeds limit")
	}
	total := 0
	for _, name := range names {
		if len(name) == 0 {
			continue
		}
		f, err := recoveryRead(candidate, string(name))
		if err != nil {
			return id, err
		}
		total += len(f.Data)
		if total > recoveryByteLimit {
			return id, errors.New("recovery snapshot exceeds byte limit")
		}
		fmt.Fprintf(h, "%q:%d:%d:", f.Name, f.Mode, len(f.Data))
		h.Write(f.Data)
	}
	end, err := recoveryIdentity(repository, candidate)
	if err != nil || end != id {
		return id, errors.New("recovery source identity drift")
	}
	id.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return id, nil
}

// SnapshotRecovery requires a previously authorized exact inventory identity.
// Entire-source freshness is checked even when only a subset is selected.
func SnapshotRecovery(repository string, expected RecoveryIdentity, selection RecoverySelection) (*RecoverySnapshot, error) {
	if len(selection.Commits) != 0 || len(selection.Patch) != 0 {
		return nil, errors.New("exact commit and arbitrary patch recovery are unsupported; select exact working files explicitly")
	}
	if len(selection.Files) == 0 || len(selection.Files) > recoveryFileLimit {
		return nil, errors.New("recovery requires bounded explicit file selection")
	}
	before, err := inspectRecovery(repository, expected.Path)
	if err != nil {
		return nil, err
	}
	if before != expected {
		return nil, errors.New("stale recovery source")
	}
	s := &RecoverySnapshot{Identity: before, repository: repository, selection: append([]string(nil), selection.Files...)}
	sort.Strings(s.selection)
	for i, name := range s.selection {
		if err := recoveryName(name); err != nil {
			return nil, err
		}
		if i > 0 && name == s.selection[i-1] {
			return nil, errors.New("duplicate recovery selection")
		}
	}
	for _, layer := range []struct {
		cached bool
		target *[]byte
	}{{true, &s.staged}, {false, &s.unstaged}} {
		args := []string{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-renames"}
		if layer.cached {
			args = append(args, "--cached", "HEAD")
		}
		args = append(args, "--")
		args = append(args, s.selection...)
		*layer.target, err = recoveryGit(expected.Path, nil, args...)
		if err != nil {
			return nil, err
		}
	}
	out, err := recoveryGit(expected.Path, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	untracked := map[string]bool{}
	for _, name := range bytes.Split(out, []byte{0}) {
		untracked[string(name)] = true
	}
	for _, name := range s.selection {
		if untracked[name] {
			f, err := recoveryRead(expected.Path, name)
			if err != nil {
				return nil, err
			}
			s.untracked = append(s.untracked, f)
			continue
		}
		tracked, err := recoveryGit(expected.Path, nil, "ls-files", "-z", "--error-unmatch", "--", name)
		if err == nil && string(tracked) != name+"\x00" {
			return nil, errors.New("recovery selection must name a file, not a directory")
		}
		if err != nil || len(tracked) == 0 {
			// A staged deletion no longer appears in the index.
			base, baseErr := recoveryGit(expected.Path, nil, "ls-tree", "-z", "HEAD", "--", name)
			if baseErr != nil || len(base) == 0 || (!bytes.HasPrefix(base, []byte("100644 ")) && !bytes.HasPrefix(base, []byte("100755 "))) || !bytes.HasSuffix(base, []byte("\t"+name+"\x00")) {
				return nil, fmt.Errorf("selection %q is not an exact tracked or nonignored untracked file", name)
			}
		}
	}
	after, err := inspectRecovery(repository, expected.Path)
	if err != nil || after != before {
		return nil, errors.New("recovery source changed during snapshot")
	}
	return s, nil
}

// CopyRecovery allocates through the existing managed allocator at source HEAD.
// The caller must hold exclusive admission until publication and must revalidate
// source/destination evidence at its durable publication boundary. This function
// never publishes authority and never automatically removes a failed allocation.
func (s *Service) CopyRecovery(snapshot *RecoverySnapshot, nameSeed, branch string) (RecoveryResult, error) {
	return s.CopyRecoveryJournaled(snapshot, nameSeed, branch, nil)
}

// CopyRecoveryJournaled records the exact destination before Git can create it.
// Allocation failures retain every resource: the ordinary allocator's destructive
// rollback is not appropriate for recovery, where another writer may have arrived.
func (s *Service) CopyRecoveryJournaled(snapshot *RecoverySnapshot, nameSeed, branch string, journal func(Allocation) error) (result RecoveryResult, err error) {
	if snapshot == nil {
		return result, errors.New("recovery snapshot required")
	}
	result.Source = snapshot.Identity
	defer func() {
		if err != nil {
			result.Diagnostic = err.Error()
		}
	}()
	current, err := inspectRecovery(snapshot.repository, snapshot.Identity.Path)
	if err != nil {
		return result, err
	}
	if current != snapshot.Identity {
		return result, errors.New("recovery source changed before allocation")
	}
	result.Allocation, err = s.allocateRecovery(snapshot, branch, journal)
	if err != nil {
		return result, err
	}
	dest := result.Allocation.WorkspacePath
	for _, layer := range []struct {
		data   []byte
		cached bool
	}{{snapshot.staged, true}, {snapshot.unstaged, false}} {
		if len(layer.data) == 0 {
			continue
		}
		args := []string{"apply", "--binary", "--whitespace=nowarn"}
		if layer.cached {
			args = append(args, "--index")
		}
		if _, err = recoveryGit(dest, layer.data, append(args, "--check", "-")...); err != nil {
			return result, err
		}
		if _, err = recoveryGit(dest, layer.data, append(args, "-")...); err != nil {
			return result, err
		}
	}
	destRoot, err := os.OpenRoot(dest)
	if err != nil {
		return result, err
	}
	defer destRoot.Close()
	for _, f := range snapshot.untracked {
		name := filepath.FromSlash(f.Name)
		if err = destRoot.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return result, err
		}
		var file *os.File
		file, err = destRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, f.Mode.Perm())
		if err != nil {
			return result, err
		}
		_, writeErr := file.Write(f.Data)
		modeErr := file.Chmod(f.Mode.Perm())
		err = errors.Join(writeErr, modeErr, file.Close())
		if err != nil {
			return result, err
		}
	}
	current, err = inspectRecovery(snapshot.repository, snapshot.Identity.Path)
	if err != nil {
		return result, err
	}
	if current != snapshot.Identity {
		return result, errors.New("recovery source changed during import; allocation retained, publication forbidden")
	}
	result.Destination, err = inspectRecovery(snapshot.repository, dest)
	return result, err
}

// ValidateRecoveryIdentity is the caller's final freshness gate under its
// exclusive publication admission. A fingerprint is evidence, not a writer lock.
func ValidateRecoveryIdentity(repository string, expected RecoveryIdentity) error {
	actual, err := inspectRecovery(repository, expected.Path)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("stale recovery identity")
	}
	return nil
}

// allocateRecovery intentionally has no cleanup branch. Its journal callback is
// durable before creation; even a partially failed Git command remains attributable.
func (s *Service) allocateRecovery(snapshot *RecoverySnapshot, branch string, journal func(Allocation) error) (Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := resolveRepositoryRoot(snapshot.repository)
	if err != nil {
		return Allocation{}, err
	}
	workspaceID, err := workspaceIdentityForRequestedBranch(branch)
	if err != nil {
		return Allocation{}, err
	}
	path, err := deterministicSessionWorktreePath(repo, workspaceID)
	if err != nil {
		return Allocation{}, err
	}
	a := Allocation{WorkspacePath: path, RepoRoot: repo, BaseBranch: snapshot.Identity.HEAD, BaseCommit: snapshot.Identity.HEAD, BranchName: branch, WorkspaceID: workspaceID}
	if _, err := os.Lstat(path); err == nil {
		return Allocation{}, errors.New("recovery destination already exists")
	} else if !os.IsNotExist(err) {
		return Allocation{}, err
	}
	exists, err := localBranchExists(repo, branch)
	if err != nil {
		return Allocation{}, err
	}
	if exists {
		return Allocation{}, errors.New("recovery branch already exists")
	}
	if journal != nil {
		if err := journal(a); err != nil {
			return a, err
		}
	}
	if err := ensureWorktreeParent(repo); err != nil {
		return a, err
	}
	if _, err := recoveryGit(repo, nil, "worktree", "add", "-b", branch, path, snapshot.Identity.HEAD); err != nil {
		return a, err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return a, err
	}
	return a, nil
}
