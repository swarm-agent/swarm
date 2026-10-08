package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Limit decoding across concurrent boards; originals are loaded only on a cache
// miss, never during collection serialization or store startup.
var projectPreviewSlots = make(chan struct{}, 1)

type previewOutput struct{ bytes.Buffer }

func (b *previewOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 80<<10 {
		return 0, errors.New("poster exceeds 80 KiB")
	}
	return b.Buffer.Write(p)
}

func smallTaskPoster(ctx context.Context, value string) ([]byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || (header != "data:video/mp4;base64" && header != "data:video/webm;base64") || len(encoded) > 128<<20 {
		return nil, errors.New("video poster source unavailable")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "swarm-task-poster-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	format := "mov"
	if header == "data:video/webm;base64" {
		format = "matroska"
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-f", format, "-i", file.Name(), "-frames:v", "1", "-vf", "scale=480:480:force_original_aspect_ratio=decrease", "-threads", "1", "-filter_threads", "1", "-q:v", "8", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
	var out previewOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("video poster extraction failed")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out.Bytes()))
	if err != nil || cfg.Width > 480 || cfg.Height > 480 {
		return nil, errors.New("invalid video poster")
	}
	return out.Bytes(), nil
}

func smallTaskPreview(value string) ([]byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") || len(encoded) > 32<<20 {
		return nil, errors.New("preview image unavailable or exceeds input limit")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 32<<20 {
		return nil, errors.New("preview image dimensions exceed limit")
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	w, h := cfg.Width, cfg.Height
	if w > 480 || h > 480 {
		if w >= h {
			h = h * 480 / w
			w = 480
		} else {
			w = w * 480 / h
			h = 480
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	target := image.NewRGBA(image.Rect(0, 0, w, h))
	bounds := source.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			target.Set(x, y, source.At(bounds.Min.X+x*cfg.Width/w, bounds.Min.Y+y*cfg.Height/h))
		}
	}
	for _, quality := range []int{80, 60, 40, 20} {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, target, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if out.Len() <= 80<<10 {
			return out.Bytes(), nil
		}
	}
	return nil, errors.New("preview exceeds 80 KiB limit")
}

func (s *Server) handleProjectTaskPreview(w http.ResponseWriter, r *http.Request, p identity.Principal, project, task, id string) {
	db := s.sessions.Store()
	summary, found, err := db.GetProjectTaskSummary(p.AccountScopeID, project, task)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	field, digest := r.URL.Query().Get("field"), r.URL.Query().Get("sha256")
	valid := false
	source := ""
	for _, d := range summary.Deliverables {
		if d.ID == id {
			u, e := url.Parse(d.Thumbnail)
			valid = e == nil && u.Query().Get("sha256") == digest && u.Query().Get("field") == field && digest != ""
			source = d.PreviewSource
		}
	}
	if !valid {
		http.NotFound(w, r)
		return
	}
	if source != "" {
		if _, _, _, err := s.projectPreviewSource(p, source); err != nil {
			writeError(w, http.StatusUnprocessableEntity, errors.New("small preview unavailable; open original explicitly"))
			return
		}
	}
	if content, found, err := db.GetProjectTaskPreview(p.AccountScopeID, project, task, id, digest); err == nil && found {
		serveProjectPreview(w, r, digest, content)
		return
	}
	// Warm reads bypass decoding admission. Bound queued misses independently of
	// browser cancellation so large boards cannot retain unlimited pending work.
	wait, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	select {
	case projectPreviewSlots <- struct{}{}:
	case <-wait.Done():
		writeError(w, http.StatusServiceUnavailable, errors.New("preview decoder busy; open original explicitly"))
		return
	}
	defer func() { <-projectPreviewSlots }()
	content, found, err := db.GetProjectTaskPreview(p.AccountScopeID, project, task, id, digest)
	if err == nil && !found {
		original, ok, e := db.GetProjectTask(p.AccountScopeID, project, task)
		err = e
		if err == nil && ok {
			for _, d := range original.Deliverables {
				if d.ID == id {
					value := d.MediaURL
					if field == "thumbnail" {
						value = d.Thumbnail
					}
					reference := pebblestore.ProjectTaskPreviewReference(*original, d.ID, field, value)
					u, e := url.Parse(reference)
					if e != nil || u.Query().Get("sha256") != digest {
						err = errors.New("preview reference is stale")
						break
					}
					if !strings.HasPrefix(value, "data:") {
						value, err = s.readProjectPreviewSource(r.Context(), p, value)
						if err != nil {
							break
						}
					}
					if strings.HasPrefix(value, "data:video/") {
						content, err = smallTaskPoster(r.Context(), value)
					} else {
						content, err = smallTaskPreview(value)
					}
					if err == nil {
						err = db.PutProjectTaskPreview(p.AccountScopeID, project, task, id, digest, content)
					}
					break
				}
			}
		}
	}
	if err != nil || len(content) == 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("small task preview unavailable"))
		return
	}
	serveProjectPreview(w, r, digest, content)
}

func serveProjectPreview(w http.ResponseWriter, r *http.Request, digest string, content []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", `"preview-`+digest+`"`)
	http.ServeContent(w, r, "preview.jpg", time.Time{}, bytes.NewReader(content))
}
