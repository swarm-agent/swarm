package pebblestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTranscriptionContractTest(t *testing.T) (*Store, *SessionStore, SessionSnapshot, MessageSnapshot) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "transcription.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := NewSessionStore(store)
	session := SessionSnapshot{
		ID: "transcription-session", UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace",
		Metadata: map[string]any{"workspace_id": "workspace"},
	}
	if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &session,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, []byte("synthetic-video-fixture"), 0o600); err != nil {
		t.Fatalf("write video source: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat video source: %v", err)
	}
	source, err := sessions.PutVideoSourceRecord(VideoSourceRecord{
		AccountScopeID: session.AccountScopeID, WorkspaceID: "workspace", RootPath: root, RelativePath: "clip.mp4",
		DisplayName: "clip.mp4", MIMEType: "video/mp4", SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("put video source: %v", err)
	}
	messageResult, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: "message", PayloadHash: "message", Kind: V3SessionMutationAppendMessage,
		Message: &MessageSnapshot{ID: "message", Role: "user", Content: "transcribe this video", VideoAttachments: []SessionVideoAttachmentReference{{Ref: source.Ref}}},
	})
	if err != nil || messageResult.Message == nil {
		t.Fatalf("append message: result=%+v err=%v", messageResult, err)
	}
	return store, sessions, session, *messageResult.Message
}

func TestTranscriptionContractBindsTrustedSourceAndFailsClosedAcrossScope(t *testing.T) {
	_, sessions, session, message := setupTranscriptionContractTest(t)
	attachment, replayed, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID,
		VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "bind", NowUnixMs: 100,
	})
	if err != nil || replayed || attachment.WorkspaceID != "workspace" || attachment.MessageID != message.ID || attachment.SourceFingerprint == "" {
		t.Fatalf("bind attachment=%+v replayed=%v err=%v", attachment, replayed, err)
	}
	rebound, replayed, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID,
		VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "bind-retry", NowUnixMs: 200,
	})
	if err != nil || !replayed || rebound.Ref != attachment.Ref {
		t.Fatalf("rebind attachment=%+v replayed=%v err=%v", rebound, replayed, err)
	}
	if _, ok, err := sessions.GetTranscriptionAttachment("other-account", session.ID, attachment.Ref); err != nil || ok {
		t.Fatalf("cross-account attachment lookup ok=%v err=%v", ok, err)
	}
	if _, _, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: "forged-message",
		VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "forged",
	}); err == nil {
		t.Fatal("expected forged message reference rejection")
	}
	if _, _, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID,
		VideoThreadID: "registered-source", VideoClipID: "videosrc_" + videoSourceDigest("not-attached"), ClientRequestID: "not-attached",
	}); err == nil {
		t.Fatal("expected unlinked video source rejection")
	}
	job, replayed, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref,
		ProviderID: "google", Model: "gemini", ModelSnapshot: "gemini-snapshot", MediaSettingsHash: "settings-v1",
	})
	if err != nil || replayed || job.Status != TranscriptionJobQueued || job.MessageID != message.ID {
		t.Fatalf("create job=%+v replayed=%v err=%v", job, replayed, err)
	}
	if _, ok, err := sessions.GetTranscriptionJob(session.AccountScopeID, "other-session", job.Ref); err != nil || ok {
		t.Fatalf("cross-session job lookup ok=%v err=%v", ok, err)
	}
	focused, _, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref,
		ProviderID: "google", Model: "gemini", ModelSnapshot: "gemini-snapshot", MediaSettingsHash: "settings-v1", FocusNotes: "Watch the title card",
	})
	if err != nil || focused.Ref == job.Ref || focused.MediaSettingsHash != job.MediaSettingsHash {
		t.Fatalf("focused job=%+v base=%+v err=%v", focused, job, err)
	}
}

func TestAudioTranscriptionBindsRegisteredSourceAndRevalidatesOpen(t *testing.T) {
	_, sessions, session, _ := setupTranscriptionContractTest(t)
	root := t.TempDir()
	path := filepath.Join(root, "speech.wav")
	payload := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sessions.PutAudioSourceRecord(AudioSourceRecord{AccountScopeID: session.AccountScopeID, WorkspaceID: "workspace", RootPath: root, RelativePath: "speech.wav", DisplayName: "speech.wav", MIMEType: "audio/wav", SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	attachment, replayed, err := sessions.BindAudioTranscriptionAttachment(BindAudioTranscriptionAttachmentInput{AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: "", AudioSourceRef: source.Ref, ClientRequestID: "bind-audio", NowUnixMs: 100})
	if err != nil || replayed || attachment.MediaKind != TranscriptionMediaAudio || attachment.SourceRecordRef != source.Ref || attachment.MessageID != "" {
		t.Fatalf("attachment=%+v replayed=%v err=%v", attachment, replayed, err)
	}
	job, replayed, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref,
		ProviderID: "google", Model: "gemini", ModelSnapshot: "snapshot", MediaSettingsHash: "settings",
	})
	if err != nil || replayed || job.MessageID != "" || job.AttachmentRef != attachment.Ref {
		t.Fatalf("job=%+v replayed=%v err=%v", job, replayed, err)
	}
	file, err := sessions.OpenTranscriptionAttachmentSource(session.AccountScopeID, session.ID, attachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := os.WriteFile(path, append(payload, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.OpenTranscriptionAttachmentSource(session.AccountScopeID, session.ID, attachment.Ref); err == nil {
		t.Fatal("expected changed audio source rejection")
	}
}

func TestNormalizedTranscriptWordTimingIsStableAndBounded(t *testing.T) {
	confidence := .91
	transcript := NormalizedTranscript{SchemaVersion: NormalizedTranscriptSchemaVersion, Ref: "transcript_" + transcriptionDigest("words"), JobRef: "trjob_" + transcriptionDigest("words-job"), AccountScopeID: "account", WorkspaceID: "workspace", SessionID: "session", MessageID: "message", AttachmentRef: "vatt_" + transcriptionDigest("words-att"), SourceFingerprint: transcriptionDigest("words-source"), ModelGenerated: true, Segments: []NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Speech: "hello world"}}, Words: []NormalizedTranscriptWord{{Text: "hello", StartMs: 10, EndMs: 250, Confidence: &confidence, Provenance: "google-stt"}, {Text: "world", StartMs: 300, EndMs: 700, Provenance: "google-stt"}}, Metadata: NormalizedTranscriptMetadata{DurationMs: 1000, ProviderID: "google", Model: "stt", ModelSnapshot: "snapshot", MediaSettingsHash: "settings"}, Validation: TranscriptValidation{State: TranscriptValidationValidated, ValidatedAt: 1}, CreatedAt: 1}
	normalized, err := normalizeAndValidateTranscript(transcript)
	if err != nil || len(normalized.Words) != 2 || normalized.Words[0].StartMs != 10 {
		t.Fatalf("normalized=%+v err=%v", normalized, err)
	}
	digest := normalized.ContentDigest
	normalizedAgain, err := normalizeAndValidateTranscript(normalized)
	if err != nil || normalizedAgain.ContentDigest != digest {
		t.Fatalf("digest=%s normalized=%+v err=%v", digest, normalizedAgain, err)
	}
	bad := transcript
	bad.Words = []NormalizedTranscriptWord{{Text: "bad", StartMs: 900, EndMs: 800, Provenance: "provider"}}
	if _, err := normalizeAndValidateTranscript(bad); err == nil {
		t.Fatal("expected invalid word timing rejection")
	}
}

func TestTranscriptionReadyRequiresDurableValidatedReadBack(t *testing.T) {
	_, sessions, session, message := setupTranscriptionContractTest(t)
	attachment, _, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID,
		VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "bind",
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	job, _, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref,
		ProviderID: "google", Model: "gemini", ModelSnapshot: "snapshot", MediaSettingsHash: "settings",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, _, err := sessions.TransitionTranscriptionJob(TransitionTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		ExpectedStatus: TranscriptionJobQueued, Status: TranscriptionJobReady, ClientRequestID: "forged-ready",
	}); err == nil {
		t.Fatal("expected ready transition without durable transcript to fail")
	}
	processing, _, err := sessions.TransitionTranscriptionJob(TransitionTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		ExpectedStatus: TranscriptionJobQueued, Status: TranscriptionJobProcessing, ClientRequestID: "processing",
	})
	if err != nil || processing.Status != TranscriptionJobProcessing {
		t.Fatalf("processing job=%+v err=%v", processing, err)
	}
	transcript, ready, _, err := sessions.CommitNormalizedTranscript(CommitNormalizedTranscriptInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		Segments: []NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Speech: "Model generated words.", Text: "forged model text"}},
		Language: "en", DurationMs: 1000, GeneratedAt: 100,
	})
	if err != nil || ready.Status != TranscriptionJobReady || !transcript.ModelGenerated || transcript.Validation.State != TranscriptValidationValidated || !strings.Contains(transcript.Text, "Speech: Model generated words.") || strings.Contains(transcript.Text, "forged model text") {
		t.Fatalf("commit transcript=%+v ready=%+v err=%v", transcript, ready, err)
	}
	loaded, ok, err := sessions.GetNormalizedTranscript(session.AccountScopeID, session.ID, transcript.Ref)
	if err != nil || !ok || loaded.ContentDigest != transcript.ContentDigest || loaded.Text != transcript.Text {
		t.Fatalf("read transcript=%+v ok=%v err=%v", loaded, ok, err)
	}
}

func TestLegacySpeechTranscriptRemainsReadable(t *testing.T) {
	store, sessions, session, _ := setupTranscriptionContractTest(t)
	legacy := NormalizedTranscript{
		SchemaVersion:  NormalizedTranscriptLegacyVersion,
		Ref:            "transcript_" + transcriptionDigest("legacy-transcript"),
		JobRef:         "trjob_" + transcriptionDigest("legacy-job"),
		AccountScopeID: session.AccountScopeID, WorkspaceID: "workspace", SessionID: session.ID,
		MessageID: "message", AttachmentRef: "vatt_" + transcriptionDigest("legacy-attachment"),
		SourceFingerprint: transcriptionDigest("legacy-source"), ModelGenerated: true,
		Text: "Legacy spoken words.", Segments: []NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Text: "Legacy spoken words."}},
		Metadata:   NormalizedTranscriptMetadata{ProviderID: "google", Model: "gemini", ModelSnapshot: "legacy", MediaSettingsHash: "legacy", GeneratedAt: 100},
		Validation: TranscriptValidation{State: TranscriptValidationValidated, ValidatedAt: 100}, CreatedAt: 100,
	}
	legacy, err := normalizeAndValidateTranscript(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(KeyNormalizedTranscript(session.AccountScopeID, session.ID, legacy.Ref), legacy); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := sessions.GetNormalizedTranscript(session.AccountScopeID, session.ID, legacy.Ref)
	if err != nil || !ok || loaded.SchemaVersion != NormalizedTranscriptLegacyVersion || loaded.Text != legacy.Text {
		t.Fatalf("legacy transcript=%+v ok=%v err=%v", loaded, ok, err)
	}
}

func TestTranscriptionReadyAcceptsVisualOnlyTimeline(t *testing.T) {
	_, sessions, session, message := setupTranscriptionContractTest(t)
	attachment, _, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID,
		VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "bind-visual",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref,
		ProviderID: "google", Model: "gemini", ModelSnapshot: "snapshot", MediaSettingsHash: "settings-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = sessions.TransitionTranscriptionJob(TransitionTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		ExpectedStatus: TranscriptionJobQueued, Status: TranscriptionJobProcessing, ClientRequestID: "processing-visual",
	}); err != nil {
		t.Fatal(err)
	}
	transcript, ready, _, err := sessions.CommitNormalizedTranscript(CommitNormalizedTranscriptInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		Segments:   []NormalizedTranscriptSegment{{StartMs: 0, EndMs: 2000, Visual: "A cursor opens Settings.", Text: "Visual: A cursor opens Settings."}},
		DurationMs: 2000, Summary: "A silent settings demonstration.", GeneratedAt: 100,
	})
	if err != nil || ready.Status != TranscriptionJobReady || transcript.Metadata.Summary == "" || transcript.Segments[0].Speech != "" || !strings.Contains(transcript.Text, "A silent settings demonstration") {
		t.Fatalf("transcript=%+v ready=%+v err=%v", transcript, ready, err)
	}
}

func TestTranscriptLookupByWorkspaceSupportsDurableCrossSessionRead(t *testing.T) {
	_, sessions, session, message := setupTranscriptionContractTest(t)
	attachment, _, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID, VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "bind-lookup"})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref, ProviderID: "google", Model: "gemini", ModelSnapshot: "snapshot", MediaSettingsHash: "settings-lookup"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = sessions.TransitionTranscriptionJob(TransitionTranscriptionJobInput{AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref, ExpectedStatus: TranscriptionJobQueued, Status: TranscriptionJobProcessing, ClientRequestID: "processing-lookup"}); err != nil {
		t.Fatal(err)
	}
	transcript, _, _, err := sessions.CommitNormalizedTranscript(CommitNormalizedTranscriptInput{AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref, Segments: []NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Visual: "A title card.", Text: "Visual: A title card."}}, DurationMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := sessions.FindNormalizedTranscriptByRef(session.AccountScopeID, session.UserID, "workspace", transcript.Ref)
	if err != nil || !ok || found.SessionID != session.ID {
		t.Fatalf("found=%+v ok=%v err=%v", found, ok, err)
	}
	if _, ok, err := sessions.FindNormalizedTranscriptByRef(session.AccountScopeID, session.UserID, "other-workspace", transcript.Ref); err != nil || ok {
		t.Fatalf("cross-workspace lookup ok=%v err=%v", ok, err)
	}
}

func TestTranscriptionTerminalStatesNeverBecomeReadyAndSessionDeletePurgesRecords(t *testing.T) {
	_, sessions, session, message := setupTranscriptionContractTest(t)
	attachment, _, err := sessions.BindVideoTranscriptionAttachment(BindVideoTranscriptionAttachmentInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, MessageID: message.ID,
		VideoThreadID: "registered-source", VideoClipID: message.VideoAttachments[0].Ref, ClientRequestID: "bind",
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	job, _, err := sessions.CreateTranscriptionJob(CreateTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, AttachmentRef: attachment.Ref,
		ProviderID: "google", Model: "gemini", ModelSnapshot: "snapshot", MediaSettingsHash: "settings",
		ProviderCacheExpiresAt: 10,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	failed, _, err := sessions.TransitionTranscriptionJob(TransitionTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		ExpectedStatus: TranscriptionJobQueued, Status: TranscriptionJobFailed, FailureCode: "provider", FailureReason: "provider cache expired", ClientRequestID: "failed",
	})
	if err != nil || failed.Status != TranscriptionJobFailed {
		t.Fatalf("failed job=%+v err=%v", failed, err)
	}
	if _, _, err := sessions.TransitionTranscriptionJob(TransitionTranscriptionJobInput{
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, SessionID: session.ID, JobRef: job.Ref,
		ExpectedStatus: TranscriptionJobFailed, Status: TranscriptionJobReady, ClientRequestID: "invalid-ready",
	}); err == nil {
		t.Fatal("expected failed-to-ready transition rejection")
	}
	if err := sessions.DeleteSession(session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, ok, err := sessions.GetTranscriptionAttachment(session.AccountScopeID, session.ID, attachment.Ref); err != nil || ok {
		t.Fatalf("deleted attachment ok=%v err=%v", ok, err)
	}
	if _, ok, err := sessions.GetTranscriptionJob(session.AccountScopeID, session.ID, job.Ref); err != nil || ok {
		t.Fatalf("deleted job ok=%v err=%v", ok, err)
	}
}
