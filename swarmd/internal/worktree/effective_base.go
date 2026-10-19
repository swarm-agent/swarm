package worktree

import (
	"context"
	"errors"
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
//   - Let tip be the target branch's current tip: the folder's
//     refs/heads/<target> (when that commit is in the job's clone) and the
//     clone's own refs/remotes/origin/<target> (which a rebase fetched).
//     Take the newest merge-base of head and those tips.
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
	if clone == "" || !validCommitID(recorded) || !validCommitID(head) || !targetBranchName.MatchString(target) || strings.Contains(target, "..") {
		return recorded
	}
	var tips []string
	if folder != "" {
		if tip, err := readGit(ctx, folder, "rev-parse", "--verify", "--quiet", "refs/heads/"+target+"^{commit}"); err == nil && validCommitID(tip) {
			if _, err := readGit(ctx, clone, "cat-file", "-e", tip+"^{commit}"); err == nil {
				tips = append(tips, tip)
			}
		}
	}
	if tip, err := readGit(ctx, clone, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+target+"^{commit}"); err == nil && validCommitID(tip) {
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
	if best == "" || best == recorded {
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
	out, err := sandbox.TimedGitOutput(ctx, dir, nil, full...)
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
