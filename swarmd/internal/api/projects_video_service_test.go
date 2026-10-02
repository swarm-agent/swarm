package api

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

type videoCredentialTransport func(*http.Request) (*http.Response, error)

func (f videoCredentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Authentication must use the canonical account header, never a URL query that
// net/http can include in persisted failure text.
func TestProjectVideoServiceCanonicalCredentials(t *testing.T) {
	// Requirement: Orchestrate uses the injected videogen.Service and its separate
	// canonical secret store, with the requesting account's credentials only.
	// Regression: resolveVideoGenerationService reconstructed auth from session
	// storage, losing the daemon's secret-store connection. A global override also
	// allowed one Server to change another Server's credential authority.
	// Authority: Server.SetVideoGenerationService/resolveVideoGenerationService,
	// videogen.Service.GenerateManagedVideo and AuthStore account-scoped lookup.
	// Layer: real stores and real videogen service; only HTTP is intercepted. No
	// provider requests, video generation, or real credentials are used.
	server, sessions, principal := setupDirectMediaTestServer(t)
	secrets, err := pebblestore.Open(filepath.Join(t.TempDir(), "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	auth := pebblestore.NewAuthStoreWithSecretStore(sessions.Underlying(), secrets)
	const canonicalKey = "fixture-canonical-video-key"
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{
		Provider: "google", AccountScopeID: principal.AccountScopeID,
		Type: pebblestore.AuthTypeAPI, APIKey: canonicalKey, SetActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	service := videogen.NewService(auth, nil, server.model)
	service.SetHTTPClient(&http.Client{Transport: videoCredentialTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("x-goog-api-key") != canonicalKey || req.URL.RawQuery != "" {
			t.Error("video request did not use the canonical secret")
		}
		if req.Method != http.MethodPost || req.URL.Path != "/v1beta/models/veo-3.1-generate-preview:predictLongRunning" {
			t.Error("unexpected video request route")
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"fixture provider rejection"}}`)),
			Request:    req,
		}, nil
	})})
	server.SetVideoGenerationService(service)
	resolved, err := server.resolveVideoGenerationService()
	if err != nil || resolved != service {
		t.Fatalf("injected service identity not preserved: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := videogen.ManagedVideoRequest{
		Model: "google:veo-3.1-generate-preview", Prompt: "Credential routing regression",
		Principal: principal, AspectRatio: "16:9", Resolution: "720p", DurationSeconds: 8,
	}
	result, err := resolved.GenerateManagedVideo(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "fixture provider rejection") || len(result.Bytes) != 0 || calls != 1 {
		t.Fatalf("expected canonical-auth request followed by honest provider failure; error=%v calls=%d bytes=%d", err, calls, len(result.Bytes))
	}

	for _, account := range []string{"another-account", ""} {
		req.Principal.AccountScopeID = account
		result, err = resolved.GenerateManagedVideo(ctx, req)
		if err == nil || calls != 1 || len(result.Bytes) != 0 {
			t.Fatalf("foreign or missing account must not reuse credentials or dispatch; error=%v calls=%d bytes=%d", err, calls, len(result.Bytes))
		}
	}

	other := &Server{}
	otherService := videogen.NewService(nil, nil, nil)
	other.SetVideoGenerationService(otherService)
	stillResolved, err := server.resolveVideoGenerationService()
	if err != nil || stillResolved != service {
		t.Fatal("another server replaced this server's canonical service")
	}
}

func TestProjectVideoServiceMissingInjectionFailsClosed(t *testing.T) {
	// Requirement: missing daemon wiring is a configuration error, never permission
	// to reconstruct credential authority from session storage. The real fixture
	// contains a session-store credential, so the retired fallback would succeed.
	// Authority/layer: Server.resolveVideoGenerationService with real session store;
	// assert no service is returned, including nil and explicitly cleared servers.
	server, _, _ := setupDirectMediaTestServer(t)
	for _, candidate := range []*Server{nil, {}, server} {
		service, err := candidate.resolveVideoGenerationService()
		if err == nil || err.Error() != "video generation service is not configured" || service != nil {
			t.Fatalf("missing wiring must fail closed: service=%T error=%v", service, err)
		}
	}
	server.SetVideoGenerationService(videogen.NewService(nil, nil, nil))
	server.SetVideoGenerationService(nil)
	if service, err := server.resolveVideoGenerationService(); err == nil || service != nil {
		t.Fatal("cleared wiring reconstructed a fallback service")
	}
}

func TestProjectVideoServiceMissingInjectionPersistsFailure(t *testing.T) {
	// Requirement: missing canonical video wiring must durably fail Orchestrate
	// work with no ready media, even when session storage contains a credential.
	// Authority: executeDirectMediaTask -> resolveVideoGenerationService and
	// updateProjectTaskWithRetry. Real temporary storage is the narrowest layer
	// proving the failure reaches the persisted task and every deliverable.
	server, sessions, principal := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{
		ID: "video-wiring-project", AccountID: principal.AccountScopeID, Name: "Video wiring",
	}
	if err := sessions.PutProject(principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID: "video-wiring-task", ProjectID: project.ID, AccountID: principal.AccountScopeID,
		Title: "Video wiring", Description: "No generation without canonical wiring",
		Agent: "video", Status: "in_progress", Model: "google:veo-3.1-generate-preview",
		AspectRatio: "16:9", Resolution: "720p", DurationSeconds: 8, VariantCount: 2,
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "video-first", Kind: "video", Status: "generating"},
			{ID: "video-second", Kind: "video", Status: "generating"},
		},
	}
	if err := sessions.PutProjectTask(principal.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	server.executeDirectMediaTask(principal, project, task)
	updated, ok, err := sessions.GetProjectTask(principal.AccountScopeID, project.ID, task.ID)
	if err != nil || !ok {
		t.Fatalf("read task: found=%v error=%v", ok, err)
	}
	if updated.Status != "failed" || updated.LastError != "video generation service is not configured" || updated.ActionNeeded == "" {
		t.Fatalf("missing wiring was not persisted as actionable failure: status=%s error=%s", updated.Status, updated.LastError)
	}
	if len(updated.Deliverables) != 2 {
		t.Fatalf("expected both failed deliverables, got %d", len(updated.Deliverables))
	}
	for _, deliverable := range updated.Deliverables {
		if deliverable.Status != "failed" || deliverable.MediaURL != "" {
			t.Fatalf("missing wiring must not publish media: status=%s", deliverable.Status)
		}
	}
}
