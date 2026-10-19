package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
)

// effectiveBaseGitTimeout bounds each read-only Git call EffectiveTaskBase
// makes.
const effectiveBaseGitTimeout = 30 * time.Second

var targetBranchName = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$`)

// EffectiveTaskBase is the base a job's commits are listed and reviewed
// from. A job records the target branch's tip when it is created; a coder
// told to rebase onto a newer target (or to merge it in) moves its head past
// that, and base..head would then list commits that are already on the
// target. The rule:
//
//   - Let tip be the target branch's current tip, taken only from
//     daemon-authoritative state: the folder's refs/heads/<target> (when
//     that commit is in the job's clone). The clone belongs to the coder and
//     its refs are writable to agent Bash, so the clone's own
//     refs/remotes/origin/<target> (which a rebase fetched) is a candidate
//     only when the folder's target branch contains it. Take the newest
//     merge-base of head and those tips, and accept it only when the
//     folder's target branch contains it too: a commit not on the target
//     never becomes the base, so no commit of the job's own can drop out of
//     its range.
//   - That merge-base is the base when it descends from the recorded base
//     (the job was rebased onto, or merged, a newer target), or when head
//     no longer descends from the recorded base at all (the target was
//     rewritten and the job rebased onto it).
//   - Otherwise (the job was not rebased, or nothing is known about the
//     target) the recorded base stays.
//
// It only reads: no ref or object is written in the clone or the folder.
// Errors reading the target are not fatal: the recorded base is returned.
func EffectiveTaskBase(ctx context.Context, clone, folder, target, recorded, head string) string {
	clone, folder, target = strings.TrimSpace(clone), strings.TrimSpace(folder), strings.TrimSpace(target)
	recorded, head = strings.TrimSpace(recorded), strings.TrimSpace(head)
	if clone == "" || folder == "" || !validCommitID(recorded) || !validCommitID(head) || !targetBranchName.MatchString(target) || strings.Contains(target, "..") {
		return recorded
	}
	folderTip := TargetBranchTip(ctx, folder, target)
	if folderTip == "" {
		return recorded
	}
	// onTarget reports whether the folder's target branch contains commit,
	// judged in the folder: a commit the coder made, or a ref it moved off
	// the target, is not there.
	onTarget := func(commit string) bool {
		contained, err := isAncestor(ctx, folder, commit, folderTip)
		return err == nil && contained
	}
	var tips []string
	if _, err := readGit(ctx, clone, "cat-file", "-e", folderTip+"^{commit}"); err == nil {
		tips = append(tips, folderTip)
	}
	if tip, err := readGit(ctx, clone, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+target+"^{commit}"); err == nil && validCommitID(tip) && tip != folderTip && onTarget(tip) {
		tips = append(tips, tip)
	}
	best := ""
	for _, tip := range tips {
		mergeBase, err := readGit(ctx, clone, "merge-base", head, tip)
		if err != nil || !validCommitID(mergeBase) {
			continue
		}
		if best == "" {
			best = mergeBase
		} else if newer, err := isAncestor(ctx, clone, best, mergeBase); err == nil && newer {
			best = mergeBase
		}
	}
	if best == "" || best == recorded || !onTarget(best) {
		return recorded
	}
	if past, err := isAncestor(ctx, clone, recorded, best); err == nil && past {
		return best
	}
	if descends, err := isAncestor(ctx, clone, recorded, head); err == nil && !descends {
		return best
	}
	return recorded
}

// TargetBranchTip is the folder's refs/heads/<target> commit, or "".
func TargetBranchTip(ctx context.Context, folder, target string) string {
	target = strings.TrimSpace(target)
	if strings.TrimSpace(folder) == "" || !targetBranchName.MatchString(target) || strings.Contains(target, "..") {
		return ""
	}
	tip, err := readGit(ctx, folder, "rev-parse", "--verify", "--quiet", "refs/heads/"+target+"^{commit}")
	if err != nil || !validCommitID(tip) {
		return ""
	}
	return tip
}

func readGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx = sandbox.WithGitCommandTimeout(ctx, effectiveBaseGitTimeout)
	full := append([]string{"-c", "safe.directory=" + dir, "-c", "core.fsmonitor=false"}, args...)
	// Replace refs and a grafts file in the coder's clone could rewrite the
	// ancestry merge-base reads; ignore both.
	env := append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE="+os.DevNull)
	out, err := sandbox.TimedGitOutput(ctx, dir, env, full...)
	return strings.TrimSpace(string(out)), err
}

// isAncestor reports git merge-base --is-ancestor ancestor head in dir.
func isAncestor(ctx context.Context, dir, ancestor, head string) (bool, error) {
	_, err := readGit(ctx, dir, "merge-base", "--is-ancestor", ancestor, head)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
