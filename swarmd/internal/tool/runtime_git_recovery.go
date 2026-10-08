package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/sandbox"

	"swarm/packages/swarmd/internal/gitenv"
)

// recoveryRepository deliberately requires catalog authority, not session metadata
// or caller-provided roots. Delegated mutation restrictions cannot be expanded.
func (r *Runtime) recoveryRepository(scope WorkspaceScope, path string) (string, error) {
	if r == nil || r.workspace == nil || scope.Principal.AccountScopeID == "" || scope.Principal.UserID == "" {
		return "", errors.New("explicit Git recovery requires account workspace authority")
	}
	if (scope.RejectScopeExpansion && !scope.ExplicitRepositoryRecovery) || scope.TaskHistoryOnly || len(scope.MutationScopes) != 0 || len(scope.ReadOnlyRoots) != 0 {
		return "", errors.New("explicit Git recovery is unavailable in a restricted delegated scope")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("recovery workspace_path must be absolute")
	}
	if err := recoveryEnvironment(); err != nil {
		return "", err
	}
	canonical, err := canonicalExistingPath(path)
	if err != nil || canonical != filepath.Clean(path) {
		return "", errors.New("recovery repository must be a canonical non-symlink path")
	}
	owned, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, canonical)
	if err != nil {
		return "", err
	}
	if !owned.Matched {
		return "", errors.New("recovery repository is not in the account workspace catalog; register the selected worktree first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultGitTimeout)
	defer cancel()
	root, err := manageSessionsGitOutput(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil || root != canonical {
		return "", errors.New("recovery requires the exact repository worktree root")
	}
	return canonical, nil
}

func recoveryOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func recoveryGuard(ctx context.Context, repo, branch, head string) error {
	if branch == "" || !recoveryOID(head) {
		return errors.New("recovery requires an explicit branch and full expected HEAD")
	}
	if strings.ContainsAny(branch, "\x00\r\n") {
		return errors.New("invalid recovery branch")
	}
	if _, err := runManageSessionsGit(ctx, repo, "check-ref-format", "refs/heads/"+branch); err != nil {
		return err
	}
	actualBranch, err := manageSessionsGitOutput(ctx, repo, "symbolic-ref", "--short", "HEAD")
	if err != nil || actualBranch != branch {
		return errors.New("recovery branch changed or is detached")
	}
	actualHead, err := manageSessionsGitOutput(ctx, repo, "rev-parse", "HEAD")
	if err != nil || actualHead != head {
		return errors.New("recovery HEAD is stale")
	}
	return nil
}

// The private index never consumes or replaces the user's index. Git's atomic
// ref transaction publishes both the commit and a request-bound retry receipt.
// No session/checkpoint write participates in this transaction.
func recoveryCommit(ctx context.Context, repo string, args map[string]any, principal string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultGitCommitTimeout)
	defer cancel()
	branch, head := stringValue(args["expected_branch"]), stringValue(args["expected_head"])
	message, key := stringValue(args["message"]), stringValue(args["request_id"])
	paths := stringSliceValue(args["files"])
	if key == "" || len(key) > 200 || message == "" || len(paths) == 0 || len(paths) > 100 || boolValue(args["all"]) {
		return "", errors.New("recovery requires request_id, message and 1..100 literal files; all is forbidden")
	}
	binding, _ := json.Marshal(struct {
		Repo, Branch, Head, Message, Principal string
		Files                                  []string
	}{repo, branch, head, message, principal, paths})
	digest := fmt.Sprintf("%x", sha256.Sum256(binding))
	ref := fmt.Sprintf("refs/swarm/recovery/commit/%x", sha256.Sum256([]byte(principal+"\x00"+key)))
	locks := acquireManageSessionsCommitLocks([]string{repo})
	defer releaseManageSessionsCommitLocks(locks)
	if prior, err := manageSessionsGitOutput(ctx, repo, "rev-parse", "--verify", ref); err == nil {
		body, err := manageSessionsGitOutput(ctx, repo, "show", "-s", "--format=%B", prior)
		if err != nil || !strings.HasSuffix(body, "Swarm-Recovery-Binding: "+digest) {
			return "", errors.New("request_id was already used for a different recovery request")
		}
		if _, err := runManageSessionsGit(ctx, repo, "merge-base", "--is-ancestor", prior, "refs/heads/"+branch); err != nil {
			return "", fmt.Errorf("recovery commit %s exists but is no longer on the requested branch", prior)
		}
		changed, err := manageSessionsGitOutput(ctx, repo, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-z", "-r", prior)
		if err != nil {
			return "", err
		}
		return recoveryCommitResult(repo, head, prior, nonEmptyNULPaths(changed), true)
	}
	if err := recoveryGuard(ctx, repo, branch, head); err != nil {
		return "", err
	}
	indexPath, err := manageSessionsGitOutput(ctx, repo, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	indexLock, err := os.OpenFile(indexPath+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("lock original index: %w", err)
	}
	defer func() {
		_ = indexLock.Close()
		_ = os.Remove(indexPath + ".lock")
	}()
	for _, marker := range []string{"rebase-merge", "rebase-apply", "sequencer"} {
		path, err := manageSessionsGitOutput(ctx, repo, "rev-parse", "--path-format=absolute", "--git-path", marker)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return "", errors.New("finish the active Git operation before recovery")
		}
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := manageSessionsGitOutput(ctx, repo, "rev-parse", "--verify", marker); err == nil {
			return "", errors.New("finish the active Git operation before recovery")
		}
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return "", err
	}
	defer root.Close()
	entries := make([]string, 0, len(paths))
	var totalBytes int64
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || path == "." || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.ContainsAny(path, ":*?[\\\x00\r\n") || strings.HasPrefix(path, "../") || path == ".." || path == ".git" || strings.HasPrefix(path, ".git/") || seen[path] {
			return "", fmt.Errorf("invalid or duplicate literal recovery file %q", path)
		}
		seen[path] = true
		current := repo
		for _, component := range strings.Split(path, "/") {
			current = filepath.Join(current, component)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break // tracked deletions are staged by Git below
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("recovery file %q traverses a symlink or unreadable component", path)
			}
			if current == filepath.Join(repo, path) && !info.Mode().IsRegular() {
				return "", fmt.Errorf("recovery selection %q is not a regular file", path)
			}
		}
		// Freeze bytes through an OS-rooted descriptor, never reopen mutable paths
		// through Git filters or follow an escaping symlink after inspection.
		file, err := openRecoverySourceFile(root, path)
		if os.IsNotExist(err) {
			entries = append(entries, "0 "+strings.Repeat("0", len(head))+"\t"+path+"\x00")
			continue
		}
		if err != nil {
			return "", err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
			file.Close()
			return "", errors.New("recovery file is not regular or exceeds 32 MiB")
		}
		contents, err := io.ReadAll(io.LimitReader(file, (32<<20)+1))
		file.Close()
		totalBytes += int64(len(contents))
		if err != nil || len(contents) > 32<<20 || totalBytes > 64<<20 {
			return "", errors.New("recovery file capture failed or exceeds 64 MiB total")
		}
		blob, err := runManageSessionsGitInput(ctx, repo, contents, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		mode := "100644"
		if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		entries = append(entries, mode+" "+strings.TrimSpace(string(blob))+"\t"+path+"\x00")
	}
	// The index must be reachable by Git inside the project's sandbox.
	tmp, err := sandbox.ScratchDir(repo, "swarm-recovery-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	gitInput := func(input string, argv ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs"}, argv...)...)
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = append(gitenv.FilterIdentityOverrides(os.Environ()), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
		out := newCappedBuffer(maxCommandOutput)
		cmd.Stdout, cmd.Stderr = out, out
		sandbox.Prepare(ctx, cmd)
		err := cmd.Run()
		if err != nil {
			return "", fmt.Errorf("recovery git %s: %w: %s", argv[0], err, out.String())
		}
		return strings.TrimSpace(out.String()), nil
	}
	git := func(argv ...string) (string, error) { return gitInput("", argv...) }
	if _, err := git("read-tree", head); err != nil {
		return "", err
	}
	if _, err := gitInput(strings.Join(entries, ""), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	changed, err := git("diff", "--cached", "--name-only", "--no-renames", "-z", head)
	if err != nil || changed == "" {
		return "", errors.New("recovery selection contains no changes")
	}
	committed := nonEmptyNULPaths(changed)
	for _, path := range committed {
		if !seen[path] {
			return "", errors.New("Git expanded recovery selection beyond reviewed files")
		}
	}
	tree, err := git("write-tree")
	if err != nil {
		return "", err
	}
	created, err := runManageSessionsGitInput(ctx, repo, []byte(message+"\n\nSwarm-Recovery-Binding: "+digest+"\n"), "commit-tree", tree, "-p", head)
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(created))
	if err := recoveryGuard(ctx, repo, branch, head); err != nil {
		return "", err
	}
	transaction := fmt.Sprintf("start\nupdate refs/heads/%s %s %s\ncreate %s %s\nprepare\ncommit\n", branch, result, head, ref, result)
	if _, err := runManageSessionsGitInput(ctx, repo, []byte(transaction), "update-ref", "--stdin"); err != nil {
		return "", err
	}
	return recoveryCommitResult(repo, head, result, committed, false)
}

func recoveryCommitResult(repo, before, after string, files []string, retry bool) (string, error) {
	return marshalManageSessions(map[string]any{"repository": repo, "before_head": before, "commit_hash": after, "files": files, "replayed": retry, "git_success": true, "index_unchanged": true, "session_state_unchanged": true, "note": "Original index preserved byte-for-byte; selected paths may appear staged against the new HEAD. Inspect before subsequent staging."})
}

// Recovery integration deliberately supports only ancestry-preserving fast
// forwards. Divergence requires explicit conflict resolution in a separate
// worktree; no automatic replay, reset, deletion or source mutation is allowed.
func (r *Runtime) recoveryIntegrate(scope WorkspaceScope, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGitCommitTimeout)
	defer cancel()
	source, err := r.recoveryRepository(scope, stringValue(args["workspace_path"]))
	if err != nil {
		return "", err
	}
	target, err := r.recoveryRepository(scope, stringValue(args["target_workspace_path"]))
	if err != nil {
		return "", err
	}
	if source == target {
		return "", errors.New("recovery source and destination must be distinct worktrees")
	}
	locks := acquireManageSessionsCommitLocks([]string{source, target})
	defer releaseManageSessionsCommitLocks(locks)
	sourceHead, targetHead := stringValue(args["source_head"]), stringValue(args["target_head"])
	sourceBranch, targetBranch := stringValue(args["source_branch"]), stringValue(args["target_branch"])
	commits := stringSliceValue(args["commits"])
	key := stringValue(args["request_id"])
	if key == "" || len(key) > 200 {
		return "", errors.New("integration recovery requires a stable request_id")
	}
	if len(commits) == 0 || len(commits) > 100 || sourceHead == targetHead || !recoveryOID(targetHead) {
		return "", errors.New("recovery requires 1..100 reviewed commits and distinct full source/destination HEADs")
	}
	if !recoveryOID(sourceHead) {
		return "", errors.New("invalid full source HEAD")
	}
	if err := recoveryGuard(ctx, source, sourceBranch, sourceHead); err != nil {
		return "", err
	}
	// Both roots must belong to the same actual Git repository; no fetch from
	// arbitrary caller-controlled URLs or repositories is performed.
	sourceCommon, err := manageSessionsGitOutput(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	targetCommon, err := manageSessionsGitOutput(ctx, target, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || targetCommon != sourceCommon {
		return "", errors.New("recovery integration requires linked worktrees of the same repository")
	}
	if _, err := runManageSessionsGit(ctx, target, "merge-base", "--is-ancestor", targetHead, sourceHead); err != nil {
		return "", errors.New("destination diverged; resolve in a separate worktree and review the resulting commits")
	}
	rangeText, err := manageSessionsGitOutput(ctx, target, "rev-list", "--reverse", "--max-count=101", targetHead+".."+sourceHead)
	if err != nil || strings.Join(strings.Fields(rangeText), "\n") != strings.Join(commits, "\n") {
		return "", errors.New("reviewed commits do not exactly match the bounded integration range")
	}
	binding, _ := json.Marshal([]any{source, target, sourceBranch, targetBranch, sourceHead, targetHead, commits})
	ref := fmt.Sprintf("refs/swarm/recovery/integrate/%x", sha256.Sum256([]byte(scope.Principal.AccountScopeID+"/"+scope.Principal.UserID+"\x00"+key)))
	if prior, err := manageSessionsGitOutput(ctx, target, "cat-file", "blob", ref); err == nil {
		if prior != string(binding) {
			return "", errors.New("request_id was already used for a different integration request")
		}
	} else {
		blob, err := runManageSessionsGitInput(ctx, target, binding, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		transaction := fmt.Sprintf("create %s %s\n", ref, strings.TrimSpace(string(blob)))
		if _, err := runManageSessionsGitInput(ctx, target, []byte(transaction), "update-ref", "--stdin"); err != nil {
			return "", err
		}
	}
	actual, err := manageSessionsGitOutput(ctx, target, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	_, ancestorErr := runManageSessionsGit(ctx, target, "merge-base", "--is-ancestor", sourceHead, actual)
	retry := ancestorErr == nil
	if retry {
		err = recoveryGuard(ctx, target, targetBranch, actual)
	} else {
		err = recoveryGuard(ctx, target, targetBranch, targetHead)
	}
	if err != nil {
		return "", err
	}
	status, err := manageSessionsGitOutput(ctx, target, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", errors.New("recovery destination must be clean; unrelated work is never overwritten")
	}
	if !retry {
		if err := recoveryGuard(ctx, target, targetBranch, targetHead); err != nil {
			return "", err
		}
		if _, err := runManageSessionsGit(ctx, target, "-c", "core.hooksPath="+os.DevNull, "merge", "--ff-only", "--no-autostash", "--no-edit", sourceHead); err != nil {
			return "", fmt.Errorf("fast-forward failed; inspect destination HEAD before retry: %w", err)
		}
	}
	if !retry {
		actual = sourceHead
	}
	if err := recoveryGuard(ctx, target, targetBranch, actual); err != nil {
		return "", fmt.Errorf("integration postcondition requires inspection: %w", err)
	}
	return r.recoveryEvidence(ctx, scope, args, target, map[string]any{"git_success": true, "integrated": true, "source_repository": source, "destination_repository": target, "destination_branch": targetBranch, "before_head": targetHead, "after_head": actual, "integrated_head": sourceHead, "commits": commits, "replayed": retry, "session_state_unchanged": true})
}

// An inherited Git selector must not redirect an explicitly authorized target.
func recoveryEnvironment() error {
	for _, key := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_REPLACE_REF_BASE"} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("explicit recovery refuses inherited %s", key)
		}
	}
	return nil
}
