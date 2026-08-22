package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAudioTranscriptionHandlersRequireProductPrincipal(t *testing.T) {
	server := &Server{}
	tests := []struct {
		path string
		handle http.HandlerFunc
		body string
	}{
		{"/v1/workspace/audio/transcribe", server.handleWorkspaceAudioTranscribe, `{}`},
		{"/v1/workspace/audio/transcribe/status", server.handleWorkspaceAudioTranscribeStatus, `{}`},
		{"/v1/workspace/audio/transcribe/read", server.handleWorkspaceAudioTranscribeRead, `{}`},
		{"/v1/workspace/audio/transcribe/cancel", server.handleWorkspaceAudioTranscribeCancel, `{}`},
		{"/v1/workspace/audio/analysis/read", server.handleWorkspaceAudioAnalysisRead, `{}`},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		test.handle(recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestAudioTranscriptionRejectsOversizedRequestBody(t *testing.T) {
	principal := accountTestPrincipal()
	workspacePath := t.TempDir()
	server, _ := newVideoWorkspaceSecurityServer(t, principal, workspacePath)
	payload := `{"workspace_path":"` + workspacePath + `","audio_ref":"audiosrc_` + strings.Repeat("a", 64) + `","focus_notes":"` + strings.Repeat("x", directVideoRequestMaxBytes) + `"}`
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/audio/transcribe", strings.NewReader(payload)))
	recorder := httptest.NewRecorder()
	server.handleWorkspaceAudioTranscribe(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "request body exceeds") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAudioTranscriptionRejectsInvalidOpaqueReferenceWithoutPathLeakage(t *testing.T) {
	principal := accountTestPrincipal()
	workspacePath := t.TempDir()
	server, _ := newVideoWorkspaceSecurityServer(t, principal, workspacePath)
	payload, err := json.Marshal(map[string]string{"workspace_path": workspacePath, "audio_ref": "../../private/song.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/audio/transcribe", bytes.NewReader(payload)))
	recorder := httptest.NewRecorder()
	server.handleWorkspaceAudioTranscribe(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), workspacePath) || strings.Contains(recorder.Body.String(), "private/song.mp3") {
		t.Fatalf("response leaked a source path: %s", recorder.Body.String())
	}
}

func TestSafeDirectAudioTranscriptBoundsWordsAndOmitsPrivateIdentity(t *testing.T) {
	words := make([]pebblestore.NormalizedTranscriptWord, directAudioTranscriptMaxWords+1)
	for index := range words {
		words[index] = pebblestore.NormalizedTranscriptWord{Text: "word", StartMs: int64(index), EndMs: int64(index + 1)}
	}
	result := safeDirectAudioTranscript(pebblestore.NormalizedTranscript{
		Ref: "transcript", JobRef: "job", SchemaVersion: pebblestore.NormalizedTranscriptSchemaVersion,
		Words: words, Metadata: pebblestore.NormalizedTranscriptMetadata{ProviderID: "private-provider", Model: "private-model"},
	})
	bounded, ok := result["words"].([]pebblestore.NormalizedTranscriptWord)
	if !ok || len(bounded) != directAudioTranscriptMaxWords || result["words_truncated"] != true || result["details_truncated"] != true {
		t.Fatalf("unexpected bounded transcript: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-provider") || strings.Contains(string(encoded), "private-model") {
		t.Fatalf("response exposed private provider identity: %s", encoded)
	}
}

func TestSafeDirectAudioAnalysisBoundsOutputAndOmitsOwnership(t *testing.T) {
	levels := make([]pebblestore.AudioAnalysisLevel, directAudioAnalysisMaxLevels+1)
	result := safeDirectAudioAnalysis(pebblestore.AudioAnalysisSnapshot{
		Ref: "audanalysis_ref", SchemaVersion: pebblestore.AudioAnalysisSchemaVersion,
		AccountScopeID: "private-account", WorkspaceID: "private-workspace", SourceFingerprint: strings.Repeat("f", 64),
		SourceRef: "audiosrc_ref", AnalyzerVersion: "swarm-dsp.v1", DurationMs: 1000, SampleIntervalMs: 100, Levels: levels,
	})
	bounded, ok := result["levels"].([]pebblestore.AudioAnalysisLevel)
	if !ok || len(bounded) != directAudioAnalysisMaxLevels || result["levels_truncated"] != true || result["details_truncated"] != true {
		t.Fatalf("unexpected bounded analysis: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-account") || strings.Contains(string(encoded), "private-workspace") || strings.Contains(string(encoded), strings.Repeat("f", 64)) {
		t.Fatalf("response exposed private ownership metadata: %s", encoded)
	}
}
