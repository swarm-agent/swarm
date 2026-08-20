package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videotranscription"
)

// TestVideoStudioDeterministicIntegrationProof exercises the launch workflow
// without private footage or an external renderer. The synthetic source and
// rendered artifact are deliberately tiny; the proof targets durable authority
// and revision lineage rather than media codec behavior.
func TestVideoStudioDeterministicIntegrationProof(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newArtifactSessionFixture(t, "fixture.txt", "video proof")
	store := sessionSvc.Store()
	projects := videoproject.NewService(store)
	server.SetVideoProjectService(projects)
	principal := testPrincipal()
	ctx := context.Background()

	session, ok, err := store.GetSession("artifact-session")
	if err != nil || !ok {
		t.Fatalf("get fixture session: ok=%t err=%v", ok, err)
	}
	const workspaceID = "video-proof-workspace"
	metadata := map[string]any{
		"workspace_id":  workspaceID,
		"session_kind":  "video",
		"creative_mode": "video",
	}
	if _, _, err := sessionSvc.UpdateMetadata(session.ID, metadata); err != nil {
		t.Fatalf("classify video session: %v", err)
	}
	classified, ok, err := store.GetSession(session.ID)
	if err != nil || !ok || classified.Metadata["session_kind"] != "video" || classified.Metadata["creative_mode"] != "video" {
		t.Fatalf("video session classification missing: session=%+v ok=%t err=%v", classified, ok, err)
	}

	// Register and attach repository-safe synthetic media through the trusted,
	// path-free source boundary.
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "synthetic.mp4")
	if err := os.WriteFile(sourcePath, []byte("00000000ftypisom-swarm-video-proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.PutVideoSourceRecord(pebblestore.VideoSourceRecord{
		AccountScopeID: principal.AccountScopeID,
		WorkspaceID:    workspaceID,
		RootPath:       sourceRoot,
		RelativePath:   "synthetic.mp4",
		DisplayName:    "synthetic.mp4",
		MIMEType:       "video/mp4",
		SizeBytes:      info.Size(),
		ModifiedAt:     info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("register trusted source: %v", err)
	}
	messageResult, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       session.ID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "video-proof-source-message",
		PayloadHash:     "video-proof-source-message",
		Kind:            pebblestore.V3SessionMutationAppendMessage,
		Message: &pebblestore.MessageSnapshot{
			ID: "video-proof-message", Role: "user", Content: "Use the synthetic fixture",
			VideoAttachments: []pebblestore.SessionVideoAttachmentReference{{Ref: source.Ref}},
		},
	})
	if err != nil || messageResult.Message == nil || len(messageResult.Message.VideoAttachments) != 1 || messageResult.Message.VideoAttachments[0].SourceFingerprint != source.SourceFingerprint {
		t.Fatalf("trusted source message result=%+v err=%v", messageResult, err)
	}

	attachment, _, err := store.BindVideoTranscriptionAttachment(pebblestore.BindVideoTranscriptionAttachmentInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: session.ID,
		MessageID: messageResult.Message.ID, VideoThreadID: "registered-source", VideoClipID: source.Ref,
		ClientRequestID: "video-proof-bind", NowUnixMs: 100,
	})
	if err != nil {
		t.Fatalf("bind transcript source: %v", err)
	}
	transcriptionJob, _, err := store.CreateTranscriptionJob(pebblestore.CreateTranscriptionJobInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: session.ID,
		AttachmentRef: attachment.Ref, ProviderID: "fixture", Model: "fixture", ModelSnapshot: "fixture-v1",
		MediaSettingsHash: "fixture-settings", NowUnixMs: 200,
	})
	if err != nil {
		t.Fatalf("create transcript fixture job: %v", err)
	}
	if _, _, err := store.TransitionTranscriptionJob(pebblestore.TransitionTranscriptionJobInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: session.ID,
		JobRef: transcriptionJob.Ref, ExpectedStatus: pebblestore.TranscriptionJobQueued,
		Status: pebblestore.TranscriptionJobProcessing, ClientRequestID: "video-proof-processing", NowUnixMs: 300,
	}); err != nil {
		t.Fatalf("start transcript fixture: %v", err)
	}
	transcript, readyTranscription, _, err := store.CommitNormalizedTranscript(pebblestore.CommitNormalizedTranscriptInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: session.ID,
		JobRef: transcriptionJob.Ref, DurationMs: 20_000, Summary: "Synthetic launch walkthrough", GeneratedAt: 350, NowUnixMs: 400,
		Segments: []pebblestore.NormalizedTranscriptSegment{
			{StartMs: 0, EndMs: 10_000, Visual: "Opening title and product view", OnScreenText: "Swarm Video Studio"},
			{StartMs: 10_000, EndMs: 20_000, Visual: "Timeline transition preview", Speech: "Preview before rendering"},
		},
	})
	if err != nil || readyTranscription.Status != pebblestore.TranscriptionJobReady {
		t.Fatalf("commit transcript fixture: transcript=%+v job=%+v err=%v", transcript, readyTranscription, err)
	}
	sectionIndex, _, err := videotranscription.BuildVideoSectionIndex(transcript)
	if err != nil || sectionIndex.Source.TranscriptRef != transcript.Ref || sectionIndex.Quality.EvidenceCount == 0 {
		t.Fatalf("build transcript evidence boundary: index=%+v err=%v", sectionIndex, err)
	}

	initialTimeline := &pebblestore.VideoProjectTimeline{
		OutputPreset: pebblestore.VideoPresetLandscape1080p,
		Clips: []pebblestore.VideoTimelineClip{
			{ID: "clip-a", Track: 0, Sequence: 0, SourceKind: pebblestore.VideoClipSourceKindSourceVideo, SourceRef: source.Ref, SourceStartMs: 0, SourceEndMs: 10_000, TimelineStartMs: 0, TimelineEndMs: 10_000, DurationMs: 10_000, Visible: true},
			{ID: "clip-b", Track: 0, Sequence: 1, SourceKind: pebblestore.VideoClipSourceKindSourceVideo, SourceRef: source.Ref, SourceStartMs: 10_000, SourceEndMs: 20_000, TimelineStartMs: 10_000, TimelineEndMs: 20_000, DurationMs: 10_000, Visible: true},
		},
		Metadata: map[string]any{"transcript_refs": []string{transcript.Ref}, "evidence_refs": []string{sectionIndex.Source.TranscriptContentDigest}},
	}
	project, base, err := projects.GetOrCreatePrimaryVideoToolProject(ctx, principal, videoproject.CreateProjectInput{
		SessionID: session.ID, WorkspaceID: workspaceID, ProjectID: "video-proof-project", Title: "Video proof",
		OutputPreset: pebblestore.VideoPresetLandscape1080p, InitialTimeline: initialTimeline,
		Metadata: map[string]any{"transcript_ref": transcript.Ref}, NowUnixMs: 500,
	})
	if err != nil || base == nil || project.ProjectKind != pebblestore.VideoProjectKindVideoTool || base.RevisionNumber != 1 {
		t.Fatalf("create primary video project: project=%+v base=%+v err=%v", project, base, err)
	}

	transition := pebblestore.VideoTimelineTransition{ID: "transition-a-b", Kind: pebblestore.VideoTransitionKindCrossfade, FromClipID: "clip-a", ToClipID: "clip-b", DurationMs: 500}
	proposal, err := projects.CreateEditProposal(ctx, principal, videoproject.CreateEditProposalInput{
		SessionID: session.ID, ProjectID: project.ID, ProposalID: "video-proof-proposal", BaseRevisionID: base.ID,
		Title: "Preview crossfade", Rationale: "Browser-previewable typed transition",
		Operations: []pebblestore.VideoEditOperation{{ID: "add-transition", Type: pebblestore.VideoEditOperationAddTransition, Transition: &transition}},
		AffectedRanges: []pebblestore.VideoTimelineRange{{StartMs: 9_500, EndMs: 10_500}}, NowUnixMs: 600,
	})
	if err != nil {
		t.Fatalf("create non-mutating proposal: %v", err)
	}
	unchanged, _, err := projects.GetProject(principal, session.ID, project.ID)
	if err != nil || unchanged.CurrentRevisionID != base.ID || unchanged.RevisionCount != 1 || proposal.Operations[0].Transition == nil || proposal.Operations[0].Transition.Kind != pebblestore.VideoTransitionKindCrossfade {
		t.Fatalf("proposal mutated canonical state or lacks preview contract: project=%+v proposal=%+v err=%v", unchanged, proposal, err)
	}

	acceptedProposal, acceptedRevision, acceptedProject, err := projects.AcceptEditProposal(ctx, principal, videoproject.AcceptEditProposalInput{
		SessionID: session.ID, ProjectID: project.ID, ProposalID: proposal.ID, RevisionID: "video-proof-accepted",
		SelectedOperationIDs: []string{"add-transition"}, AuthorPrincipal: principal.UserID, NowUnixMs: 700,
	})
	if err != nil || acceptedProposal.AcceptedRevisionID != acceptedRevision.ID || acceptedProject.RevisionCount != 2 || len(acceptedRevision.Timeline.Transitions) != 1 {
		t.Fatalf("accept exactly one revision: proposal=%+v revision=%+v project=%+v err=%v", acceptedProposal, acceptedRevision, acceptedProject, err)
	}
	if _, _, _, err := projects.AcceptEditProposal(ctx, principal, videoproject.AcceptEditProposalInput{
		SessionID: session.ID, ProjectID: project.ID, ProposalID: proposal.ID, RevisionID: "video-proof-duplicate",
		SelectedOperationIDs: []string{"add-transition"}, AuthorPrincipal: principal.UserID, NowUnixMs: 701,
	}); err == nil {
		t.Fatal("accepted proposal created more than one revision")
	}

	renderJob, err := projects.StartRenderJob(ctx, principal, videoproject.StartRenderJobInput{
		SessionID: session.ID, ProjectID: project.ID, RevisionID: acceptedRevision.ID, JobID: "video-proof-render", NowUnixMs: 800,
	})
	if err != nil || renderJob.RevisionID != acceptedRevision.ID {
		t.Fatalf("render is not pinned to accepted revision: job=%+v err=%v", renderJob, err)
	}
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(ctx, artifact.Principal{SessionID: session.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{
		RequestID: "video-proof-artifact", CollectionID: "video-proof-renders", CollectionName: "Final renders",
		VariantID: "video-proof-mp4", Filename: "video-proof.mp4", MediaType: "video/mp4", Body: []byte("synthetic-rendered-video"),
	})
	if err != nil {
		t.Fatalf("create ready render artifact: %v", err)
	}
	artifactRef := &pebblestore.SessionArtifactSelectionReference{SessionID: session.ID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq, Action: "use"}
	readyRender, err := projects.CompleteRenderJob(ctx, principal, videoproject.CompleteRenderJobInput{
		SessionID: session.ID, JobID: renderJob.ID, OutputPreset: pebblestore.VideoPresetLandscape1080p,
		OutputWidth: 1920, OutputHeight: 1080, OutputFPS: 30, OutputDurationMs: 20_000,
		OutputSizeBytes: int64(len("synthetic-rendered-video")), OutputDigestSHA256: variant.DigestSHA256,
		OutputArtifact: artifactRef, NowUnixMs: 900,
	})
	if err != nil || readyRender.Status != pebblestore.VideoRenderJobStatusReady || readyRender.OutputArtifact == nil {
		t.Fatalf("complete ready render: job=%+v err=%v", readyRender, err)
	}

	exportPath := filepath.Join(session.WorkspacePath, "exports", "video-proof.mp4")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v3/sessions/"+session.ID+"/video/projects/"+project.ID+"/export", nil)
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
	server.handleSessionV3VideoExport(recorder, request, principal, session.ID, project.ID, sessionV3ExportVideoRequest{DestinationPath: exportPath, JobID: readyRender.ID})
	if recorder.Code != 200 {
		t.Fatalf("export ready artifact status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	exported, err := os.ReadFile(exportPath)
	if err != nil || string(exported) != "synthetic-rendered-video" {
		t.Fatalf("exported artifact mismatch: bytes=%q err=%v", exported, err)
	}
}
