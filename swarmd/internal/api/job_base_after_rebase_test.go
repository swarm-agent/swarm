package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// rebaseFixture is a folder with a dev branch and job clones of it (origin is
// the folder), as Swarm allocates them, plus a session for each job in the
// real store.
type rebaseFixture struct {
	t      *testing.T
	f      reopenFixture
	folder string
	root   string
}

func newRebaseFixture(t *testing.T) rebaseFixture {
	t.Helper()
	f := newReopenFixture(t)
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	writeRebaseFile(t, folder, "README", "readme\n")
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "root")
	writeRebaseFile(t, folder, "base.txt", "base\n")
	outboxPinGit(t, folder, "add", ".")
	outboxPinGit(t, folder, "commit", "-q", "-m", "base")
	return rebaseFixture{t: t, f: f, folder: folder, root: outboxPinGit(t, folder, "rev-parse", "HEAD~1")}
}

func writeRebaseFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// job clones the folder at its current dev, commits the job's own work on
// agent/<name>, and records the job's session with the base it was created
// on, as allocation does.
func (r rebaseFixture) job(name string) (dir, base string) {
	r.t.Helper()
	dir = filepath.Join(r.t.TempDir(), name)
	outboxPinGit(r.t, filepath.Dir(dir), "clone", "-q", r.folder, dir)
	outboxPinGit(r.t, dir, "checkout", "-q", "-b", "agent/"+name)
	base = outboxPinGit(r.t, dir, "rev-parse", "HEAD")
	writeRebaseFile(r.t, dir, name+".go", "package "+name+"\n")
	outboxPinGit(r.t, dir, "add", ".")
	outboxPinGit(r.t, dir, "commit", "-q", "-m", name+" work")
	p := testPrincipal()
	if err := r.f.server.sessions.Store().CreateSession(pebblestore.SessionSnapshot{ID: "sess-" + name, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		WorkspacePath: r.folder, Title: "Coder job", WorktreeEnabled: true, WorktreeRootPath: dir, WorktreeBranch: "agent/" + name, WorktreeBaseBranch: "dev",
		Metadata: map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": r.folder}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		r.t.Fatal(err)
	}
	return dir, base
}

// land commits a change on the folder's dev, as another job's delivery does.
func (r rebaseFixture) land(file, message string) string {
	r.t.Helper()
	writeRebaseFile(r.t, r.folder, file, message+"\n")
	outboxPinGit(r.t, r.folder, "add", ".")
	outboxPinGit(r.t, r.folder, "commit", "-q", "-m", message)
	return outboxPinGit(r.t, r.folder, "rev-parse", "HEAD")
}

// rebase is what the coder does when told to rebase its own commits (those
// after from) onto the new dev.
func (r rebaseFixture) rebase(dir, from string) string {
	r.t.Helper()
	outboxPinGit(r.t, dir, "fetch", "-q", "origin")
	outboxPinGit(r.t, dir, "rebase", "-q", "--onto", "origin/dev", from)
	return outboxPinGit(r.t, dir, "rev-parse", "HEAD")
}

// changes is swarm_get_changes for the job's session. The session is read
// from the real store; only the V3 session route's projection layer is
// replaced by that read, since these job sessions are recorded the way
// worker allocation records them (a store snapshot, no V3 creation event).
func (r rebaseFixture) changes(name string) map[string]any {
	r.t.Helper()
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
	sessionRoute := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id := strings.TrimPrefix(req.URL.Path, "/v3/sessions/")
		session, found, err := r.f.server.sessions.Store().GetSession(id)
		if err != nil || !found || req.Method != http.MethodGet {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"session": session})
	})
	call := &controlMCPCall{request: req, next: sessionRoute, server: r.f.server}
	out, err := controlMCPGetChanges(call, map[string]any{"session_id": "sess-" + name})
	if err != nil {
		r.t.Fatalf("swarm_get_changes %s: %v", name, err)
	}
	return controlMCPMap(out)
}

func onlyCommit(t *testing.T, changes map[string]any, head, subject string) {
	t.Helper()
	commits, _ := changes["commits"].([]string)
	if len(commits) != 1 || !strings.HasPrefix(head, strings.Fields(commits[0])[0]) || !strings.HasSuffix(commits[0], " "+subject) {
		t.Fatalf("commits = %q, want only the job's own %q at %s", commits, subject, head)
	}
}

// Requirement: a job whose head was rebased onto a newer target branch
// after it was created reports that newer base: the merge-base of its head
// and the folder's current target branch, when that lies past (or replaces)
// the recorded base. Its commit list (what swarm_get_job's result, its
// base_commit and the automation result are built from) then holds only the
// job's own commits. A job that was not rebased keeps its recorded base even
// after the target moves on; a job rebased onto a rewritten target uses the
// rewritten target.
// Threat (forge run): a reviewer handed base..head with the recorded base
// sees commits already on dev in the range and blocks the job for
// "unrequested commits" it never made.
// Owners: controlMCPGetChanges (worktree.EffectiveTaskBase) over the real
// session store, the daemon's session route and real Git in a job clone of
// a real folder. No model provider is involved.
func TestJobRebasedOntoNewerTargetReportsTheNewBase(t *testing.T) {
	r := newRebaseFixture(t)

	// Rebased onto the new dev: the new dev is the base.
	rebasedDir, created := r.job("rebased")
	landed := r.land("landed.txt", "landed elsewhere")
	head := r.rebase(rebasedDir, created)
	changes := r.changes("rebased")
	if changes["base"] != landed || changes["base"] == created {
		t.Fatalf("rebased job base = %v, want the new dev %s (created on %s)", changes["base"], landed, created)
	}
	onlyCommit(t, changes, head, "rebased work")
	for _, file := range controlMCPList(changes["files"]) {
		if controlMCPMap(file)["path"] == "landed.txt" {
			t.Fatalf("a commit already on dev is in the job's changes: %+v", changes["files"])
		}
	}

	// Not rebased: dev moved on, but the job still forks at its recorded base.
	staleDir, staleBase := r.job("stale")
	r.land("later.txt", "later on dev")
	changes = r.changes("stale")
	if changes["base"] != staleBase {
		t.Fatalf("job that was not rebased reports base %v, want its recorded %s", changes["base"], staleBase)
	}
	onlyCommit(t, changes, outboxPinGit(t, staleDir, "rev-parse", "HEAD"), "stale work")

	// The target was rewritten below the recorded base and the job rebased
	// onto it: its head no longer descends from the recorded base.
	rewrittenDir, rewrittenBase := r.job("rewritten")
	outboxPinGit(t, r.folder, "reset", "-q", "--hard", r.root)
	newDev := r.land("rewrite.txt", "rewritten dev")
	head = r.rebase(rewrittenDir, rewrittenBase)
	changes = r.changes("rewritten")
	if changes["base"] != newDev || changes["base"] == rewrittenBase {
		t.Fatalf("job rebased onto a rewritten dev reports base %v, want %s", changes["base"], newDev)
	}
	onlyCommit(t, changes, head, "rewritten work")
}

// Requirement: the brief of a reviewer an automation agent step starts for a
// job rebased after creation reviews base..head from the new base, so its
// diff holds only the job's own change, and names the folder's target branch
// at its current tip, so "is this already on dev" can be answered from
// evidence.
// Threat (forge run): the reviewer reviews a864..HEAD, sees 77af (already
// landed) and blocks the job.
// Owners: Server.automationReviewerBrief (automationJobDiff,
// reviewerAutomationBrief) with real Git in a real job clone and folder.
func TestReviewerBriefOfARebasedJobDiffsFromTheNewBase(t *testing.T) {
	r := newRebaseFixture(t)
	dir, created := r.job("brief")
	landed := r.land("landed.txt", "landed elsewhere")
	head := r.rebase(dir, created)
	p := testPrincipal()
	result := jobautomation.Result{V: 1, JobID: "worker/" + r.f.workerID + "/run-brief", Kind: jobautomation.KindWorker, Status: jobautomation.StatusSucceeded,
		Git: &jobautomation.ResultGit{Branch: "agent/brief", BaseBranch: "dev", BaseCommit: created, Head: head, Worktree: dir}}
	brief := r.f.server.automationReviewerBrief(context.Background(), jobautomation.AgentRequest{Account: p.AccountScopeID, Folder: r.folder, Worker: "reviewer",
		Brief: "Review this job's exact head.", Result: result})
	for _, want := range []string{
		"## Diff against its base (" + landed + ".." + head + ")",
		"+package brief",
		"dev is at " + landed,
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("reviewer brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "landed elsewhere") || strings.Contains(brief, created+"..") {
		t.Fatalf("reviewer brief reviews from the stale base:\n%s", brief)
	}
}
