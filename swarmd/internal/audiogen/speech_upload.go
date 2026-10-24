package audiogen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Leave room for the transcript prompt/schema below Gemini's 20 MiB request limit.
const speechInlineAudioLimit = 14 << 20

type speechUploadedFile struct {
	Name  string `json:"name"`
	URI   string `json:"uri"`
	State string `json:"state"`
}

// Use the same resumable Files protocol as Google's transcription adapter.
// Keep credentials on the configured origin, bound responses/polling, and remove
// the temporary file on both success and failure.
func (s *Service) uploadSpeechAudio(ctx context.Context, key string, audio SpeechResult) (speechUploadedFile, func(), error) {
	client := *s.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := strings.TrimRight(s.googleURL(), "/")
	do := func(method, target string, body []byte, headers map[string]string) ([]byte, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("x-goog-api-key", key)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, errors.New("speech file request failed")
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, nil, fmt.Errorf("speech file request failed (HTTP %d)", resp.StatusCode)
		}
		return data, resp.Header, nil
	}
	_, headers, err := do(http.MethodPost, base+"/upload/v1beta/files", []byte(`{"file":{"display_name":"swarm-speech-timings"}}`), map[string]string{"Content-Type": "application/json", "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": fmt.Sprint(len(audio.Bytes)), "X-Goog-Upload-Header-Content-Type": audio.MediaType})
	noop := func() {}
	if err != nil {
		return speechUploadedFile{}, noop, err
	}
	target, err := url.Parse(headers.Get("X-Goog-Upload-URL"))
	origin, _ := url.Parse(base)
	if err != nil || target.Scheme != origin.Scheme || target.Host != origin.Host || target.User != nil {
		return speechUploadedFile{}, noop, errors.New("unsafe speech upload target")
	}
	data, _, err := do(http.MethodPost, target.String(), audio.Bytes, map[string]string{"Content-Type": audio.MediaType, "X-Goog-Upload-Offset": "0", "X-Goog-Upload-Command": "upload, finalize"})
	if err != nil {
		return speechUploadedFile{}, noop, err
	}
	var response struct {
		File speechUploadedFile `json:"file"`
	}
	if json.Unmarshal(data, &response) != nil || !strings.HasPrefix(response.File.Name, "files/") || strings.ContainsAny(strings.TrimPrefix(response.File.Name, "files/"), "/.?#%") {
		return speechUploadedFile{}, noop, errors.New("invalid speech upload reference")
	}
	file := response.File
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, base+"/v1beta/"+response.File.Name, nil)
		if err != nil {
			return
		}
		req.Header.Set("x-goog-api-key", key)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	for attempt := 0; attempt < 60; attempt++ {
		if file.State == "ACTIVE" && file.URI != "" {
			return file, cleanup, nil
		}
		if file.State != "PROCESSING" {
			cleanup()
			return speechUploadedFile{}, noop, errors.New("speech upload not active")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			cleanup()
			return speechUploadedFile{}, noop, ctx.Err()
		case <-timer.C:
		}
		data, _, err = do(http.MethodGet, base+"/v1beta/"+response.File.Name, nil, nil)
		if err != nil || json.Unmarshal(data, &file) != nil || file.Name != response.File.Name {
			cleanup()
			return speechUploadedFile{}, noop, errors.New("speech upload status failed")
		}
	}
	cleanup()
	return speechUploadedFile{}, noop, errors.New("speech upload processing timed out")
}
