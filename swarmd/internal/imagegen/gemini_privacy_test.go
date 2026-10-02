package imagegen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Purpose: GenerateImage must use header authentication and remove plain/JSON
// provider echoes before image errors reach logs or durable tool results. Local
// HTTP is the narrowest production boundary covering request and error parsing.
func TestGeminiCredentialBoundary(t *testing.T) {
	key := "fake-image-credential+/="
	for _, body := range []string{key, `{"error":{"message":"` + key + `","status":"INVALID"}}`, url.QueryEscape(key)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != key {
				t.Error("credential URL or missing authentication")
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, body)
		}))
		client := googleGeminiImageClient{baseURL: server.URL, httpClient: &http.Client{Timeout: time.Second}}
		_, err := client.GenerateImage(context.Background(), GeminiImageGenerationRequest{APIKey: key, Model: "test-model", Prompt: "test"})
		server.Close()
		if err == nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), url.QueryEscape(key)) {
			t.Fatal("provider error leaked or became success")
		}
	}
}
