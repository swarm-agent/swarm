package api

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/videotranscription"
)

type attachedCatalog struct{}

func (attachedCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return []pebblestore.ModelCatalogRecord{{Model: "gemini-test", SourceSnapshotID: "snap", SourceSnapshotVersion: "1", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"video", "audio"}, Outputs: []string{"text"}}}}, nil
}

type attachedSettings struct{}

func (attachedSettings) GetForAccount(string) (uisettings.UISettings, error) {
	return uisettings.UISettings{Media: uisettings.MediaSettings{TranscriptionModel: "gemini-test"}}, nil
}

type attachedSpeech struct{}

func (attachedSpeech) Transcribe(context.Context, videotranscription.TranscribeRequest) (videotranscription.GeneratedTranscript, error) {
	return videotranscription.GeneratedTranscript{}, nil
}

func (attachedSpeech) AnalyzeFrameBatch(context.Context, videotranscription.FrameBatchRequest) ([]videotranscription.FrameObservation, error) {
	return nil, nil
}

func (attachedSpeech) AnalyzeAudio(context.Context, videotranscription.AudioAnalysisRequest) (videotranscription.GeneratedTranscript, error) {
	return videotranscription.GeneratedTranscript{Language: "en", Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Speech: "take two"}}}, nil
}

// A file the person attached to the chat is an input by its asset_id: for
// media to make, and for transcribe. Another session's file is not.
func TestManageMediaAttachedFiles(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	wavPath := filepath.Join(t.TempDir(), "take.wav")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", wavPath).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	wav, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	root := t.TempDir()
	for _, id := range []string{"session", "other"} {
		if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: id, WorkspacePath: root, Metadata: map[string]any{"project_id": "project"}}, p.UserID, p.AccountScopeID); err != nil {
			t.Fatal(err)
		}
	}
	asset, _, err := store.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{AccountScopeID: p.AccountScopeID, SessionID: "session", Modality: "audio", DeclaredMIMEType: "audio/wav", FileName: "take.wav", Reader: bytes.NewReader(wav)})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := store.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{AccountScopeID: p.AccountScopeID, SessionID: "other", Modality: "audio", DeclaredMIMEType: "audio/wav", FileName: "theirs.wav", Reader: bytes.NewReader(append(append([]byte{}, wav...), 0))})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil), videoTranscription: videotranscription.NewService(store, attachedCatalog{}, attachedSettings{}, attachedSpeech{})}
	scope := tool.WorkspaceScope{SessionID: "session", Principal: p, PrimaryPath: root}

	result, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": "transcribe", "inputs": []any{map[string]any{"asset_id": asset.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["file"] != "take.wav" || out["model"] != "gemini-test" || !strings.Contains(out["text"].(string), "take two") || len(out["segments"].([]map[string]any)) != 1 {
		t.Fatalf("transcript %+v", out)
	}
	if _, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": "transcribe", "inputs": []any{map[string]any{"asset_id": foreign.ID}}}); err == nil {
		t.Fatal("another session's attachment was read")
	}
	if _, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": "transcribe", "inputs": []any{map[string]any{"asset_id": asset.ID}, map[string]any{"asset_id": asset.ID}}}); err == nil {
		t.Fatal("two inputs were taken")
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("plain words"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": "transcribe", "inputs": []any{map[string]any{"path": "note.txt"}}}); err == nil || !strings.Contains(err.Error(), "audio or video") {
		t.Fatalf("text file: %v", err)
	}
	// A workspace file transcribes the same way.
	if err := os.WriteFile(filepath.Join(root, "take.wav"), wav, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": "transcribe", "inputs": []any{map[string]any{"path": "take.wav"}}}); err != nil {
		t.Fatal(err)
	}

	readers := mediaInputReaders{asset: func(id string) ([]byte, string, string, error) {
		a, data, err := store.ReadSessionMediaAsset(p.AccountScopeID, "session", id)
		return data, pebblestore.NormalizeSessionMediaMIME(a.DetectedMIMEType), a.FileName, err
	}}
	inputs, err := mediaInputs([]any{map[string]any{"asset_id": asset.ID}}, func() ([]string, error) { return []string{root}, nil }, nil, manageMediaErrors, readers)
	if err != nil || len(inputs) != 1 || inputs[0]["kind"] != "audio" || inputs[0]["media_type"] != "audio/wav" || inputs[0]["filename"] != "take.wav" {
		t.Fatalf("inputs %+v %v", inputs, err)
	}
	// MCP callers have no chat: asset_id is refused there.
	if _, err := mediaInputs([]any{map[string]any{"asset_id": asset.ID}}, func() ([]string, error) { return []string{root}, nil }, nil, mcpMediaErrors); err == nil {
		t.Fatal("asset_id taken without a session")
	}
	// Without a transcription service it says so.
	server.videoTranscription = nil
	if _, err := server.ManageMedia(context.Background(), scope, map[string]any{"action": "transcribe", "inputs": []any{map[string]any{"asset_id": asset.ID}}}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("no service: %v", err)
	}
}
