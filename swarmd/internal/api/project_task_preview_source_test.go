package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: real local artifact bytes must produce a bounded authenticated card
// derivative. Warm reads must not decode canonical task bodies, and deleted or
// foreign sources must not escape through the cache. Real artifact authority +
// HTTP handler is the narrowest layer proving all these boundaries together.
func TestProjectPreviewPinnedArtifact(t *testing.T) {
	server, svc, registry, _, _, _, _ := newArtifactSessionFixture(t, "workspace.txt", "workspace")
	p := testPrincipal()
	authority := artifact.NewAuthority(registry, svc)
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 960, 640))); err != nil {
		t.Fatal(err)
	}
	principal := artifact.Principal{SessionID: "artifact-session", AccountScopeID: p.AccountScopeID, UserID: p.UserID}
	v, err := authority.Create(context.Background(), principal, artifact.CreateInput{RequestID: "preview-create", CollectionID: "preview-collection", CollectionName: "Preview", VariantID: "preview-variant", Filename: "source.png", MediaType: "image/png", Role: pebblestore.SessionArtifactRoleRenderOnly, Presentation: pebblestore.SessionArtifactPresentation{Kind: "image", Previewable: true}, Body: original.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	db := svc.Store()
	project := &pebblestore.ProjectRecord{ID: "project", Name: "Preview"}
	if err = db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("/v3/sessions/%s/artifacts/%s?event_seq=%d", v.SessionID, v.ID, v.EventSeq)
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, Title: "Preview", Agent: "image", Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "d", Kind: "image", Status: "ready", MediaURL: source}}}
	if err = db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	summary, found, err := db.GetProjectTaskSummary(p.AccountScopeID, project.ID, task.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	reference := summary.Deliverables[0].Thumbnail + "&preview=1"
	read := func(account string) *httptest.ResponseRecorder {
		principal := p
		principal.AccountScopeID = account
		req := httptest.NewRequest(http.MethodGet, reference, nil)
		req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: account, UserID: p.UserID, Scopes: []string{"projects:read"}}))
		w := httptest.NewRecorder()
		server.handleProjectDeliverableContent(w, req, principal, project.ID, task.ID, "d")
		return w
	}
	first := read(p.AccountScopeID)
	if first.Code != 200 {
		t.Fatalf("preview: %d %s", first.Code, first.Body.String())
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(first.Body.Bytes()))
	if err != nil || format != "jpeg" || cfg.Width > 480 || cfg.Height > 480 || first.Body.Len() > 80<<10 || bytes.Equal(original.Bytes(), first.Body.Bytes()) {
		t.Fatal("unbounded or original preview", err)
	}
	if err = db.Underlying().PutBytes(pebblestore.KeyProjectTask(p.AccountScopeID, project.ID, task.ID), []byte("unreadable")); err != nil {
		t.Fatal(err)
	}
	warm := read(p.AccountScopeID)
	if warm.Code != 200 || !bytes.Equal(warm.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("warm decoded original task")
	}
	if w := read("foreign"); w.Code != 404 || bytes.Equal(w.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("foreign cache disclosure")
	}
	if err = authority.DeleteVariant(principal, "delete-preview", v.CollectionID, v.ID); err != nil {
		t.Fatal(err)
	}
	if w := read(p.AccountScopeID); w.Code == 200 || bytes.Equal(w.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("deleted artifact served from cache")
	}
}

// Purpose: the preview resolver must reject arbitrary remote/file/traversal and
// ambiguous revision input before any I/O. Pure resolver rejection with no
// services configured proves these strings cannot trigger network/file access.
func TestProjectPreviewRejectsUnsafeSources(t *testing.T) {
	s := &Server{}
	for _, value := range []string{"https://example.invalid/v3/sessions/s/artifacts/a?event_seq=1", "//example.invalid/v3/sessions/s/artifacts/a?event_seq=1", "file:///etc/passwd", "/v3/sessions/../artifacts/a?event_seq=1", "/v3/sessions/s/artifacts/%2e%2e?event_seq=1", "/v3/sessions/s/artifacts/a", "/v3/sessions/s/artifacts/a?event_seq=1&event_seq=2", "/v3/media-staging/stg_untrusted"} {
		if authority, _, _, err := s.projectPreviewSource(testPrincipal(), value); err == nil || authority != nil {
			t.Fatalf("unsafe preview accepted %q", value)
		}
	}
}

// Purpose: non-inline video artifact resolution must feed real bounded poster
// extraction, not return the video or dereference an arbitrary file. Artifact
// authority plus real ffmpeg proves the format path; this is not a benchmark.
func TestProjectPreviewPinnedVideoPoster(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "source.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=640x360:r=1", "-frames:v", "1", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", path)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server, svc, registry, _, _, _, _ := newArtifactSessionFixture(t, "workspace.txt", "workspace")
	p := testPrincipal()
	v, err := artifact.NewAuthority(registry, svc).Create(ctx, artifact.Principal{SessionID: "artifact-session", AccountScopeID: p.AccountScopeID, UserID: p.UserID}, artifact.CreateInput{RequestID: "poster-create", CollectionID: "poster-collection", CollectionName: "Poster", VariantID: "poster-variant", Filename: "source.mp4", MediaType: "video/mp4", Role: pebblestore.SessionArtifactRoleRenderOnly, Presentation: pebblestore.SessionArtifactPresentation{Kind: "download"}, Body: original})
	if err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("/v3/sessions/%s/artifacts/%s?event_seq=%d", v.SessionID, v.ID, v.EventSeq)
	value, err := server.readProjectPreviewSource(ctx, p, source)
	if err != nil {
		t.Fatal(err)
	}
	poster, err := smallTaskPoster(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(poster))
	if err != nil || format != "jpeg" || cfg.Width > 480 || cfg.Height > 480 || len(poster) > 80<<10 || bytes.Equal(poster, original) {
		t.Fatal("invalid artifact poster", err)
	}
	foreign := p
	foreign.AccountScopeID = "foreign"
	if data, err := server.readProjectPreviewSource(ctx, foreign, source); err == nil || data != "" {
		t.Fatal("foreign artifact resolved")
	}
}
