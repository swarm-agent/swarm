package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"swarm/packages/swarmd/internal/workspace"
)

// Requirement: onboarding discloses OS identity only through the existing
// sensitive metadata gate. Threat: caller HOME/USER or query parameters could
// impersonate the daemon or leak its paths. The response builder is the narrowest
// layer asserting the serialized contract without provider or network access.
func TestOnboardingWorkspaceGuidanceUsesDaemonIdentityAndSensitiveGate(t *testing.T) {
	server := newLocalAuthTestServer(t)
	t.Setenv("PATH", t.TempDir()) // no live tailscale discovery
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USER", "caller-not-daemon")
	request := httptest.NewRequest("GET", "http://example.test/v1/onboarding?runtime_username=caller-not-daemon", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	if server.allowSensitiveOnboardingMetadata(request) {
		t.Fatal("unauthenticated remote request admitted")
	}
	public, err := server.onboardingResponseWithServeDetection(false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["workspace_guidance"]; ok {
		t.Fatal("public response leaked workspace guidance")
	}
	private, err := server.onboardingResponseWithServeDetection(true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := workspace.DaemonWorkspaceGuidance()
	if private.WorkspaceGuidance == nil || *private.WorkspaceGuidance != want || want.RuntimeUID != strconv.Itoa(os.Geteuid()) || want.RuntimeUsername == "caller-not-daemon" {
		t.Fatalf("incorrect runtime guidance: %+v", private.WorkspaceGuidance)
	}
	if want.SuggestedWorkspacePath != "" {
		if _, err := os.Lstat(want.SuggestedWorkspacePath); !os.IsNotExist(err) {
			t.Fatalf("read created suggested directory: %v", err)
		}
	}
	entries, err := os.ReadDir(fakeHome)
	if err != nil || len(entries) != 0 {
		t.Fatalf("response mutated caller home: entries=%v err=%v", entries, err)
	}
}

// Requirement: a real product-session bearer used by the non-browser TUI may
// retrieve guidance, but an absent/invalid bearer must not disclose it. Exercise
// the handler rather than bypassing authentication with a response-builder flag.
func TestOnboardingWorkspaceGuidanceProductBearer(t *testing.T) {
	server := newLocalAuthTestServer(t)
	t.Setenv("PATH", t.TempDir())
	issued, err := server.identitySessions.IssueForCurrentSelection()
	if err != nil || issued.Token == "" {
		t.Fatalf("bootstrap: %v", err)
	}
	for _, token := range []string{"", "invalid", issued.Token} {
		req := httptest.NewRequest("GET", "http://example.test/v1/onboarding", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		server.handleOnboarding(rec, req)
		var response onboardingResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if (response.WorkspaceGuidance != nil) != (token == issued.Token) {
			t.Fatal("guidance bearer gate mismatch")
		}
	}
}
