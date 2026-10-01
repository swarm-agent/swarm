package videogen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type privacyTransport func(*http.Request) (*http.Response, error)

func (f privacyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Purpose: real Omni/Veo, file upload/poll/download error exits must sanitize
// before task/tool persistence, while authenticating without query credentials.
// Injecting only HTTP transport exercises production payload and error code.
func TestGoogleMediaCredentialBoundary(t *testing.T) {
	key := "fake-video-credential+/="
	for _, mode := range []string{"transport", "json", "plaintext"} {
		t.Run(mode, func(t *testing.T) {
			s := &Service{googleBaseURL: "https://example.invalid", pollInterval: time.Nanosecond}
			s.httpClient = &http.Client{Transport: privacyTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != key {
					t.Fatal("credential URL or missing authentication")
				}
				if mode == "transport" {
					return nil, fmt.Errorf("echo %s: %w", key, context.Canceled)
				}
				body := key
				if mode == "json" {
					body = `{"error":{"message":"` + key + `"}}`
				}
				return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := []func() error{
				func() error { _, err := s.generateGoogleOmni(ctx, key, "test-model", "test", "", "", "create", nil, nil); return err },
				func() error { _, err := s.generateGoogleVeo(ctx, key, "test-model", "test", "", "", 8, "create", nil, nil); return err },
				func() error { _, err := s.uploadGoogleFile(ctx, key, []byte("video"), "video/mp4"); return err },
				func() error { _, err := s.downloadGoogleFile(ctx, key, "/file"); return err },
			}
			if mode == "json" {
				calls = append(calls, func() error { _, err := s.pollGoogleFileActive(ctx, key, "files/test", ""); return err })
				calls = append(calls, func() error { _, err := s.pollGoogleVeoOperation(ctx, key, "test-model", "operations/test", "", "create", nil); return err })
			}
			for _, call := range calls {
				err := call()
				if err == nil { t.Fatal("failure became success") }
				encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
				if strings.Contains(string(encoded), key) || strings.Contains(fmt.Sprintf("%#v", err), key) { t.Fatal("credential escaped provider boundary") }
				if mode == "transport" && !errors.Is(err, context.Canceled) { t.Fatal("lost cancellation") }
			}
		})
	}
}

// Purpose: malformed request construction and signed upload/download transport
// errors must be safe even before/after generation, with no unsafe error cause.
// Fake transport keeps all signed links hermetic and proves final upload errors.
func TestGoogleMediaSignedFailureBoundary(t *testing.T) {
	key := "fake-video-secret"
	signed := "fake-signed-resource"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := &Service{googleBaseURL: "https://example.invalid"}
	calls := 0
	s.httpClient = &http.Client{Transport: privacyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(r.URL.Path, "/upload/") {
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Goog-Upload-Url": []string{"https://example.invalid/finalize?signature=" + signed}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return nil, errors.New("request failed " + r.URL.String())
	})}
	_, err := s.uploadGoogleFile(ctx, key, []byte("video"), "video/mp4")
	if err == nil || strings.Contains(err.Error(), signed) || calls != 2 { t.Fatal("signed upload failure leaked or wrong request count") }
	_, err = s.downloadGoogleFile(ctx, key, "/download?signature="+signed)
	if err == nil || strings.Contains(err.Error(), signed) { t.Fatal("signed download failure leaked") }
	s.googleBaseURL = "https://example.invalid/\n" + key
	_, err = s.generateGoogleOmni(ctx, key, "test-model", "test", "", "", "extend", &ManagedVideoSource{InteractionID: "prior-interaction"}, nil)
	if err == nil || strings.Contains(err.Error(), key) { t.Fatal("request construction failure leaked") }
}
