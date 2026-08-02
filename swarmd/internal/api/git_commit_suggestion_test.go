package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func TestDecodeWorkspaceGitCommitSuggestionStrict(t *testing.T) {
	message, err := decodeWorkspaceGitCommitSuggestion(`{"message":"feat: add AI commit suggestions"}`)
	if err != nil {
		t.Fatalf("decode valid suggestion: %v", err)
	}
	if message != "feat: add AI commit suggestions" {
		t.Fatalf("message = %q", message)
	}
	for name, raw := range map[string]string{
		"empty":         `{"message":"   "}`,
		"unknown":       `{"message":"ok","extra":true}`,
		"trailing":      `{"message":"ok"} {}`,
		"multiline":     `{"message":"feat: first\nsecond"}`,
		"missing":       `{}`,
		"bare markdown": "```json\n{\"message\":\"ok\"}\n```",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeWorkspaceGitCommitSuggestion(raw); err == nil {
				t.Fatalf("expected strict decode failure for %q", raw)
			}
		})
	}
}

func TestCollectWorkspaceGitSuggestionContextIncludesStagedUnstagedAndUntrackedWithoutMutation(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("write staged state: %v", err)
	}
	runGitCommitTestCommand(t, repo, "add", "note.txt")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("staged\nunstaged\n"), 0o644); err != nil {
		t.Fatalf("write unstaged state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatalf("write untracked state: %v", err)
	}
	beforeIndex := runGitCommitTestCommand(t, repo, "diff", "--cached")
	beforeHead := strings.TrimSpace(runGitCommitTestCommand(t, repo, "rev-parse", "HEAD"))

	changes, err := collectWorkspaceGitSuggestionContext(context.Background(), repo)
	if err != nil {
		t.Fatalf("collect suggestion context: %v", err)
	}
	if !strings.Contains(changes.Staged, "+staged") || !strings.Contains(changes.Unstaged, "+unstaged") {
		t.Fatalf("missing staged/unstaged content: %+v", changes)
	}
	if len(changes.Untracked) != 1 || changes.Untracked[0].Path != "new.txt" || changes.Untracked[0].Content != "new file\n" {
		t.Fatalf("untracked context = %+v", changes.Untracked)
	}
	if after := runGitCommitTestCommand(t, repo, "diff", "--cached"); after != beforeIndex {
		t.Fatal("suggestion context mutated the index")
	}
	if after := strings.TrimSpace(runGitCommitTestCommand(t, repo, "rev-parse", "HEAD")); after != beforeHead {
		t.Fatal("suggestion context created or changed a commit")
	}
}

func TestCollectWorkspaceGitSuggestionContextRejectsCleanConflictAndOversizedUntracked(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		repo := initGitCommitTestRepo(t)
		if _, err := collectWorkspaceGitSuggestionContext(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "no staged, unstaged, or untracked changes") {
			t.Fatalf("clean error = %v", err)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		repo := initGitCommitTestRepo(t)
		baseBranch := strings.TrimSpace(runGitCommitTestCommand(t, repo, "branch", "--show-current"))
		runGitCommitTestCommand(t, repo, "checkout", "-b", "other")
		if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("other\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCommitTestCommand(t, repo, "commit", "-am", "other")
		runGitCommitTestCommand(t, repo, "checkout", baseBranch)
		if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("master\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCommitTestCommand(t, repo, "commit", "-am", "master")
		cmdOutput, mergeErr := runGitCommitTestCommandAllowError(repo, "merge", "other")
		if mergeErr == nil {
			t.Fatalf("expected conflict, merge output=%s", cmdOutput)
		}
		if _, err := collectWorkspaceGitSuggestionContext(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "unresolved conflict") {
			t.Fatalf("conflict error = %v", err)
		}
	})
	t.Run("oversized untracked", func(t *testing.T) {
		repo := initGitCommitTestRepo(t)
		body := strings.Repeat("x", maxWorkspaceGitSuggestionUntrackedFile+1)
		if err := os.WriteFile(filepath.Join(repo, "large.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := collectWorkspaceGitSuggestionContext(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "complete input is required") {
			t.Fatalf("oversized error = %v", err)
		}
	})
}

func TestInvokeConfiguredRouterOnceUsesOneToolFreeRequestAndAccountSettings(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"message":"feat: suggest commits"}`}}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Git workspace"}})

	response, err := server.invokeConfiguredRouterOnce(context.Background(), principal, "strict instructions", "untrusted diff", maxWorkspaceGitSuggestionOutputBytes)
	if err != nil {
		t.Fatalf("invoke Router once: %v", err)
	}
	if response.Text != `{"message":"feat: suggest commits"}` || runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("response=%+v calls=%d/%d", response, runner.createCalls, runner.streamingCalls)
	}
	request := runner.requests[0]
	if request.Model != "router-model" || request.Thinking != "high" || request.ServiceTier != "priority" || request.ToolChoice != "none" || len(request.Tools) != 0 || request.ToolInvoker != nil {
		t.Fatalf("provider request = %+v", request)
	}
	providerPrincipal, ok := identity.PrincipalFromContext(runner.contexts[0])
	if !ok || !reflect.DeepEqual(providerPrincipal, principal) {
		t.Fatalf("provider principal = %+v ok=%v, want %+v", providerPrincipal, ok, principal)
	}
}

func TestInvokeConfiguredRouterOnceDoesNotRetryProviderFailure(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	runner := &sessionRouterRecordingRunner{id: "recording", err: providerErr}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Git workspace"}})

	_, err := server.invokeConfiguredRouterOnce(context.Background(), principal, "strict instructions", "untrusted diff", maxWorkspaceGitSuggestionOutputBytes)
	if !errors.Is(err, providerErr) {
		t.Fatalf("provider error = %v", err)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("provider failure caused retry/fallback: %d/%d", runner.createCalls, runner.streamingCalls)
	}
}

func TestResolveGitCommitWorkspacePathRoutesOwnedWorkspaceAndRejectsOtherAccount(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording"}
	server, principal, entries := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/owned", "Owned", "Git workspace"}})

	resolved, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: entries[0].Path}, principal, "")
	if err != nil || resolved != entries[0].Path {
		t.Fatalf("owned workspace resolution = %q, err=%v", resolved, err)
	}
	other := principal
	other.AccountScopeID = "other-account"
	if _, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: entries[0].Path}, other, ""); err == nil {
		t.Fatal("expected account-owned path authorization failure")
	}
}

func runGitCommitTestCommandAllowError(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
