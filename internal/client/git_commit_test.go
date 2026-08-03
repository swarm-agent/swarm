package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommitWorkspaceChangesUsesBoundCanonicalEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspace/git/commit" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("session_id"); got != "session-1" {
			t.Fatalf("session_id = %q", got)
		}
		if got := r.Header.Get("X-Swarm-Token"); got != "test-token" {
			t.Fatalf("X-Swarm-Token = %q", got)
		}
		var body struct {
			WorkspacePath string `json:"workspace_path"`
			CWD           string `json:"cwd"`
			Message       string `json:"message"`
			All           bool   `json:"all"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.WorkspacePath != "/workspace" || body.CWD != "/workspace" || body.Message != "manual message" || !body.All {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(GitCommitResponse{OK: true, Summary: "git commit exited 0"})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	response, err := api.CommitWorkspaceChanges(context.Background(), "/workspace", "manual message", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Summary != "git commit exited 0" {
		t.Fatalf("response = %#v", response)
	}
}

func TestSuggestWorkspaceCommitMessageUsesExistingAIEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspace/git/commit/suggestion" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("session_id"); got != "session-ai" {
			t.Fatalf("session_id = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["workspace_path"] != "/workspace" || body["cwd"] != "/workspace" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(GitCommitSuggestionResponse{OK: true, Message: "generated message"})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	response, err := api.SuggestWorkspaceCommitMessage(context.Background(), "/workspace", "session-ai")
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Message != "generated message" {
		t.Fatalf("response = %#v", response)
	}
}

func TestGitCommitClientRejectsBlankInputsBeforeRequest(t *testing.T) {
	api := New("http://127.0.0.1:1")
	if _, err := api.CommitWorkspaceChanges(context.Background(), "/workspace", " ", "session"); err == nil {
		t.Fatal("expected blank message error")
	}
	if _, err := api.CommitWorkspaceChanges(context.Background(), " ", "message", "session"); err == nil {
		t.Fatal("expected blank workspace error")
	}
	if _, err := api.SuggestWorkspaceCommitMessage(context.Background(), " ", "session"); err == nil {
		t.Fatal("expected blank workspace error")
	}
}
