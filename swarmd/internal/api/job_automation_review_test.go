package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
)

// toolHistory is a tool message as the run loop records it in a session.
func toolHistory(t *testing.T, tool string, args map[string]any, output map[string]any) string {
	t.Helper()
	encodedArgs, _ := json.Marshal(args)
	encodedOut, _ := json.Marshal(output)
	record, err := json.Marshal(map[string]any{"path_id": "tool.history.v3", "type": "tool_result", "tool_name": tool, "call_id": "call-" + tool,
		"arguments": string(encodedArgs), "output": string(encodedOut)})
	if err != nil {
		t.Fatal(err)
	}
	return string(record)
}

// Requirement: a reviewer job an agent step starts gets, in its brief, the
// reviewed job's original brief, its report, the shell commands the job
// actually ran with their exit codes (read from its session's tool calls,
// not from what the report claims) and the diff against its base, within a
// run's 64 KiB input.
// Threat: a reviewer that trusts the report's "tests pass" because it cannot
// see what really ran, or a brief that silently drops the evidence.
// Owners: Server.automationReviewerBrief (automationJobCommands over the
// real session messages, automationJobDiff with routed Git in a real
// checkout, reviewerAutomationBrief).
func TestReviewerBriefHasTheBriefReportCommandsWithExitCodesAndDiff(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	jobID := f.job(t, "run-review-brief", "succeeded", f.session.ID)
	for _, content := range []string{
		toolHistory(t, "bash", map[string]any{"command": "go test ./internal/upload/"}, map[string]any{"command": "go test ./internal/upload/", "exit_code": 0, "output": "ok"}),
		toolHistory(t, "read", map[string]any{"path": "upload.go"}, map[string]any{"content": "package upload"}),
		toolHistory(t, "bash", map[string]any{"command": "go vet ./internal/upload/"}, map[string]any{"command": "go vet ./internal/upload/", "exit_code": 1, "output": "vet: unused"}),
	} {
		if _, _, _, err := f.server.sessions.AppendMessage(f.session.ID, "tool", content, nil); err != nil {
			t.Fatal(err)
		}
	}
	checkout := t.TempDir()
	outboxPinGit(t, checkout, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(checkout, "upload.go"), []byte("package upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, checkout, "add", ".")
	outboxPinGit(t, checkout, "commit", "-q", "-m", "base")
	base := outboxPinGit(t, checkout, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(checkout, "upload.go"), []byte("package upload\n\nconst maxUploadsPerMinute = 10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, checkout, "commit", "-q", "-am", "add the limit")
	head := outboxPinGit(t, checkout, "rev-parse", "HEAD")

	result := jobautomation.Result{V: 1, JobID: jobID, Kind: jobautomation.KindWorker, Status: jobautomation.StatusSucceeded,
		Brief: "Add a rate limit of 10 uploads per minute to the upload route.", Report: "Added the limit. go test and go vet pass.",
		Git: &jobautomation.ResultGit{BaseCommit: base, Head: head, Worktree: checkout}}
	brief := f.server.automationReviewerBrief(context.Background(), jobautomation.AgentRequest{Account: p.AccountScopeID, Worker: "reviewer",
		Brief: "Review this job's exact head.", Result: result})

	for _, want := range []string{
		"Review this job's exact head.",
		"## The job's original brief\nAdd a rate limit of 10 uploads per minute to the upload route.",
		"## The job's report (what it says it did and tested)\nAdded the limit. go test and go vet pass.",
		"$ go test ./internal/upload/    [exit 0]",
		"$ go vet ./internal/upload/    [exit 1]",
		"## Diff against its base (" + base + ".." + head + ")",
		"+const maxUploadsPerMinute = 10",
		head,
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("reviewer brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "$ upload.go") || strings.Contains(brief, `"path"`) {
		t.Fatalf("a non-shell tool call was listed as a command:\n%s", brief)
	}
	if strings.Index(brief, "go test ./internal/upload/") > strings.Index(brief, "go vet ./internal/upload/") {
		t.Fatal("commands are not in the order they ran")
	}
	if len(brief) > 64<<10 {
		t.Fatalf("brief is %d bytes, over a run's 64 KiB input", len(brief))
	}

	// A huge diff is cut inside the input bound and says how to read the rest.
	big := strings.Repeat("// padding line for a large diff\n", 4000)
	if err := os.WriteFile(filepath.Join(checkout, "big.go"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, checkout, "add", ".")
	outboxPinGit(t, checkout, "commit", "-q", "-m", "big")
	result.Git.Head = outboxPinGit(t, checkout, "rev-parse", "HEAD")
	brief = f.server.automationReviewerBrief(context.Background(), jobautomation.AgentRequest{Account: p.AccountScopeID, Brief: "Review.", Result: result})
	if len(brief) > 64<<10 || !strings.Contains(brief, "the diff is cut here") {
		t.Fatalf("large diff brief = %d bytes, cut note %v", len(brief), strings.Contains(brief, "the diff is cut here"))
	}
}

// Requirement: VerifyJobHead says whether a job's head descends from the
// delivery head, with Git in the folder after bringing both commits in from
// their own checkouts; anything else is refused.
// Threat: moving the delivery head to a reviewer's unrelated history.
// Owner: VerifyJobHead (importJobHead, git merge-base --is-ancestor).
func TestVerifyJobHeadAcceptsOnlyDescendants(t *testing.T) {
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	coder := filepath.Join(t.TempDir(), "coder")
	outboxPinGit(t, filepath.Dir(coder), "clone", "-q", folder, coder)
	outboxPinGit(t, coder, "commit", "-q", "--allow-empty", "-m", "coder work")
	jobHead := outboxPinGit(t, coder, "rev-parse", "HEAD")
	reviewer := filepath.Join(t.TempDir(), "reviewer")
	outboxPinGit(t, filepath.Dir(reviewer), "clone", "-q", coder, reviewer)
	outboxPinGit(t, reviewer, "commit", "-q", "--allow-empty", "-m", "reviewer security fix")
	fixHead := outboxPinGit(t, reviewer, "rev-parse", "HEAD")
	other := filepath.Join(t.TempDir(), "other")
	outboxPinGit(t, filepath.Dir(other), "clone", "-q", folder, other)
	outboxPinGit(t, other, "commit", "-q", "--allow-empty", "-m", "unrelated")
	otherHead := outboxPinGit(t, other, "rev-parse", "HEAD")

	ctx := context.Background()
	ok, err := VerifyJobHead(ctx, folder, jobautomation.HeadRef{Commit: jobHead, Worktree: coder}, jobautomation.HeadRef{Commit: fixHead, Worktree: reviewer})
	if err != nil || !ok {
		t.Fatalf("descendant = %v %v", ok, err)
	}
	ok, err = VerifyJobHead(ctx, folder, jobautomation.HeadRef{Commit: jobHead, Worktree: coder}, jobautomation.HeadRef{Commit: otherHead, Worktree: other})
	if err != nil || ok {
		t.Fatalf("non-descendant = %v %v", ok, err)
	}
	ok, err = VerifyJobHead(ctx, folder, jobautomation.HeadRef{Commit: fixHead, Worktree: reviewer}, jobautomation.HeadRef{Commit: jobHead, Worktree: coder})
	if err != nil || ok {
		t.Fatalf("an ancestor of the delivery head was accepted: %v %v", ok, err)
	}
	if _, err := VerifyJobHead(ctx, folder, jobautomation.HeadRef{Commit: "HEAD~1"}, jobautomation.HeadRef{Commit: fixHead, Worktree: reviewer}); err == nil {
		t.Fatal("a ref name was accepted as a commit id")
	}
}
