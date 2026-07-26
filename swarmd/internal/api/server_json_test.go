package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONDisablesCaching(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusOK, map[string]any{"ok": true})

	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
}

func TestWriteJSONPreservesExplicitCacheControl(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "no-cache")

	writeJSON(rec, http.StatusOK, map[string]any{"ok": true})

	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}

func TestDecodeJSONLimitedRejectsOversizedBody(t *testing.T) {
	var out map[string]string
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/stt/transcribe", strings.NewReader(`{"audio_base64":"AAAA"}`))

	err := decodeJSONLimited(recorder, request, &out, 8)
	if err == nil || !strings.Contains(err.Error(), "request body exceeds 8 bytes") {
		t.Fatalf("decodeJSONLimited error = %v, want size limit error", err)
	}
}

func TestDecodeBase64AudioEnforcesDecodedBoundaryBeforeAllocation(t *testing.T) {
	const limit = 3
	atLimit := base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	audio, err := decodeBase64Audio(atLimit, limit)
	if err != nil {
		t.Fatalf("decode at limit: %v", err)
	}
	if len(audio) != limit {
		t.Fatalf("decoded bytes = %d, want %d", len(audio), limit)
	}

	overLimit := base64.RawStdEncoding.EncodeToString([]byte{1, 2, 3, 4})
	if _, err := decodeBase64Audio(overLimit, limit); err == nil || !strings.Contains(err.Error(), "exceeds 3 decoded bytes") {
		t.Fatalf("decode over limit error = %v, want decoded size limit error", err)
	}
}
