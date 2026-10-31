package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/jobautomation"
	"testing"
)

// Purpose: automationReviewerBrief must send only the since-head delta plus
// prior findings, never repeat the full diff. Real routed Git and the brief
// builder are the narrowest boundary proving the actual reviewer input.
func TestAutomationDeltaReviewAndHandoff(t *testing.T) {
	f := newReopenFixture(t)
	dir := t.TempDir()
	outboxPinGit(t, dir, "init", "-q", "-b", "dev")
	for _, text := range []string{"base", "old-review-change", "fresh-fix"} {
		if err := os.WriteFile(filepath.Join(dir, text), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		outboxPinGit(t, dir, "add", ".")
		outboxPinGit(t, dir, "commit", "-q", "-m", text)
	}
	head := outboxPinGit(t, dir, "rev-parse", "HEAD")
	since := outboxPinGit(t, dir, "rev-parse", "HEAD~1")
	base := outboxPinGit(t, dir, "rev-parse", "HEAD~2")
	brief := f.server.automationReviewerBrief(context.Background(), jobautomation.AgentRequest{Brief: "Review", Head: head, ReviewBase: since, PreviousFindings: "close the hole", Result: jobautomation.Result{Git: &jobautomation.ResultGit{Worktree: dir, Head: head, BaseCommit: base}}})
	if !strings.Contains(brief, since+".."+head) || !strings.Contains(brief, "close the hole") || !strings.Contains(brief, "every previous finding is closed") || !strings.Contains(brief, "+fresh-fix") || strings.Contains(brief, "+old-review-change") {
		t.Fatalf("wrong delta brief: %s", brief)
	}
	if got := f.server.automationHandoff(f.session.ID); got != "" {
		t.Fatalf("invented summary: %s", got)
	}
	content := toolHistory(t, "task_progress", map[string]any{"action": "done", "summary": "changed; tested; open"}, map[string]any{"status": "ok"})
	if _, _, _, err := f.server.sessions.AppendMessage(f.session.ID, "tool", content, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.server.automationHandoff(f.session.ID); got != "changed; tested; open" {
		t.Fatalf("summary lost: %s", got)
	}
}
