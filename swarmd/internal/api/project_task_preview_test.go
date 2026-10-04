package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: smallTaskPreview must produce a real, independently bounded raster,
// never return original bytes or accept active/oversized content. The encoder is
// the narrowest deterministic layer proving dimensions and byte postconditions.
func TestSmallTaskPreview(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 960, 640))
	for y := 0; y < 640; y++ {
		for x := 0; x < 960; x++ {
			source.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x * y), 255})
		}
	}
	var original bytes.Buffer
	if err := png.Encode(&original, source); err != nil {
		t.Fatal(err)
	}
	value := "data:image/png;base64," + base64.StdEncoding.EncodeToString(original.Bytes())
	preview, err := smallTaskPreview(value)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(preview))
	if err != nil || format != "jpeg" || cfg.Width > 480 || cfg.Height > 480 || len(preview) > 80<<10 || bytes.Equal(preview, original.Bytes()) {
		t.Fatalf("invalid preview: %+v %s bytes=%d err=%v", cfg, format, len(preview), err)
	}
	for _, bad := range []string{"data:image/svg+xml;base64,PHN2Zz4=", "https://example.invalid/image.png", "data:video/mp4;base64,AAAA", "data:image/png;base64,invalid"} {
		if data, err := smallTaskPreview(bad); err == nil || len(data) != 0 {
			t.Fatal("invalid input returned preview")
		}
	}
}

// Purpose: preview HTTP authorization, stale references and cached independence
// must hold together. This handler/store layer proves a warm preview never needs
// the full task body and that a foreign account cannot retrieve cached bytes.
func TestProjectTaskPreviewAuthorizationAndCache(t *testing.T) {
	server, store, p := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{ID: "preview-project", Name: "Preview"}
	if err := store.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 640, 320)))
	value := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, Title: "Image", Agent: "image", Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "image", MediaURL: value, Thumbnail: value}}}
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	reference := projectDeliverableContentURL(task, task.Deliverables[0], "media", value) + "&preview=1"
	read := func(account, scope, ref string) *httptest.ResponseRecorder {
		principal := p
		principal.AccountScopeID = account
		req := httptest.NewRequest(http.MethodGet, ref, nil)
		req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: account, UserID: p.UserID, Scopes: []string{scope}}))
		response := httptest.NewRecorder()
		server.handleProjectDeliverableContent(response, req, principal, project.ID, task.ID, "image")
		return response
	}
	first := read(p.AccountScopeID, "projects:read", reference)
	if first.Code != 200 || first.Body.Len() > 80<<10 {
		t.Fatalf("first preview %d %s", first.Code, first.Body.String())
	}
	// Corrupt only this isolated fixture's original body to prove the warm route
	// uses the compact identity + separate derivative, never a canonical decode.
	if err := store.Underlying().PutBytes(pebblestore.KeyProjectTask(p.AccountScopeID, project.ID, task.ID), []byte("unreadable")); err != nil {
		t.Fatal(err)
	}
	warm := read(p.AccountScopeID, "projects:read", reference)
	if warm.Code != 200 || !bytes.Equal(first.Body.Bytes(), warm.Body.Bytes()) {
		t.Fatal("warm cache decoded original")
	}
	denied := read("foreign", "projects:read", reference)
	if denied.Code != 404 || bytes.Equal(denied.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("foreign cached content disclosed")
	}
	if denied := read(p.AccountScopeID, "unrelated:read", reference); denied.Code != 403 {
		t.Fatal("scope bypass")
	}
	u, _ := url.Parse(reference)
	q := u.Query()
	q.Set("sha256", "stale")
	u.RawQuery = q.Encode()
	if stale := read(p.AccountScopeID, "projects:read", u.String()); stale.Code != 404 {
		t.Fatal("stale reference accepted")
	}
}

// Purpose: the poster path must decode a real video into a bounded image, not
// return video bytes or a fake poster. Real ffmpeg is the format boundary; this
// small generated fixture is codec regression coverage, not performance evidence.
func TestSmallTaskVideoPoster(t *testing.T) {
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
	poster, err := smallTaskPoster(ctx, "data:video/mp4;base64,"+base64.StdEncoding.EncodeToString(original))
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(poster))
	if err != nil || format != "jpeg" || cfg.Width > 480 || cfg.Height > 480 || len(poster) > 80<<10 {
		t.Fatalf("invalid poster %+v %s %d %v", cfg, format, len(poster), err)
	}
}
