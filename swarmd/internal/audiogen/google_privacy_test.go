package audiogen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Purpose: Lyria generation and download failures must not disclose API keys;
// untrusted returned URLs must be rejected before authentication is sent. Real
// local HTTP handlers exercise the production service without a paid provider.
func TestLyriaCredentialBoundary(t *testing.T) {
	key := "fake-audio-credential"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != key { t.Error("unsafe authentication") }
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"message":%q}}`, key)
	}))
	defer server.Close()
	s := &Service{googleBaseURL: server.URL, httpClient: &http.Client{Timeout: time.Second}}
	_, err := s.generateGoogleLyria(context.Background(), key, "test-model", "test", 30, ManagedAudioRequest{})
	if err == nil || strings.Contains(err.Error(), key) { t.Fatal("generation error leaked or became success") }
	_, err = s.downloadGoogleFile(context.Background(), key, "/file")
	if err == nil || strings.Contains(err.Error(), key) { t.Fatal("download error leaked or became success") }
	_, err = s.downloadGoogleFile(context.Background(), key, "https://foreign.invalid/file?token="+key)
	if err == nil || strings.Contains(err.Error(), key) { t.Fatal("foreign download was not safely rejected") }
}
