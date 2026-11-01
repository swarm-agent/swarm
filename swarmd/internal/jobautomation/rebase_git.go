package jobautomation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The daemon's own rebase of a delivery head onto its target branch
// (docs/automations.md, "Rebasing"). It is Git in the folder, run by the
// daemon (GitFunc routes it like all daemon Git on a folder), with hooks
// off, and it never touches a worktree, the target branch or a remote:
//
//   - each of the job's commits is replayed onto the target's tip with
//     git merge-tree --write-tree (a three-way merge whose base is the
//     commit's parent: a cherry-pick without a checkout) and git
//     commit-tree with the commit's own message and author;
//   - a conflict keeps the conflicted state as a commit (the files with
//     their conflict markers, on top of what was replayed so far) for a
//     resolver to check out, and stops;
//   - a resolution takes exactly the conflicted files: each is written as a
//     blob (git hash-object --stdin) and put in the conflicted state's tree
//     (git ls-tree / git mktree), refused while a conflict marker is left,
//     and the replay continues;
//   - nothing is written outside the folder's own repository: commit
//     messages and blobs go on standard input and no index file is used, so
//     it runs unchanged when daemon Git runs in the project's sandbox (which
//     mounts only the folder and its Git directory);
//   - refs move only by compare-and-swap (git update-ref <ref> <new> <old>).

// GitFunc runs Git in dir with extra environment variables (added to the
// daemon's own) and stdin (nil: none) as its standard input, and returns its
// standard output; err is an *exec.ExitError when Git exited non-zero (the
// output is still returned). The rebaser never names a path outside dir in
// its arguments or environment.
type GitFunc func(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error)

// Outcomes of a rebase (RebaseRecord.Outcome, ReplayResult.Status).
const (
	RebaseUpToDate   = "up_to_date"
	RebaseRebased    = "rebased"
	RebaseConflicted = "conflict"
	RebaseResolving  = "resolving"
	RebaseError      = "error"
	// RebaseSkipped: a rebase (automatic, a rebase step's, a request's)
	// replayed cleanly, but the job's
	// branch could not be moved to the new head while a deliver step to
	// merge or publish (which lands that branch) is still to run: the
	// delivery keeps its head, and the record says why.
	RebaseSkipped = "skipped"
)

// Bounds of one rebase.
const (
	maxRebaseCommits     = 200
	maxRebaseScanCommits = 1000
	maxRangeDiffBytes    = 16 << 10
	maxConflictDiffBytes = 12 << 10
	maxTargetCommits     = 20
	maxResolvedFileBytes = 8 << 20
)

// RebasedCommit maps one of the job's commits to its replay.
type RebasedCommit struct {
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Resolved is a commit whose replay conflicted: the resolver's edits
	// of the conflicted files are in it, so its patch differs.
	Resolved bool `json:"resolved,omitempty"`
	// Empty is a commit whose change the target already had: its replay
	// changes nothing.
	Empty bool `json:"empty,omitempty"`
	// InTarget is a commit of the old head that a rebased head (an agent
	// step's own rebase) dropped because the target already has the same
	// change.
	InTarget bool `json:"in_target,omitempty"`
}

// TargetCommit is a commit of the target branch.
type TargetCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject,omitempty"`
}

// RebaseConflict is where a replay stopped.
type RebaseConflict struct {
	// Commit is the job's commit whose replay conflicted, Subject its
	// subject, Files the files that conflicted.
	Commit  string   `json:"commit"`
	Subject string   `json:"subject,omitempty"`
	Files   []string `json:"files"`
	// TargetCommits are the target's commits since the job's base that
	// changed those files.
	TargetCommits []TargetCommit `json:"target_commits,omitempty"`
	// TargetDiff and JobDiff are both sides' changes of those files: the
	// target's since the job's base, and the stopped commit's own.
	TargetDiff string `json:"target_diff,omitempty"`
	JobDiff    string `json:"job_diff,omitempty"`
	// Checkout is the kept conflicted state: a commit (on no branch) of
	// the files with their conflict markers on top of Parent, what was
	// replayed so far. A resolver works in a checkout of it.
	Checkout string `json:"checkout"`
	Parent   string `json:"parent"`
	// ResolverJobs are the resolver agents' jobs, Rounds how many ran,
	// Pending the one running now; Reason why the last round's work was
	// not accepted; Outcome resolved or unresolved once it ended.
	ResolverJobs []string `json:"resolver_jobs,omitempty"`
	Rounds       int      `json:"rounds,omitempty"`
	Pending      string   `json:"pending,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Outcome      string   `json:"outcome,omitempty"`
}

// ReplayState is a replay stopped at a conflict: what it continues from.
type ReplayState struct {
	Base string `json:"base"`
	Onto string `json:"onto"`
	From string `json:"from"`
	// Done are the commits replayed so far; Remaining the job's commits
	// after the conflicted one, in order.
	Done      []RebasedCommit `json:"done,omitempty"`
	Remaining []string        `json:"remaining,omitempty"`
}

// ReplayResult is what a replay did.
type ReplayResult struct {
	Status string
	// Base is where the job's commits start (the merge base of the head
	// and the target), Onto the target tip, From the head replayed, To
	// the new head (rebased).
	Base, Onto, From, To string
	Commits              []RebasedCommit
	RangeDiff            string
	// Conflict and State are set when it stopped at a conflict.
	Conflict *RebaseConflict
	State    *ReplayState
	// InTarget is an up_to_date replay whose head the target already
	// contains (the head is an ancestor of the tip, or every one of the
	// job's commits changes nothing on it): nothing is moved.
	InTarget bool
}

// BranchUpdate is what became of the job's own branch after a rebase.
type BranchUpdate struct {
	Branch  string `json:"branch"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Updated bool   `json:"updated"`
	Reason  string `json:"reason,omitempty"`
	// CheckedOut is the worktree the branch is checked out in (it is then
	// moved only in that worktree: a task's by its branch advancer, a
	// worker job's own by MoveCheckedOutBranch; never by update-ref).
	CheckedOut string `json:"checked_out,omitempty"`
}

// CommitMapping is how a rebased head's commits map to an old head's.
type CommitMapping struct {
	Onto    string          `json:"onto"`
	Base    string          `json:"base"`
	Commits []RebasedCommit `json:"commits"`
	// Added are new commits that map to none of the old head's (the
	// agent's own commits on top of its rebase).
	Added []string `json:"added,omitempty"`
	// Unmatched are the old head's commits whose patch is in no commit of
	// the rebased head and not in the target.
	Unmatched []string `json:"unmatched,omitempty"`
}

// ResolveRefusedError is a resolver's work that was not accepted (a
// conflict marker left, a file that is not a regular file): the conflict
// stands and another round may try.
type ResolveRefusedError struct{ Reason string }

func (e *ResolveRefusedError) Error() string { return e.Reason }

// Rebaser is the daemon's rebase (GitRebaser).
type Rebaser interface {
	TargetTip(ctx context.Context, folder, branch string) (string, error)
	Replay(ctx context.Context, folder, head, onto string) (ReplayResult, error)
	Resume(ctx context.Context, folder string, state ReplayState, conflict RebaseConflict, resolved string) (ReplayResult, error)
	MapCommits(ctx context.Context, folder, oldHead, onto, newHead string) (CommitMapping, error)
	UpdateBranch(ctx context.Context, folder, branch, from, to string) BranchUpdate
	MoveCheckedOutBranch(ctx context.Context, worktree, branch, from, to string) (bool, string)
	KeepRef(ctx context.Context, folder, name, commit string) error
}

// GitRebaser is the Rebaser over Git in the folder.
type GitRebaser struct {
	Git GitFunc
}

func (g GitRebaser) git(ctx context.Context, folder string, env []string, args ...string) (string, error) {
	return g.gitIn(ctx, folder, env, nil, args...)
}

// gitIn is git with stdin as Git's standard input.
func (g GitRebaser) gitIn(ctx context.Context, folder string, env []string, stdin []byte, args ...string) (string, error) {
	if g.Git == nil {
		return "", errors.New("this daemon cannot run Git for a rebase")
	}
	base := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgSign=false", "-c", "core.quotePath=false"}
	out, err := g.Git(ctx, folder, env, stdin, append(base, args...)...)
	return string(out), err
}

func (g GitRebaser) gitLine(ctx context.Context, folder string, args ...string) (string, error) {
	out, err := g.git(ctx, folder, nil, args...)
	if err != nil {
		return "", gitError(args, out, err)
	}
	return strings.TrimSpace(out), nil
}

func gitError(args []string, out string, err error) error {
	name := "git"
	if len(args) > 0 {
		name += " " + args[0]
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("%s: %w: %s", name, err, truncate(string(exit.Stderr), 300))
	}
	return fmt.Errorf("%s: %w", name, err)
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// isAncestor reports whether ancestor is head or one of its ancestors.
func (g GitRebaser) isAncestor(ctx context.Context, folder, ancestor, head string) (bool, error) {
	out, err := g.git(ctx, folder, nil, "merge-base", "--is-ancestor", ancestor, head)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, gitError([]string{"merge-base"}, out, err)
}

// TargetTip is the commit refs/heads/<branch> names in the folder now.
func (g GitRebaser) TargetTip(ctx context.Context, folder, branch string) (string, error) {
	if !ValidBranchName(branch) {
		return "", fmt.Errorf("%q is not a branch name", branch)
	}
	tip, err := g.gitLine(ctx, folder, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("read the tip of %s: %w", branch, err)
	}
	if !isCommitID(tip) {
		return "", fmt.Errorf("refs/heads/%s does not name a commit", branch)
	}
	return tip, nil
}

// ValidBranchName refuses a branch name that is not a plain branch: empty,
// an option, or one Git would not accept under refs/heads.
func ValidBranchName(branch string) bool {
	if branch == "" || len(branch) > 250 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.Contains(branch, "..") || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	return !strings.ContainsAny(branch, " \t\r\n~^:?*[\\\x00\x7f")
}

// commits lists base..head oldest first, refusing merge commits and more
// than limit commits.
func (g GitRebaser) commits(ctx context.Context, folder, base, head string, limit int) ([]string, error) {
	merges, err := g.gitLine(ctx, folder, "rev-list", "--merges", "--max-count=1", base+".."+head)
	if err != nil {
		return nil, err
	}
	if merges != "" {
		return nil, fmt.Errorf("the commits from %s to %s include a merge commit (%s): only a linear history is replayed", short(base), short(head), short(merges))
	}
	out, err := g.gitLine(ctx, folder, "rev-list", "--reverse", fmt.Sprintf("--max-count=%d", limit+1), base+".."+head)
	if err != nil {
		return nil, err
	}
	list := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			list = append(list, line)
		}
	}
	if len(list) > limit {
		return nil, fmt.Errorf("more than %d commits from %s to %s", limit, short(base), short(head))
	}
	return list, nil
}

// Replay replays the commits of head that onto does not have onto onto.
func (g GitRebaser) Replay(ctx context.Context, folder, head, onto string) (ReplayResult, error) {
	if !isCommitID(head) || !isCommitID(onto) {
		return ReplayResult{}, errors.New("the head and the target tip must be commit ids")
	}
	res := ReplayResult{Onto: onto, From: head}
	upToDate, err := g.isAncestor(ctx, folder, onto, head)
	if err != nil {
		return res, err
	}
	if upToDate {
		res.Status, res.To = RebaseUpToDate, head
		return res, nil
	}
	base, err := g.gitLine(ctx, folder, "merge-base", onto, head)
	if err != nil || !isCommitID(base) {
		return res, fmt.Errorf("the head %s and the target tip %s share no history: %v", short(head), short(onto), err)
	}
	res.Base = base
	list, err := g.commits(ctx, folder, base, head, maxRebaseCommits)
	if err != nil {
		return res, err
	}
	// A commit whose change the target already has (the same patch id
	// among the target's commits since the base: it landed as another
	// commit) is not replayed, as git rebase skips it.
	var skipped []RebasedCommit
	if len(list) > 0 {
		if inTarget, err := g.targetPatchIDs(ctx, folder, base, onto); err == nil && len(inTarget) > 0 {
			if ids, err := g.patchIDs(ctx, folder, base+".."+head); err == nil {
				keep := list[:0:0]
				for _, c := range list {
					if id := ids[c]; id != "" && inTarget[id] {
						skipped = append(skipped, RebasedCommit{From: c, InTarget: true})
						continue
					}
					keep = append(keep, c)
				}
				list = keep
			}
		}
	}
	if len(list) == 0 {
		// The target already contains the head, or every one of its
		// changes: nothing to replay, and the head must not move to the
		// target's tip.
		res.Status, res.To, res.InTarget, res.Commits = RebaseUpToDate, head, true, skipped
		return res, nil
	}
	out, err := g.replay(ctx, folder, ReplayState{Base: base, Onto: onto, From: head, Remaining: list, Done: skipped}, onto)
	if err == nil && out.Status == RebaseRebased && allEmpty(out.Commits) {
		// Every change is in the target already (it landed by another
		// route): the head is not moved onto a string of empty commits.
		return ReplayResult{Status: RebaseUpToDate, Base: base, Onto: onto, From: head, To: head, InTarget: true}, nil
	}
	return out, err
}

// allEmpty reports whether every replayed commit changes nothing (or was
// skipped because the target has its change).
func allEmpty(commits []RebasedCommit) bool {
	for _, c := range commits {
		if !c.Empty && !c.InTarget {
			return false
		}
	}
	return true
}

// targetPatchIDs is the set of patch ids of the target's commits since base
// (at most maxRebaseScanCommits of them).
// Merge commits on the target are ignored (they carry no patch of their own).
func (g GitRebaser) targetPatchIDs(ctx context.Context, folder, base, onto string) (map[string]bool, error) {
	count, err := g.gitLine(ctx, folder, "rev-list", "--count", "--no-merges", base+".."+onto)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.Atoi(count); err != nil || n > maxRebaseScanCommits {
		return nil, fmt.Errorf("more than %d target commits to scan", maxRebaseScanCommits)
	}
	ids, err := g.patchIDs(ctx, folder, base+".."+onto)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			out[id] = true
		}
	}
	return out, nil
}

// replay replays state.Remaining on top of cur.
func (g GitRebaser) replay(ctx context.Context, folder string, state ReplayState, cur string) (ReplayResult, error) {
	res := ReplayResult{Base: state.Base, Onto: state.Onto, From: state.From}
	for len(state.Remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		commit := state.Remaining[0]
		meta, err := g.commitMeta(ctx, folder, commit)
		if err != nil {
			return res, err
		}
		// A commit with cur's tree and the replayed commit's parent: the
		// merge base of it and the commit is that parent, so the merge is
		// the commit's own change applied to cur (a cherry-pick).
		ours, err := g.commitTree(ctx, folder, cur+"^{tree}", meta.parent, meta, "swarm rebase: replay base\n")
		if err != nil {
			return res, err
		}
		tree, files, conflicted, err := g.mergeTree(ctx, folder, ours, commit)
		if err != nil {
			return res, err
		}
		if conflicted {
			checkout, err := g.commitTree(ctx, folder, tree, cur, meta, "swarm rebase: conflict replaying "+commit+" ("+meta.subject+")\n\nThe files listed below hold conflict markers:\n\n"+strings.Join(files, "\n")+"\n")
			if err != nil {
				return res, err
			}
			conflict := &RebaseConflict{Commit: commit, Subject: meta.subject, Files: files, Checkout: checkout, Parent: cur}
			g.describeConflict(ctx, folder, state, conflict)
			state.Remaining = state.Remaining[1:]
			res.Status, res.Conflict, res.State, res.Commits = RebaseConflicted, conflict, &state, state.Done
			return res, nil
		}
		next, err := g.commitTree(ctx, folder, tree, cur, meta, "")
		if err != nil {
			return res, err
		}
		parentTree, err := g.gitLine(ctx, folder, "rev-parse", cur+"^{tree}")
		if err != nil {
			return res, err
		}
		state.Done = append(state.Done, RebasedCommit{From: commit, To: next, Subject: meta.subject, Empty: tree == parentTree})
		state.Remaining = state.Remaining[1:]
		cur = next
	}
	if err := g.verifyReplay(ctx, folder, state.Done); err != nil {
		return res, err
	}
	res.Status, res.To, res.Commits = RebaseRebased, cur, state.Done
	if out, err := g.git(ctx, folder, nil, "range-diff", "--no-color", state.Base+".."+state.From, state.Onto+".."+cur); err == nil {
		res.RangeDiff = clip(out, maxRangeDiffBytes)
	}
	return res, nil
}

// mergeTree merges commit onto ours with git merge-tree --write-tree.
func (g GitRebaser) mergeTree(ctx context.Context, folder, ours, commit string) (tree string, files []string, conflicted bool, err error) {
	out, runErr := g.git(ctx, folder, nil, "merge-tree", "--write-tree", "-z", "--name-only", "--no-messages", ours, commit)
	code := 0
	if runErr != nil {
		code = exitCode(runErr)
		if code != 1 {
			return "", nil, false, fmt.Errorf("git merge-tree (Git 2.38 or later is needed for the daemon's rebase): %w", gitError([]string{"merge-tree"}, out, runErr))
		}
	}
	parts := strings.Split(out, "\x00")
	tree = strings.TrimSpace(parts[0])
	if !isCommitID(tree) {
		return "", nil, false, fmt.Errorf("git merge-tree wrote no tree: %q", truncate(out, 200))
	}
	if code == 0 {
		return tree, nil, false, nil
	}
	seen := map[string]bool{}
	for _, name := range parts[1:] {
		if name == "" {
			break // the end of the conflicted file list
		}
		if !seen[name] {
			seen[name] = true
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		return "", nil, false, errors.New("git merge-tree reported a conflict but named no file")
	}
	return tree, files, true, nil
}

type commitMeta struct {
	parent, subject                        string
	authorName, authorEmail, authorDate    string
	committerName, committerEmail, message string
}

func (g GitRebaser) commitMeta(ctx context.Context, folder, commit string) (commitMeta, error) {
	out, err := g.git(ctx, folder, nil, "log", "-1", "--no-decorate", "--date=raw", "--format=%P%x00%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%s%x00%B", commit, "--")
	if err != nil {
		return commitMeta{}, gitError([]string{"log"}, out, err)
	}
	fields := strings.SplitN(out, "\x00", 8)
	if len(fields) != 8 {
		return commitMeta{}, fmt.Errorf("could not read commit %s", short(commit))
	}
	parents := strings.Fields(fields[0])
	if len(parents) != 1 {
		return commitMeta{}, fmt.Errorf("commit %s has %d parents: only a commit with one parent is replayed", short(commit), len(parents))
	}
	return commitMeta{parent: parents[0], authorName: fields[1], authorEmail: fields[2], authorDate: fields[3], committerName: fields[4], committerEmail: fields[5],
		subject: fields[6], message: strings.TrimRight(fields[7], "\n") + "\n"}, nil
}

// commitTree commits tree on parent with meta's author, committer identity
// and message (or message when set), never signing it.
func (g GitRebaser) commitTree(ctx context.Context, folder, tree, parent string, meta commitMeta, message string) (string, error) {
	if message == "" {
		message = meta.message
	}
	env := []string{"GIT_AUTHOR_NAME=" + meta.authorName, "GIT_AUTHOR_EMAIL=" + meta.authorEmail, "GIT_AUTHOR_DATE=" + meta.authorDate,
		"GIT_COMMITTER_NAME=" + meta.committerName, "GIT_COMMITTER_EMAIL=" + meta.committerEmail}
	// The message goes on standard input (commit-tree reads it there).
	out, err := g.gitIn(ctx, folder, env, []byte(message), "commit-tree", "--no-gpg-sign", tree, "-p", parent)
	if err != nil {
		return "", gitError([]string{"commit-tree"}, out, err)
	}
	commit := strings.TrimSpace(out)
	if !isCommitID(commit) {
		return "", errors.New("git commit-tree wrote no commit")
	}
	return commit, nil
}

// describeConflict adds the target commits that changed the conflicted
// files and both sides' diffs of them. What cannot be read stays empty.
func (g GitRebaser) describeConflict(ctx context.Context, folder string, state ReplayState, c *RebaseConflict) {
	paths := append([]string{"--"}, c.Files...)
	if out, err := g.git(ctx, folder, nil, append([]string{"log", "--no-decorate", "--no-merges", fmt.Sprintf("--max-count=%d", maxTargetCommits), "--format=%H%x00%s", state.Base + ".." + state.Onto}, paths...)...); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			sha, subject, _ := strings.Cut(line, "\x00")
			if isCommitID(sha) {
				c.TargetCommits = append(c.TargetCommits, TargetCommit{SHA: sha, Subject: truncate(subject, 200)})
			}
		}
	}
	if out, err := g.git(ctx, folder, nil, append([]string{"diff", "--no-color", "--no-ext-diff", state.Base, state.Onto}, paths...)...); err == nil {
		c.TargetDiff = clip(out, maxConflictDiffBytes)
	}
	if out, err := g.git(ctx, folder, nil, append([]string{"diff", "--no-color", "--no-ext-diff", c.Commit + "^", c.Commit}, paths...)...); err == nil {
		c.JobDiff = clip(out, maxConflictDiffBytes)
	}
}

// Resume continues a replay stopped at conflict with the resolver's files:
// for each conflicted file, its content in resolved (a directory: the
// resolver's checkout of conflict.Checkout), or its removal when it is not
// there. Only the conflicted files are taken; a file still holding a
// conflict marker, a symbolic link or anything but a regular file is
// refused (ResolveRefusedError) and nothing is replayed.
func (g GitRebaser) Resume(ctx context.Context, folder string, state ReplayState, conflict RebaseConflict, resolved string) (ReplayResult, error) {
	if !isCommitID(conflict.Checkout) || !isCommitID(conflict.Parent) || !isCommitID(conflict.Commit) || len(conflict.Files) == 0 {
		return ReplayResult{}, errors.New("the conflict to continue from is incomplete")
	}
	root, err := resolvedRoot(resolved)
	if err != nil {
		return ReplayResult{}, err
	}
	changes := map[string]*treeEntry{}
	for _, name := range conflict.Files {
		data, mode, present, err := readResolvedFile(root, name)
		if err != nil {
			return ReplayResult{}, err
		}
		if !present {
			changes[name] = nil
			continue
		}
		if marker := conflictMarkerLine(data); marker > 0 {
			return ReplayResult{}, &ResolveRefusedError{Reason: fmt.Sprintf("%s still has a conflict marker on line %d", name, marker)}
		}
		out, err := g.gitIn(ctx, folder, nil, data, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return ReplayResult{}, gitError([]string{"hash-object"}, out, err)
		}
		blob := strings.TrimSpace(out)
		if !isCommitID(blob) {
			return ReplayResult{}, fmt.Errorf("git hash-object wrote no blob for %s", name)
		}
		changes[name] = &treeEntry{mode: mode, kind: "blob", oid: blob}
	}
	base, err := g.gitLine(ctx, folder, "rev-parse", conflict.Checkout+"^{tree}")
	if err != nil {
		return ReplayResult{}, err
	}
	tree, err := g.setPaths(ctx, folder, base, changes)
	if err != nil {
		return ReplayResult{}, err
	}
	if tree == "" {
		// Every file was removed: the empty tree.
		out, err := g.gitIn(ctx, folder, nil, []byte{}, "mktree", "-z")
		if err != nil {
			return ReplayResult{}, gitError([]string{"mktree"}, out, err)
		}
		tree = strings.TrimSpace(out)
	}
	// Only the conflicted files may differ from the conflicted state.
	changed, err := g.gitLine(ctx, folder, "diff-tree", "-r", "--name-only", "--no-renames", conflict.Checkout+"^{tree}", tree)
	if err != nil {
		return ReplayResult{}, err
	}
	allowed := map[string]bool{}
	for _, name := range conflict.Files {
		allowed[name] = true
	}
	for _, name := range strings.Split(changed, "\n") {
		if name = strings.TrimSpace(name); name != "" && !allowed[name] {
			return ReplayResult{}, fmt.Errorf("the resolution changed %s, which did not conflict", name)
		}
	}
	meta, err := g.commitMeta(ctx, folder, conflict.Commit)
	if err != nil {
		return ReplayResult{}, err
	}
	next, err := g.commitTree(ctx, folder, tree, conflict.Parent, meta, "")
	if err != nil {
		return ReplayResult{}, err
	}
	state.Done = append(state.Done, RebasedCommit{From: conflict.Commit, To: next, Subject: meta.subject, Resolved: true})
	return g.replay(ctx, folder, state, next)
}

// treeEntry is one entry of a Git tree.
type treeEntry struct{ mode, kind, oid string }

// setPaths writes a tree: tree with each path of changes set to its entry
// (nil: removed), building the changed subtrees with git ls-tree and git
// mktree (nothing but the repository's own objects is written). A
// directory left empty is dropped.
func (g GitRebaser) setPaths(ctx context.Context, folder, tree string, changes map[string]*treeEntry) (string, error) {
	entries := map[string]treeEntry{}
	if tree != "" {
		out, err := g.git(ctx, folder, nil, "ls-tree", "-z", tree)
		if err != nil {
			return "", gitError([]string{"ls-tree"}, out, err)
		}
		for _, rec := range strings.Split(out, "\x00") {
			if rec == "" {
				continue
			}
			meta, name, ok := strings.Cut(rec, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 {
				return "", fmt.Errorf("git ls-tree: unexpected entry %q", truncate(rec, 120))
			}
			entries[name] = treeEntry{mode: fields[0], kind: fields[1], oid: fields[2]}
		}
	}
	nested := map[string]map[string]*treeEntry{}
	for path, entry := range changes {
		dir, rest, deeper := strings.Cut(path, "/")
		if dir == "" || dir == "." || dir == ".." || (deeper && rest == "") {
			return "", fmt.Errorf("%q is not a path in the tree", path)
		}
		if !deeper {
			if entry == nil {
				delete(entries, dir)
			} else {
				entries[dir] = *entry
			}
			continue
		}
		if nested[dir] == nil {
			nested[dir] = map[string]*treeEntry{}
		}
		nested[dir][rest] = entry
	}
	for dir, sub := range nested {
		base := ""
		if cur, ok := entries[dir]; ok && cur.kind == "tree" {
			base = cur.oid
		}
		oid, err := g.setPaths(ctx, folder, base, sub)
		if err != nil {
			return "", err
		}
		if oid == "" {
			delete(entries, dir)
		} else {
			entries[dir] = treeEntry{mode: "040000", kind: "tree", oid: oid}
		}
	}
	if len(entries) == 0 && tree != "" {
		return "", nil
	}
	var in strings.Builder
	for name, e := range entries {
		fmt.Fprintf(&in, "%s %s %s\t%s\x00", e.mode, e.kind, e.oid, name)
	}
	out, err := g.gitIn(ctx, folder, nil, []byte(in.String()), "mktree", "-z")
	if err != nil {
		return "", gitError([]string{"mktree"}, out, err)
	}
	oid := strings.TrimSpace(out)
	if !isCommitID(oid) {
		return "", errors.New("git mktree wrote no tree")
	}
	if len(entries) == 0 {
		return "", nil
	}
	return oid, nil
}

// resolvedRoot is the resolver's checkout, resolved through symbolic links.
func resolvedRoot(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return "", &ResolveRefusedError{Reason: "the resolver reported no checkout to take its files from"}
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", &ResolveRefusedError{Reason: "the resolver's checkout cannot be read: " + err.Error()}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", &ResolveRefusedError{Reason: "the resolver's checkout is not a directory"}
	}
	return root, nil
}

// readResolvedFile reads one conflicted file from the resolver's checkout:
// its content and mode, or present false when the resolver removed it. A
// path that leaves the checkout (through a symbolic link among its
// directories) or a file that is not a regular file is refused.
func readResolvedFile(root, name string) ([]byte, string, bool, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, "", false, &ResolveRefusedError{Reason: name + " is not a path inside the checkout"}
	}
	path := filepath.Join(root, clean)
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", false, nil
		}
		return nil, "", false, &ResolveRefusedError{Reason: name + " cannot be read: " + err.Error()}
	}
	if parent != root && !strings.HasPrefix(parent, root+string(filepath.Separator)) {
		return nil, "", false, &ResolveRefusedError{Reason: name + " is outside the resolver's checkout"}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, &ResolveRefusedError{Reason: name + " cannot be read: " + err.Error()}
	}
	if !info.Mode().IsRegular() {
		return nil, "", false, &ResolveRefusedError{Reason: name + " is not a regular file in the resolver's checkout"}
	}
	if info.Size() > maxResolvedFileBytes {
		return nil, "", false, &ResolveRefusedError{Reason: fmt.Sprintf("%s is larger than %d bytes", name, maxResolvedFileBytes)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false, &ResolveRefusedError{Reason: name + " cannot be read: " + err.Error()}
	}
	mode := "100644"
	if info.Mode().Perm()&0o111 != 0 {
		mode = "100755"
	}
	return data, mode, true, nil
}

// conflictMarkerLine is the first line (1-based) of data that starts a
// conflict marker (<<<<<<< or >>>>>>> or |||||||, alone or followed by a
// space), 0 when none.
func conflictMarkerLine(data []byte) int {
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		for _, marker := range [][]byte{[]byte("<<<<<<<"), []byte(">>>>>>>"), []byte("|||||||")} {
			if bytes.Equal(line, marker) || bytes.HasPrefix(line, append(append([]byte(nil), marker...), ' ')) {
				return i + 1
			}
		}
	}
	return 0
}

// verifyReplay checks that every replayed commit carries the same patch as
// the job's commit it replays (patchIDs), except a commit the resolver
// changed or one whose change the target already had.
func (g GitRebaser) verifyReplay(ctx context.Context, folder string, done []RebasedCommit) error {
	if len(done) == 0 {
		return nil
	}
	for _, c := range done {
		if c.Resolved || c.Empty || c.InTarget {
			continue
		}
		from, err := g.patchIDs(ctx, folder, c.From+"^!")
		if err != nil {
			return err
		}
		to, err := g.patchIDs(ctx, folder, c.To+"^!")
		if err != nil {
			return err
		}
		if from[c.From] != to[c.To] {
			return fmt.Errorf("the replay of %s does not carry the same change (patch id differs): not rebased", short(c.From))
		}
	}
	return nil
}

// patchIDs computes a patch id for each commit of rev (a range, or
// <commit>^! for one): a digest of the lines each file's diff adds and
// removes, with the file's name and mode changes and, for a binary file,
// its new content id, ignoring line numbers and context, so a change keeps
// its id when the lines around it moved. A commit that changes nothing has
// the empty id.
func (g GitRebaser) patchIDs(ctx context.Context, folder, rev string) (map[string]string, error) {
	out, err := g.git(ctx, folder, nil, "log", "--no-decorate", "--no-color", "--no-ext-diff", "--no-renames", "--no-merges", "-p", "-U0", "--full-index",
		"--format=%x01%H", rev, "--")
	if err != nil {
		return nil, gitError([]string{"log"}, out, err)
	}
	ids := map[string]string{}
	for _, chunk := range strings.Split(out, "\x01")[1:] {
		sha, body, _ := strings.Cut(chunk, "\n")
		sha = strings.TrimSpace(sha)
		if !isCommitID(sha) {
			continue
		}
		ids[sha] = patchID(body)
	}
	return ids, nil
}

// patchID digests one commit's -U0 diff (see patchIDs).
func patchID(diff string) string {
	sections := []string{}
	var cur *sha256Writer
	inHunk, binary := false, false
	newIndex := ""
	flush := func() {
		if cur == nil {
			return
		}
		if binary {
			cur.add("binary " + newIndex)
		}
		sections = append(sections, cur.sum())
		cur = nil
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &sha256Writer{h: sha256.New()}
			cur.add(line)
			inHunk, binary, newIndex = false, false, ""
		case cur == nil:
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk && strings.HasPrefix(line, "index "):
			if _, ids, ok := strings.Cut(line, " "); ok {
				ids, _, _ = strings.Cut(ids, " ")
				if _, after, ok := strings.Cut(ids, ".."); ok {
					newIndex = after
				}
			}
		case !inHunk && (strings.HasPrefix(line, "new file mode") || strings.HasPrefix(line, "deleted file mode") ||
			strings.HasPrefix(line, "old mode") || strings.HasPrefix(line, "new mode")):
			cur.add(line)
		case !inHunk && strings.HasPrefix(line, "Binary files "):
			binary = true
		case inHunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			cur.add(line)
		}
	}
	flush()
	if len(sections) == 0 {
		return ""
	}
	sort.Strings(sections)
	sum := sha256.Sum256([]byte(strings.Join(sections, "\n")))
	return hex.EncodeToString(sum[:])
}

type sha256Writer struct {
	h interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
}

func (w *sha256Writer) add(line string) {
	_, _ = w.h.Write([]byte(line))
	_, _ = w.h.Write([]byte{'\n'})
}

func (w *sha256Writer) sum() string { return hex.EncodeToString(w.h.Sum(nil)) }

// MapCommits maps a rebased head's commits to an old head's by patch id:
// newHead must descend from onto; the old head's commits are those onto
// does not have (from their merge base), newHead's those after onto. Each
// old commit maps to a distinct new commit with the same patch, or is in
// the target already (InTarget); the rest are Unmatched. New commits that
// map to none are Added.
func (g GitRebaser) MapCommits(ctx context.Context, folder, oldHead, onto, newHead string) (CommitMapping, error) {
	out := CommitMapping{Onto: onto}
	for _, c := range []string{oldHead, onto, newHead} {
		if !isCommitID(c) {
			return out, fmt.Errorf("%q is not a commit id", c)
		}
	}
	descends, err := g.isAncestor(ctx, folder, onto, newHead)
	if err != nil {
		return out, err
	}
	if !descends {
		return out, fmt.Errorf("%s does not descend from the target tip %s", short(newHead), short(onto))
	}
	base, err := g.gitLine(ctx, folder, "merge-base", oldHead, onto)
	if err != nil || !isCommitID(base) {
		return out, fmt.Errorf("the old head %s and the target tip %s share no history", short(oldHead), short(onto))
	}
	out.Base = base
	oldList, err := g.commits(ctx, folder, base, oldHead, maxRebaseCommits)
	if err != nil {
		return out, err
	}
	newList, err := g.commits(ctx, folder, onto, newHead, maxRebaseCommits)
	if err != nil {
		return out, err
	}
	oldIDs, err := g.patchIDs(ctx, folder, base+".."+oldHead)
	if err != nil {
		return out, err
	}
	newIDs, err := g.patchIDs(ctx, folder, onto+".."+newHead)
	if err != nil {
		return out, err
	}
	var targetIDs map[string]bool
	used := map[string]bool{}
	for _, from := range oldList {
		id := oldIDs[from]
		mapped := RebasedCommit{From: from}
		for _, to := range newList {
			if !used[to] && newIDs[to] == id {
				used[to], mapped.To = true, to
				break
			}
		}
		if mapped.To == "" && id != "" {
			if targetIDs == nil {
				targetIDs = map[string]bool{}
				if ids, err := g.targetPatchIDs(ctx, folder, base, onto); err == nil {
					targetIDs = ids
				}
			}
			mapped.InTarget = targetIDs[id]
		}
		if mapped.To == "" && !mapped.InTarget && id != "" {
			out.Unmatched = append(out.Unmatched, from)
		}
		if mapped.To == "" && id == "" {
			mapped.Empty = true
		}
		out.Commits = append(out.Commits, mapped)
	}
	for _, to := range newList {
		if !used[to] {
			out.Added = append(out.Added, to)
		}
	}
	return out, nil
}

// UpdateBranch moves refs/heads/<branch> from from to to by
// compare-and-swap, only while it is not checked out in a worktree (a
// checked-out branch is reported with CheckedOut and left alone: moving a
// ref under a worktree would leave it inconsistent).
func (g GitRebaser) UpdateBranch(ctx context.Context, folder, branch, from, to string) BranchUpdate {
	out := BranchUpdate{Branch: branch, From: from, To: to}
	if !ValidBranchName(branch) {
		out.Reason = "the job has no branch to move"
		return out
	}
	if !isCommitID(from) || !isCommitID(to) {
		out.Reason = "the heads are not commit ids"
		return out
	}
	cur, err := g.gitLine(ctx, folder, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil || !isCommitID(cur) {
		out.Reason = "the branch " + branch + " is not in the folder"
		return out
	}
	if cur == to {
		out.Updated = true
		return out
	}
	if cur != from {
		out.Reason = "the branch " + branch + " is at " + short(cur) + ", not at the head that was rebased (" + short(from) + "): it is not moved"
		return out
	}
	if list, err := g.gitLine(ctx, folder, "worktree", "list", "--porcelain"); err == nil {
		path := ""
		for _, line := range strings.Split(list, "\n") {
			if strings.HasPrefix(line, "worktree ") {
				path = strings.TrimPrefix(line, "worktree ")
			}
			if line == "branch refs/heads/"+branch {
				out.CheckedOut = path
				out.Reason = "the branch " + branch + " is checked out in " + path
				return out
			}
		}
	} else {
		out.Reason = "the folder's worktrees could not be listed: " + truncate(err.Error(), 200)
		return out
	}
	if _, err := g.gitLine(ctx, folder, "update-ref", "-m", "swarm: rebased onto the target branch", "refs/heads/"+branch, to, from); err != nil {
		out.Reason = "the branch " + branch + " moved meanwhile, so it was not updated: " + truncate(err.Error(), 200)
		return out
	}
	out.Updated = true
	return out
}

// MoveCheckedOutBranch moves branch, checked out in worktree (a job's own
// checkout, made with git worktree add -b), from from to to: only while the
// worktree is on branch, its HEAD is at from (the compare-and-swap) and it
// has no uncommitted or untracked changes, by git reset --keep, which
// refuses to lose anything. It reports whether the branch now names to,
// else why not. The worktree is the job's, and the job has ended while its
// delivery runs; a commit made there meanwhile moves HEAD off from and is
// left alone.
func (g GitRebaser) MoveCheckedOutBranch(ctx context.Context, worktree, branch, from, to string) (bool, string) {
	if !ValidBranchName(branch) || !isCommitID(from) || !isCommitID(to) {
		return false, "the branch or heads to move are not valid"
	}
	if worktree == "" || !filepath.IsAbs(worktree) {
		return false, "the job has no worktree of its own"
	}
	if cur, err := g.gitLine(ctx, worktree, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil || cur != branch {
		return false, "the job's worktree " + worktree + " is not on branch " + branch
	}
	head, err := g.gitLine(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return false, "the job's worktree head could not be read: " + truncate(err.Error(), 200)
	}
	if head == to {
		return true, ""
	}
	if head != from {
		return false, "the branch " + branch + " is at " + short(head) + ", not at the head that was rebased (" + short(from) + "): it is not moved"
	}
	status, err := g.gitLine(ctx, worktree, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return false, "the job's worktree status could not be read: " + truncate(err.Error(), 200)
	}
	if status != "" {
		return false, "the job's worktree " + worktree + " has uncommitted changes"
	}
	if _, err := g.gitLine(ctx, worktree, "reset", "--keep", "--quiet", to); err != nil {
		return false, "git reset --keep failed: " + truncate(err.Error(), 200)
	}
	if moved, err := g.gitLine(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || moved != to {
		return false, "the job's worktree did not reach the rebased head"
	}
	return true, ""
}

// KeepRef points refs/swarm/<name> at commit, so a rebased head or a kept
// conflict stays reachable.
func (g GitRebaser) KeepRef(ctx context.Context, folder, name, commit string) error {
	if !isCommitID(commit) || name == "" || strings.ContainsAny(name, " \t\r\n~^:?*[\\\x00") || strings.Contains(name, "..") {
		return errors.New("not a ref to keep")
	}
	_, err := g.gitLine(ctx, folder, "update-ref", "refs/swarm/"+name, commit)
	return err
}

// rebaseRefName is a ref name for a delivery's rebase n.
func rebaseRefName(kind, deliveryID string, n int) string {
	sum := sha256.Sum256([]byte(deliveryID))
	return fmt.Sprintf("%s/%s/%d", kind, hex.EncodeToString(sum[:10]), n)
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// clip keeps the first limit bytes of s, marking the cut.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d more bytes)\n", len(s)-cut)
}
