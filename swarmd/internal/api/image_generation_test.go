package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestHandleImageGenerationsStreamSerializesConcurrentEvents(t *testing.T) {
	xdgDataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", xdgDataHome)

	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "image-generation.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	authStore := pebblestore.NewAuthStore(db)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		Provider:  "google",
		Type:      pebblestore.AuthTypeAPI,
		APIKey:    "test-key",
		SetActive: true,
	}); err != nil {
		t.Fatalf("seed google credential: %v", err)
	}

	imageThreads := pebblestore.NewImageThreadStore(db)
	thread, err := imageThreads.Create(pebblestore.ImageThreadSnapshot{
		ID:            "thread-test",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "Workspace",
		Title:         "Images",
		Metadata:      map[string]any{},
	})
	if err != nil {
		t.Fatalf("create image thread: %v", err)
	}

	count := 3
	geminiClient := newConcurrentEventGeminiClient(count)
	imageService := imagegen.NewService(nil, authStore, imageThreads)
	imageService.SetGeminiImageClient(geminiClient)
	server := &Server{}
	server.SetImageGenerationService(imageService)

	payload := map[string]any{
		"provider": imagegen.ProviderGoogleGemini,
		"model":    "gemini-3.1-flash-image-preview",
		"prompt":   "make images",
		"count":    count,
		"target": map[string]any{
			"kind":      imagegen.TargetWorkspaceImage,
			"thread_id": thread.ID,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/image/generations", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	writer := newConcurrentDetectResponseWriter()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleImageGenerations(writer, req)
	}()

	select {
	case <-geminiClient.ready:
	case <-done:
		t.Fatalf("handler completed before all concurrent Gemini slots started; body=%s", writer.Body())
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for concurrent Gemini slots to start")
	}
	close(geminiClient.start)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for image generation stream handler")
	}

	if writer.ConcurrentWriteDetected() {
		t.Fatalf("image generation SSE response writer was used concurrently; body=%s", writer.Body())
	}
	if got := writer.Status(); got != http.StatusOK {
		t.Fatalf("status = %d body = %s", got, writer.Body())
	}
	streamBody := writer.Body()
	if strings.Contains(streamBody, "event: error") {
		t.Fatalf("stream returned error: %s", streamBody)
	}
	if !strings.Contains(streamBody, "event: completed") {
		t.Fatalf("stream body missing completed event: %s", streamBody)
	}
}

type concurrentEventGeminiClient struct {
	ready chan struct{}
	start chan struct{}
	count int

	mu        sync.Mutex
	callCount int
}

func newConcurrentEventGeminiClient(count int) *concurrentEventGeminiClient {
	return &concurrentEventGeminiClient{ready: make(chan struct{}), start: make(chan struct{}), count: count}
}

func (c *concurrentEventGeminiClient) GenerateImage(_ context.Context, req imagegen.GeminiImageGenerationRequest) (imagegen.GeminiImageGenerationResult, error) {
	c.mu.Lock()
	c.callCount++
	if c.callCount == c.count {
		close(c.ready)
	}
	c.mu.Unlock()

	<-c.start
	if req.OnEvent != nil {
		req.OnEvent(imagegen.GenerateStreamEvent{Type: "thinking", OutputIndex: req.OutputIndex, SequenceNumber: 1, Thinking: "thinking"})
		req.OnEvent(imagegen.GenerateStreamEvent{Type: "generating", OutputIndex: req.OutputIndex, SequenceNumber: 1})
	}
	png := imageGenerationTestPNGBytes()
	return imagegen.GeminiImageGenerationResult{
		ResponseID:   "response-test",
		ModelVersion: req.Model,
		Images: []imagegen.GeminiGeneratedImage{{
			Base64Image: base64.StdEncoding.EncodeToString(png),
			DecodedPNG:  png,
			MIMEType:    "image/png",
		}},
		ProviderResponse: map[string]any{"transport": "test"},
		ChunkCount:       1,
	}, nil
}

type concurrentDetectResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	mu         sync.Mutex
	status     int
	writing    atomic.Int32
	concurrent atomic.Bool
}

func newConcurrentDetectResponseWriter() *concurrentDetectResponseWriter {
	return &concurrentDetectResponseWriter{header: make(http.Header)}
}

func (w *concurrentDetectResponseWriter) Header() http.Header {
	return w.header
}

func (w *concurrentDetectResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *concurrentDetectResponseWriter) Write(data []byte) (int, error) {
	if !w.writing.CompareAndSwap(0, 1) {
		w.concurrent.Store(true)
	}
	defer w.writing.Store(0)
	time.Sleep(time.Millisecond)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *concurrentDetectResponseWriter) Flush() {}

func (w *concurrentDetectResponseWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *concurrentDetectResponseWriter) Body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func (w *concurrentDetectResponseWriter) ConcurrentWriteDetected() bool {
	return w.concurrent.Load()
}

func imageGenerationTestPNGBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}
