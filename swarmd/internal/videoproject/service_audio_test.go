package videoproject

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCreateProjectValidatesExactTrustedAudioSource(t *testing.T) {
	dir := t.TempDir()
	db, err := pebblestore.Open(filepath.Join(dir, "sessions.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	const sessionID = "session"
	const workspaceID = "workspace"
	if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		ClientRequestID: "create-session", IdempotencyKey: "create-session", PayloadHash: "create-session",
		Kind:      pebblestore.V3SessionMutationCreateSession,
		Session:   &pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Metadata: map[string]any{"workspace_id": workspaceID}},
		NowUnixMs: 1,
	}); err != nil {
		t.Fatal(err)
	}
	audioRoot := filepath.Join(dir, "audio")
	if err := os.Mkdir(audioRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	const filename = "soundtrack.wav"
	path := filepath.Join(audioRoot, filename)
	if err := os.WriteFile(path, []byte("RIFF\x04\x00\x00\x00WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.PutAudioSourceRecord(pebblestore.AudioSourceRecord{
		AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID,
		RootPath: audioRoot, RelativePath: filename, DisplayName: filename, MIMEType: "audio/wav",
		SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	exact := &pebblestore.AudioSourceReference{
		Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes,
		SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion,
	}
	clip := pebblestore.VideoTimelineClip{
		ID: "soundtrack", Track: 1, Layer: 1, Sequence: 0,
		SourceKind: pebblestore.VideoClipSourceKindSourceAudio, AudioSource: exact, MediaType: record.MIMEType,
		SourceStartMs: 0, SourceEndMs: 1000, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Volume: 1,
	}
	svc := NewService(store)
	project, revision, err := svc.CreateProject(context.Background(), principal, CreateProjectInput{
		SessionID: sessionID, WorkspaceID: workspaceID, ProjectID: "project", Title: "Audio project",
		InitialTimeline: &pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetLandscape1080p, Clips: []pebblestore.VideoTimelineClip{clip}},
	})
	if err != nil {
		t.Fatalf("trusted audio clip rejected: %v", err)
	}
	if revision == nil || project.CurrentRevisionID != revision.ID || revision.Timeline.Clips[0].AudioSource.Ref != record.Ref {
		t.Fatalf("audio clip was not durably preserved: project=%+v revision=%+v", project, revision)
	}

	stale := clip
	stale.AudioSource = &pebblestore.AudioSourceReference{
		Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes,
		SourceFingerprint: strings.Repeat("c", 64), FingerprintVersion: record.FingerprintVersion,
	}
	_, _, err = svc.CreateProject(context.Background(), principal, CreateProjectInput{
		SessionID: sessionID, WorkspaceID: workspaceID, ProjectID: "stale", Title: "Stale audio",
		InitialTimeline: &pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetLandscape1080p, Clips: []pebblestore.VideoTimelineClip{stale}},
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("expected clear stale fingerprint rejection, got %v", err)
	}

	staleProposalClip := stale
	_, err = svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{
		SessionID: sessionID, ProjectID: project.ID, BaseRevisionID: revision.ID,
		Operations:     []pebblestore.VideoEditOperation{{ID: "replace-soundtrack", Type: pebblestore.VideoEditOperationReplaceClip, ClipID: clip.ID, Clip: &staleProposalClip}},
		AffectedRanges: []pebblestore.VideoTimelineRange{{StartMs: 0, EndMs: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("expected proposal fingerprint mismatch rejection, got %v", err)
	}

	unsupported := clip
	unsupported.AudioSource = &pebblestore.AudioSourceReference{
		Ref: record.Ref, Name: record.DisplayName, MIMEType: "video/mp4", SizeBytes: record.SizeBytes,
		SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion,
	}
	_, err = svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{
		SessionID: sessionID, ProjectID: project.ID, BaseRevisionID: revision.ID,
		Operations:     []pebblestore.VideoEditOperation{{ID: "unsupported-soundtrack", Type: pebblestore.VideoEditOperationReplaceClip, ClipID: clip.ID, Clip: &unsupported}},
		AffectedRanges: []pebblestore.VideoTimelineRange{{StartMs: 0, EndMs: 1000}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported media type") {
		t.Fatalf("expected clear unsupported-media proposal rejection, got %v", err)
	}
}

func TestCreateProjectAndProposalWithImportedAudioSourceRecord(t *testing.T) {
	dir := t.TempDir()
	db, err := pebblestore.Open(filepath.Join(dir, "sessions-imported-audio.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc-1", UserID: "user-1"}
	const sessionID = "sess-imported"
	const workspaceID = "ws-imported"

	if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
		ClientRequestID: "create-session", IdempotencyKey: "create-session", PayloadHash: "create-session",
		Kind:      pebblestore.V3SessionMutationCreateSession,
		Session:   &pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Metadata: map[string]any{"workspace_id": workspaceID}},
		NowUnixMs: 1,
	}); err != nil {
		t.Fatal(err)
	}

	mediaRoot := filepath.Join(dir, "imported_media")
	if err := os.Mkdir(mediaRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	const filename = "imported_theme.mp3"
	path := filepath.Join(mediaRoot, filename)
	if err := os.WriteFile(path, []byte("ID3\x04\x00\x00\x00\x00\x00\x00sample-mp3-audio-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	record, err := store.PutAudioSourceRecord(pebblestore.AudioSourceRecord{
		AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID,
		RootPath: mediaRoot, RelativePath: filename, DisplayName: "Theme Track", MIMEType: "audio/mpeg",
		SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("put imported audio source: %v", err)
	}

	exact := &pebblestore.AudioSourceReference{
		Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes,
		SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion,
	}

	clip := pebblestore.VideoTimelineClip{
		ID: "soundtrack-track", Track: 1, Layer: 1, Sequence: 0,
		SourceKind: pebblestore.VideoClipSourceKindSourceAudio, AudioSource: exact, MediaType: record.MIMEType,
		SourceStartMs: 0, SourceEndMs: 5000, TimelineStartMs: 0, TimelineEndMs: 5000, DurationMs: 5000, Volume: 0.9,
	}

	svc := NewService(store)
	project, revision, err := svc.CreateProject(context.Background(), principal, CreateProjectInput{
		SessionID: sessionID, WorkspaceID: workspaceID, ProjectID: "proj-imported", Title: "Project with imported audio",
		InitialTimeline: &pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			Clips:        []pebblestore.VideoTimelineClip{clip},
		},
	})
	if err != nil {
		t.Fatalf("CreateProject with imported audio rejected: %v", err)
	}
	if revision == nil || len(revision.Timeline.Clips) != 1 || revision.Timeline.Clips[0].AudioSource.Ref != record.Ref {
		t.Fatalf("imported audio clip not saved properly: %+v", revision)
	}

	// Create edit proposal adding another operation on the imported audio
	proposalClip := clip
	proposalClip.Volume = 0.5
	proposal, err := svc.CreateEditProposal(context.Background(), principal, CreateEditProposalInput{
		SessionID: sessionID, ProjectID: project.ID, BaseRevisionID: revision.ID,
		Operations: []pebblestore.VideoEditOperation{
			{ID: "op-update-soundtrack", Type: pebblestore.VideoEditOperationUpdateClip, ClipID: clip.ID, Clip: &proposalClip},
		},
		AffectedRanges: []pebblestore.VideoTimelineRange{{StartMs: 0, EndMs: 5000}},
	})
	if err != nil {
		t.Fatalf("CreateEditProposal with imported audio failed: %v", err)
	}
	if proposal.Status != pebblestore.VideoEditProposalStatusPending {
		t.Fatalf("expected pending status, got %s", proposal.Status)
	}

	// Negative test: Audio source in wrong workspace scope is rejected
	wrongWorkspaceClip := clip
	wrongWorkspaceClip.AudioSource = &pebblestore.AudioSourceReference{
		Ref:                "audiosrc_0000000000000000000000000000000000000000000000000000000000000000",
		Name:               record.DisplayName,
		MIMEType:           record.MIMEType,
		SizeBytes:          record.SizeBytes,
		SourceFingerprint:  record.SourceFingerprint,
		FingerprintVersion: record.FingerprintVersion,
	}
	_, _, err = svc.CreateProject(context.Background(), principal, CreateProjectInput{
		SessionID: sessionID, WorkspaceID: workspaceID, ProjectID: "proj-wrong", Title: "Wrong workspace audio",
		InitialTimeline: &pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			Clips:        []pebblestore.VideoTimelineClip{wrongWorkspaceClip},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered in the video project workspace scope") {
		t.Fatalf("expected workspace scope rejection, got %v", err)
	}
}
