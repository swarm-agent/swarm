package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"strings"
	"swarm/packages/swarmd/internal/videogen"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: accepting ready media is an idempotent review mutation, never a
// second generation. Boundary: handleProjects accept route and account store.
// Negative cases prove unfinished/foreign tasks cannot be accepted or changed.
func TestMediaAcceptanceDoesNotRegenerate(t *testing.T) {
	server, store, p := setupDirectMediaTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	file := filepath.Join(t.TempDir(), "fixture.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:r=24", "-t", "1", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("encoder fixture: %v %s", err, output)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	generator := &storyReadyGenerator{data: data, preflight: videogen.NewService(pebblestore.NewAuthStore(store.Underlying()), nil, server.model)}
	server.SetVideoGenerationService(generator)
	project := &pebblestore.ProjectRecord{ID: "accept-project", AccountID: p.AccountScopeID, Name: "Review"}
	if err := store.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "accept-task", Title: "Assembled story", ProjectID: project.ID, AccountID: p.AccountScopeID, Agent: "video", OutcomeType: "video_story", Status: "in_progress", Model: "veo-3.1-generate-preview", DurationSeconds: 8, AspectRatio: "16:9", Resolution: "720p", Scenes: []pebblestore.ProjectTaskScene{{Prompt: "first"}, {Prompt: "second"}}, Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "cut", Kind: "video", Status: "generating"}}}
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	server.executeDirectMediaTask(p, project, task)
	task, _, err = store.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || task.Status != "needs_review" || len(generator.prompts) != 2 || task.Deliverables[0].MediaURL == "" || task.Deliverables[0].VideoProvenance != nil {
		t.Fatalf("story execution failed: err=%v task=%+v calls=%v", err, task, generator.prompts)
	}
	call := func(principal identity.Principal) int {
		req := httptest.NewRequest(http.MethodPost, "/v3/projects/"+project.ID+"/tasks/"+task.ID+"/accept", strings.NewReader("{}"))
		req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, principal))
		req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Scopes: []string{"projects:write"}}))
		w := httptest.NewRecorder()
		server.handleProjects(w, req)
		return w.Code
	}
	foreign := p
	foreign.AccountScopeID = "foreign"
	if status := call(foreign); status == http.StatusOK {
		t.Fatal("foreign task accepted")
	}
	for i := 0; i < 2; i++ {
		if status := call(p); status != http.StatusOK {
			t.Fatalf("accept status %d", status)
		}
	}
	current, _, err := store.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || current.Status != "completed" || current.Deliverables[0].Status != "accepted" || current.Deliverables[0].MediaURL != task.Deliverables[0].MediaURL || len(generator.prompts) != 2 {
		t.Fatalf("acceptance mutated output or dispatched: %v", err)
	}
	ref := pebblestore.ProjectTaskMediaRef{ID: "cut", Kind: "video", MediaType: "video/mp4", URL: "/v3/projects/" + project.ID + "/tasks/" + task.ID + "/deliverables/cut"}
	resolved, err := server.resolveSourceMediaRecord(ctx, p, ref, "video", project.ID)
	if err != nil || len(resolved.Bytes) == 0 {
		t.Fatalf("accepted source unavailable: %v", err)
	}
	if _, err := server.resolveSourceMediaRecord(ctx, foreign, ref, "video", project.ID); err == nil {
		t.Fatal("foreign accepted source readable")
	}
	current.Status = "in_progress"
	current.Deliverables[0].Status = "generating"
	if err := store.PutProjectTask(p.AccountScopeID, current); err != nil {
		t.Fatal(err)
	}
	if status := call(p); status != http.StatusConflict {
		t.Fatalf("unfinished status %d", status)
	}
	unchanged, _, _ := store.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if unchanged.Status != "in_progress" || unchanged.Deliverables[0].Status != "generating" || len(generator.prompts) != 2 {
		t.Fatal("rejected acceptance changed state")
	}
}

// Encoder-fixture provider; deliberately not live generation evidence.
type storyReadyGenerator struct {
	data      []byte
	prompts   []string
	preflight *videogen.Service
}

func (g *storyReadyGenerator) GenerateManagedVideo(_ context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	g.prompts = append(g.prompts, req.Prompt)
	return videogen.ManagedVideoResult{Bytes: g.data, MediaType: "video/mp4", Model: req.Model, Provider: "google", AspectRatio: req.AspectRatio, Resolution: req.Resolution}, nil
}

func (g *storyReadyGenerator) PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	return g.preflight.PreflightVideoOperation(ctx, req)
}
