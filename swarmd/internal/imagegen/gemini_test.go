package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestGoogleGeminiImageClientGenerateImageUsesRESTGenerateContent(t *testing.T) {
	png := testPNGBytes()
	var gotPath string
	var gotKey string
	var gotBody geminiGenerateContentRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("key")
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiGenerateContentResponse{
			ResponseID:    "resp_rest",
			ModelVersion:  "test-model-version",
			UsageMetadata: map[string]any{"totalTokenCount": float64(12)},
			Candidates: []geminiRESTCandidate{{
				Content: &geminiRESTContent{Parts: []geminiRESTPart{
					{Text: "thinking", Thought: true},
					{InlineData: &geminiRESTInlineData{MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(png)}},
				}},
			}},
		})
	}))
	defer server.Close()

	var events []GenerateStreamEvent
	client := googleGeminiImageClient{httpClient: server.Client(), baseURL: server.URL + "/v1beta"}
	result, err := client.GenerateImage(context.Background(), GeminiImageGenerationRequest{
		APIKey:      "test-key",
		Model:       "test-model",
		Prompt:      "make image",
		AspectRatio: "16:9",
		ImageSize:   "1K",
		OutputIndex: 2,
		OnEvent: func(event GenerateStreamEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if gotPath != "/v1beta/models/test-model:generateContent" {
		t.Fatalf("path = %q, want generateContent model path", gotPath)
	}
	if gotKey != "test-key" {
		t.Fatalf("api key query = %q, want test-key", gotKey)
	}
	if len(gotBody.Contents) != 1 || len(gotBody.Contents[0].Parts) != 1 || gotBody.Contents[0].Parts[0].Text != "make image" {
		t.Fatalf("contents = %#v, want single prompt part", gotBody.Contents)
	}
	if got := gotBody.GenerationConfig.ResponseModalities; len(got) != 1 || got[0] != "IMAGE" {
		t.Fatalf("response modalities = %#v, want IMAGE only", got)
	}
	if gotBody.GenerationConfig.ImageConfig.AspectRatio != "16:9" || gotBody.GenerationConfig.ImageConfig.ImageSize != "1K" {
		t.Fatalf("image config = %#v, want requested aspect ratio and size", gotBody.GenerationConfig.ImageConfig)
	}
	if len(result.Images) != 1 || string(result.Images[0].DecodedPNG) != string(png) {
		t.Fatalf("images = %#v, want decoded REST image", result.Images)
	}
	if len(result.Thinking) != 1 || result.Thinking[0] != "thinking" {
		t.Fatalf("thinking = %#v, want thought text", result.Thinking)
	}
	if result.ProviderResponse["transport"] != "rest" || result.ProviderResponse["stream_method"] != "REST generateContent non-stream" {
		t.Fatalf("provider response = %#v, want REST non-stream metadata", result.ProviderResponse)
	}
	if result.Usage == nil || result.Usage["available"] != true {
		t.Fatalf("usage = %#v, want available usage metadata", result.Usage)
	}
	if !hasGenerateEventType(events, "generating") || !hasGenerateEventType(events, "image") || !hasGenerateEventType(events, "thinking") {
		t.Fatalf("events = %#v, want generating/image/thinking events", events)
	}
}

type blockingGeminiImageClient struct {
	ready chan struct{}
	start chan struct{}

	mu          sync.Mutex
	active      int
	maxActive   int
	callCount   int
	outputIndex []int
}

func (c *blockingGeminiImageClient) GenerateImage(_ context.Context, req GeminiImageGenerationRequest) (GeminiImageGenerationResult, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.callCount++
	c.outputIndex = append(c.outputIndex, req.OutputIndex)
	if c.callCount == cap(c.ready) {
		close(c.ready)
	}
	c.mu.Unlock()

	<-c.start

	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return GeminiImageGenerationResult{ProviderResponse: map[string]any{"slot": req.OutputIndex}}, nil
}

func TestGenerateGeminiSlotsParallelKeepsRESTSlotsParallel(t *testing.T) {
	count := 3
	client := &blockingGeminiImageClient{ready: make(chan struct{}, count), start: make(chan struct{})}
	svc := &Service{geminiImageClient: client}

	resultCh := make(chan []GeminiImageGenerationResult, 1)
	errCh := make(chan error, 1)
	go func() {
		results, err := svc.generateGeminiSlotsParallel(context.Background(), "key", "model", "prompt", "1:1", "1K", count, nil)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- results
	}()

	<-client.ready
	client.mu.Lock()
	maxActive := client.maxActive
	client.mu.Unlock()
	if maxActive != count {
		t.Fatalf("max active REST calls = %d, want %d parallel calls", maxActive, count)
	}
	close(client.start)

	select {
	case err := <-errCh:
		t.Fatalf("generateGeminiSlotsParallel: %v", err)
	case results := <-resultCh:
		if len(results) != count {
			t.Fatalf("results len = %d, want %d", len(results), count)
		}
	}
}

func hasGenerateEventType(events []GenerateStreamEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
