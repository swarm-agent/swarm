package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
)

func TestSessionsV3SoundtrackProposalRemainsPendingForUserAcceptance(t *testing.T) {
	server, sessionSvc, _, _, _, _, _ := newArtifactSessionFixture(t, "note.txt", "fixture")
	store := sessionSvc.Store()
	server.SetVideoProjectService(videoproject.NewService(store))
	principal := testPrincipal()
	const sessionID = "soundtrack-api-session"
	const workspaceID = "soundtrack-workspace"
	if err := store.CreateSession(pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Metadata: map[string]any{"workspace_id": workspaceID}}); err != nil {
		t.Fatal(err)
	}

	audioRoot := t.TempDir()
	audioPath := filepath.Join(audioRoot, "soundtrack.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF\x04\x00\x00\x00WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.PutAudioSourceRecord(pebblestore.AudioSourceRecord{
		AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID, RootPath: audioRoot,
		RelativePath: "soundtrack.wav", DisplayName: "soundtrack.wav", MIMEType: "audio/wav",
		SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(sessionV3CreateVideoProjectRequest{ProjectID: "soundtrack-project", Title: "Soundtrack", OutputPreset: pebblestore.VideoPresetLandscape1080p, InitialTimeline: &pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetLandscape1080p, TotalDurationMs: 2000, Clips: []pebblestore.VideoTimelineClip{{ID: "visual", Track: 0, Sequence: 0, SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 2000, TimelineStartMs: 0, TimelineEndMs: 2000, Visible: true}}}})
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/video/projects", bytes.NewReader(createBody))
	createRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(createRec, createReq.WithContext(identity.ContextWithPrincipal(createReq.Context(), principal)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var create struct {
		Revision pebblestore.VideoProjectRevisionSnapshot `json:"revision"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &create); err != nil {
		t.Fatal(err)
	}

	exact := &pebblestore.AudioSourceReference{Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion}
	clip := &pebblestore.VideoTimelineClip{ID: "soundtrack", Name: "soundtrack.wav", Track: 1, Layer: 1, Sequence: 0, SourceKind: pebblestore.VideoClipSourceKindSourceAudio, AudioSource: exact, MediaType: exact.MIMEType, SourceStartMs: 0, SourceEndMs: 2000, TimelineStartMs: 0, TimelineEndMs: 2000, DurationMs: 2000, Volume: 0.7}
	proposalBody, _ := json.Marshal(sessionV3CreateVideoEditProposalRequest{BaseRevisionID: create.Revision.ID, Title: "Add soundtrack", Operations: []pebblestore.VideoEditOperation{{ID: "add-soundtrack", Type: pebblestore.VideoEditOperationAddClip, Clip: clip}}, AffectedRanges: []pebblestore.VideoTimelineRange{{StartMs: 0, EndMs: 2000}}})
	proposalReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/video/projects/soundtrack-project/edit-proposals", bytes.NewReader(proposalBody))
	proposalRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(proposalRec, proposalReq.WithContext(identity.ContextWithPrincipal(proposalReq.Context(), principal)))
	if proposalRec.Code != http.StatusCreated {
		t.Fatalf("create proposal status=%d body=%s", proposalRec.Code, proposalRec.Body.String())
	}
	var response sessionV3CreateVideoEditProposalResponse
	if err := json.Unmarshal(proposalRec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.ProposalStatus != pebblestore.VideoEditProposalStatusPending || !response.RequiresUserAcceptance || response.WorkingRevisionID == "" || response.Proposal.AcceptedRevisionID != "" {
		t.Fatalf("proposal response does not preserve user authority: %+v", response)
	}
	if strings.Contains(proposalRec.Body.String(), audioRoot) {
		t.Fatalf("proposal response leaked audio source path: %s", proposalRec.Body.String())
	}

	staleBody := bytes.NewReader(proposalBody)
	staleReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/video/projects/soundtrack-project/edit-proposals", staleBody)
	staleRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(staleRec, staleReq.WithContext(identity.ContextWithPrincipal(staleReq.Context(), principal)))
	if staleRec.Code != http.StatusBadRequest || !strings.Contains(staleRec.Body.String(), "base revision must be the current project revision") {
		t.Fatalf("stale proposal status=%d body=%s", staleRec.Code, staleRec.Body.String())
	}
}
