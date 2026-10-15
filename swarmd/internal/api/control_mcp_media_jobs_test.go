package api

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/outbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRecommendedMediaModel(t *testing.T) {
	ratios := &mediaCatalogGenerationOptions{AspectRatios: []string{"1:1"}}
	video := &mediaCatalogGenerationOptions{Resolutions: []string{"720p"}}
	catalog := mediaCatalogResponse{
		ImageModels: []mediaCatalogOption{
			{ID: imagegen.DefaultModelSelectionID, Ready: true},
			{ID: "gemini-3-pro-image", Ready: true, GenerationOptions: ratios},
			{ID: "gemini-nano-banana-2.1", Ready: false, GenerationOptions: ratios},
			{ID: "gemini-3.1-flash-image", Ready: true}, // no settings data: never a default
		},
		VideoGenerationModels: []mediaCatalogOption{
			{ID: "veo-3.1-generate-preview", Ready: true, GenerationOptions: video},
			{ID: "gemini-omni-1.1-flash", Ready: true, GenerationOptions: video},
			{ID: "google/veo-3.1", Ready: false, GenerationOptions: video},
		},
	}
	if got := recommendedMediaModel(catalog, "image"); got != "gemini-3-pro-image" {
		t.Fatalf("image default = %q, want the first ready preferred model with settings", got)
	}
	if got := recommendedMediaModel(catalog, "video"); got != "gemini-omni-1.1-flash" {
		t.Fatalf("video default = %q", got)
	}
	if got := recommendedMediaModel(catalog, "sound"); got != "" {
		t.Fatalf("sound has no fixed default (picked by length), got %q", got)
	}
	catalog.ImageModels = catalog.ImageModels[:1]
	if got := recommendedMediaModel(catalog, "image"); got != imagegen.DefaultModelSelectionID {
		t.Fatalf("image falls back to the ChatGPT image model, got %q", got)
	}
	catalog.ImageModels[0].Ready = false
	if got := recommendedMediaModel(catalog, "image"); got != "" {
		t.Fatalf("nothing ready: no default, got %q", got)
	}
}

func mediaTestTask(png []byte) *pebblestore.ProjectTaskRecord {
	return &pebblestore.ProjectTaskRecord{
		ID: "task_0123456789", ProjectID: "proj_1", Title: "Launch card: Swarm!", Agent: "image", Status: "needs_review", Model: "gemini-3-pro-image",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d1", Kind: "image", Status: "ready", Model: "gemini-3-pro-image", AspectRatio: "16:9", MediaURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
			{ID: "d2", Kind: "image", Status: "failed", Description: "Google blocked the prompt (SAFETY)"},
		},
	}
}

func TestMediaTaskFilesViewAndNames(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage-bytes")
	files := mediaTaskFiles(mediaTestTask(png))
	if len(files) != 2 || string(files[0].data) != string(png) || files[0].mediaType != "image/png" || files[1].data != nil {
		t.Fatalf("files = %+v", files)
	}
	view := files[0].view(1)
	if view["size_bytes"] != len(png) || view["aspect_ratio"] != "16:9" || view["sha256"] == nil || view["error"] != nil {
		t.Fatalf("ready view = %v", view)
	}
	if failed := files[1].view(2); failed["error"] != "Google blocked the prompt (SAFETY)" {
		t.Fatalf("failed view must say why: %v", failed)
	}
	if name := files[0].filename("Launch card: Swarm!", 1); name != "launch-card-swarm-1.png" {
		t.Fatalf("filename = %q", name)
	}
	if _, _, ok := decodeMediaDataURL("https://example.com/x.png"); ok {
		t.Fatal("only data: URLs are files")
	}
}

func TestControlMCPWorkspaceRootOf(t *testing.T) {
	roots := []string{"/srv/site", "/srv/site/media", "/srv/other"}
	for path, want := range map[string]string{
		"/srv/site/out":     "/srv/site",
		"/srv/site/media/x": "/srv/site/media",
		"/srv/sitex/out":    "",
		"/srv/site":         "/srv/site",
		"/etc/passwd":       "",
	} {
		if got := controlMCPWorkspaceRootOf(roots, path); got != want {
			t.Fatalf("%s: root %q, want %q", path, got, want)
		}
	}
}

func TestOutboxMediaExport(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage-bytes")
	export := outboxMediaExport(mediaTestTask(png), outbox.NewBuilder("pc", 1<<20))
	t.Cleanup(func() {
		for _, blob := range export.Blobs {
			_ = os.Remove(blob.Path)
		}
	})
	m := export.Manifest
	if export.JobID != "media/proj_1/task_0123456789" || export.RunID != "task_0123456789" || m["outcome"] != "media" || m["kind"] != "image" || m["model"] != "gemini-3-pro-image" {
		t.Fatalf("manifest = %v", m)
	}
	files, _ := m["files"].([]map[string]any)
	if len(files) != 1 || len(export.Blobs) != 1 || files[0]["key"] != "machines/pc/blobs/sha256/"+export.Blobs[0].SHA256 || files[0]["type"] != "image/png" {
		t.Fatalf("files = %v blobs = %v", files, export.Blobs)
	}
	stored, err := os.ReadFile(export.Blobs[0].Path)
	if err != nil || string(stored) != string(png) {
		t.Fatalf("blob holds the exact bytes: %q %v", stored, err)
	}
	skipped, _ := m["skipped"].([]map[string]string)
	if len(skipped) != 1 || skipped[0]["reason"] != "Google blocked the prompt (SAFETY)" {
		t.Fatalf("a failed file is listed with its reason: %v", skipped)
	}
	small := outboxMediaExport(mediaTestTask(png), outbox.NewBuilder("pc", 4))
	if len(small.Blobs) != 0 {
		t.Fatal("a file over the limit is skipped, not exported")
	}
}

// Requirement: MCP media views direct retries to swarm_request_media, explain
// content-policy refusals without rewriting stored errors, and advertise inline
// images only when present. Threat: generic coder guidance or image-only wording
// misdirects the AI, while matching truncated errors hides actionable refusals.
// controlMCPMediaJobFiles owns the view and calls controlMCPMediaJobGuidance;
// testing that pure guidance boundary with decoded deliverables is the narrowest
// layer that proves the wording, negative cases, and unchanged source errors.
func TestControlMCPMediaJobGuidance(t *testing.T) {
	failed := func(message string) pebblestore.ProjectTaskDeliverable {
		return pebblestore.ProjectTaskDeliverable{Kind: "sound", Status: "failed", Description: message}
	}
	ready := func(kind, mediaType string) pebblestore.ProjectTaskDeliverable {
		return pebblestore.ProjectTaskDeliverable{Kind: kind, Status: "ready", MediaURL: "data:" + mediaType + ";base64,Ynl0ZXM="}
	}
	const hint = "the provider refused the brief by its content policy: rephrase it; naming real artists, songs, films, brands or people often triggers this"
	for _, tc := range []struct {
		name         string
		status       string
		deliverables []pebblestore.ProjectTaskDeliverable
		wantHint     bool
		wantRetry    bool
		wantImages   bool
	}{
		{"failed sound", "failed", []pebblestore.ProjectTaskDeliverable{failed("provider unavailable")}, false, true, false},
		{"blocked", "blocked", nil, false, true, false},
		{"cancelled", "cancelled", nil, false, true, false},
		{"rejected", "rejected", nil, false, true, false},
		{"policy code case insensitive", "failed", []pebblestore.ProjectTaskDeliverable{failed("HTTP 400: PROHIBITED_CONTENT")}, true, true, false},
		{"policy message case insensitive", "failed", []pebblestore.ProjectTaskDeliverable{failed("iNpUt BlOcKeD: sensitive words")}, true, true, false},
		{"later file untruncated error", "failed", []pebblestore.ProjectTaskDeliverable{failed("timeout"), failed(strings.Repeat("x", 450) + "prohibited_content")}, true, true, false},
		{"ready sound", "needs_review", []pebblestore.ProjectTaskDeliverable{ready("sound", "audio/mpeg")}, false, false, false},
		{"completed video", "completed", []pebblestore.ProjectTaskDeliverable{ready("video", "video/mp4")}, false, false, false},
		{"ready image", "needs_review", []pebblestore.ProjectTaskDeliverable{ready("image", "image/png")}, false, false, true},
		{"completed mixed files", "completed", []pebblestore.ProjectTaskDeliverable{ready("sound", "audio/mpeg"), ready("image", "image/png")}, false, false, true},
		{"ready partial refusal", "needs_review", []pebblestore.ProjectTaskDeliverable{ready("image", "image/png"), failed("prohibited_content")}, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &pebblestore.ProjectTaskRecord{Deliverables: tc.deliverables}
			files := mediaTaskFiles(task)
			out := map[string]any{"status": tc.status, "next": controlMCPJobNext(tc.status, false)}
			controlMCPMediaJobGuidance(out, files)
			next, _ := out["next"].(string)
			if tc.wantRetry {
				if !strings.Contains(next, "swarm_request_media") || strings.Contains(next, "swarm_give_job") {
					t.Fatalf("media retry guidance = %q", next)
				}
			} else {
				if !strings.Contains(next, "swarm_save_media") || !strings.Contains(next, "nothing to merge") {
					t.Fatalf("ready guidance = %q", next)
				}
				if strings.Contains(next, "images") != tc.wantImages || strings.Contains(next, "include_media") != tc.wantImages {
					t.Fatalf("image guidance = %q, want images %v", next, tc.wantImages)
				}
			}
			gotHint, present := out["hint"]
			if present != tc.wantHint || (present && gotHint != hint) {
				t.Fatalf("hint = %v (present %v), want present %v", gotHint, present, tc.wantHint)
			}
			if tc.wantHint && !strings.Contains(next, "rephrase") {
				t.Fatalf("policy retry must suggest rephrasing: %q", next)
			}
			if out["status"] != tc.status {
				t.Fatalf("guidance changed status: %v", out)
			}
			for i, file := range files {
				if file.record.Description != tc.deliverables[i].Description {
					t.Fatal("guidance changed a stored file error")
				}
			}
		})
	}
}
