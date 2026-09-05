package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/videoproject"
)

type artifactV3RuntimeRenderer struct{}

func (artifactV3RuntimeRenderer) Capture(_ context.Context, request htmlcapture.Request) ([]htmlcapture.Result, error) {
	if request.Entry == "" || len(request.Files) == 0 || request.ViewportWidth != 1440 || request.ViewportHeight != 900 {
		return nil, htmlcapture.NewError("capture_invalid", "invalid capture")
	}
	return []htmlcapture.Result{{StateID: "default", PNG: []byte("real-renderer-evidence")}}, nil
}

type artifactV3ConversionRenderer struct{}

func (artifactV3ConversionRenderer) PreflightAnimation(_ context.Context, request htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	return htmlcapture.AnimationResult{DurationMS: request.DurationMS, FPS: request.FPS, FrameCount: (request.DurationMS*request.FPS + 999) / 1000}, nil
}

func (artifactV3ConversionRenderer) RenderAnimation(_ context.Context, request htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("runtime-evidence")...)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	return htmlcapture.AnimationResult{PreviewPNG: png, MP4: mp4, DurationMS: request.DurationMS, FPS: request.FPS, FrameCount: (request.DurationMS*request.FPS + 999) / 1000}, nil
}

// Requirement: the production bridge must connect managed whole-tree authoring,
// exact Git/Pebble state, API reads, and restart recovery without V2 records.
func TestArtifactV3RuntimeAdapterProductionPathAndRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "session.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "artifact-v3-runtime", AccountScopeID: "account", UserID: "user", Title: "Artifact V3", WorkspacePath: root, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot, workspaceRoot, evidenceRoot, err := artifactV3StorageRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), repositoryRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), repositoryRoot, evidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	adapter.publish = func(identity.Principal, api.ArtifactV3Artifact, string, string) error { return nil }
	author := tool.NewArtifactV3AuthorService(workspaceRoot, adapter, adapter, adapter)
	grant, err := author.PrepareTurn(context.Background(), tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "artifact-v3-runtime", TaskCallID: "call", Prompt: "create", PolicyRevision: "policy", CandidateIndex: 1, Initial: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	principal := tool.ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "child", ProducerRunID: "run"}
	grant.ProducerSessionID = "child"
	grant.ProducerRunID = "run"
	manifest, _ := json.Marshal(pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: []pebblestore.ArtifactV3Part{{ID: "hero", Label: "Hero", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}}}})
	if err := author.Create(context.Background(), principal, grant, "swarm-artifact.json", manifest); err != nil {
		t.Fatal(err)
	}
	if err := author.Create(context.Background(), principal, grant, "index.html", []byte(`<!doctype html><html><head><link rel="stylesheet" href="styles/theme.css"></head><body><main id="hero">Artifact V3</main><script type="module" src="src/app.js"></script></body></html>`)); err != nil {
		t.Fatal(err)
	}
	if err := author.Create(context.Background(), principal, grant, "styles/theme.css", []byte(`body{color:navy}`)); err != nil {
		t.Fatal(err)
	}
	if err := author.Create(context.Background(), principal, grant, "src/app.js", []byte(`document.body.dataset.ready="true"`)); err != nil {
		t.Fatal(err)
	}
	gate, err := author.BuildPreview(context.Background(), principal, grant)
	if err != nil || !gate.Ready || len(gate.Preview.EvidenceDigests) != 1 {
		t.Fatalf("gate=%+v err=%v", gate, err)
	}
	finished, err := author.Finish(context.Background(), principal, grant)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Head == nil || artifact.Head.CommitOID != finished.Revision.CommitOID || artifact.Head.Build == nil || artifact.Head.Build.Status != "succeeded" || artifact.Head.Validation == nil || artifact.Head.Validation.Status != "valid" {
		t.Fatalf("artifact=%+v finish=%+v", artifact, finished)
	}
	followup := tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "artifact-v3-runtime", TaskCallID: "followup", ArtifactID: grant.ArtifactID, BaseCommitOID: artifact.Head.CommitOID, ProjectionSeq: artifact.Revision, PolicyRevision: "policy", CandidateIndex: 1, Initial: false, TargetPartIDs: []string{"hero"}, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	preparedFollowup, err := adapter.PrepareArtifactV3Turn(context.Background(), followup)
	if err != nil {
		t.Fatalf("valid manifest target was rejected: %v", err)
	}
	// Requirement: one exact-base wave allocates distinct sibling slots; a
	// failed sibling retains the good candidate and cannot advance head. This
	// real Git/Pebble boundary tests durable postconditions, not renderer quality.
	siblingRequest := followup
	siblingRequest.CandidateIndex = 2
	sibling, err := adapter.PrepareArtifactV3Turn(context.Background(), siblingRequest)
	if err != nil || sibling.ArtifactID != preparedFollowup.ArtifactID || sibling.TurnID != preparedFollowup.TurnID || sibling.CandidateID == preparedFollowup.CandidateID || sibling.BaseCommitOID != preparedFollowup.BaseCommitOID {
		t.Fatalf("sibling allocation=%+v err=%v", sibling, err)
	}
	followPrincipal := tool.ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "child", ProducerRunID: "repair-run"}
	preparedFollowup.ProducerSessionID, preparedFollowup.ProducerRunID = "child", "repair-run"
	if _, err := author.Inspect(context.Background(), followPrincipal, preparedFollowup); err != nil {
		t.Fatalf("materialize direct repair base: %v", err)
	}
	if err := author.Edit(context.Background(), followPrincipal, preparedFollowup, "index.html", []byte("Artifact V3"), []byte("Artifact V3 repaired"), false); err != nil {
		t.Fatal(err)
	}
	if gate, err := author.BuildPreview(context.Background(), followPrincipal, preparedFollowup); err != nil || !gate.Ready {
		t.Fatalf("repair gate=%+v err=%v", gate, err)
	}
	repair, err := author.Finish(context.Background(), followPrincipal, preparedFollowup)
	if err != nil {
		t.Fatal(err)
	}
	sibling.ProducerSessionID, sibling.ProducerRunID = "sibling-child", "failed-run"
	if err := author.MarkFailed(context.Background(), sibling, "child_run_failed", "injected sibling failure"); err != nil {
		t.Fatal(err)
	}
	good, goodOK, goodErr := sessions.Store().GetArtifactV3Candidate("account", "user", grant.ArtifactID, preparedFollowup.TurnID, preparedFollowup.CandidateID)
	bad, badOK, badErr := sessions.Store().GetArtifactV3Candidate("account", "user", grant.ArtifactID, sibling.TurnID, sibling.CandidateID)
	if goodErr != nil || badErr != nil || !goodOK || !badOK || good.Status != "ready" || good.CommitOID != repair.Revision.CommitOID || bad.Status != "failed" || bad.CommitOID != "" {
		t.Fatalf("partial wave good=%+v bad=%+v errors=%v/%v", good, bad, goodErr, badErr)
	}
	beforeSelect, err := adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil || beforeSelect.Head.CommitOID != finished.Revision.CommitOID {
		t.Fatalf("repair candidate moved head before selection: artifact=%+v err=%v", beforeSelect, err)
	}
	// Requirement: Desktop must see both durable slots, not only Git refs.
	// Threat: Git-only enumeration silently erases failed siblings from review.
	var visibleGood, visibleBad bool
	for _, turn := range beforeSelect.Turns {
		for _, candidate := range turn.Candidates {
			if candidate.CandidateID == good.CandidateID {
				visibleGood = candidate.Status == "ready" && candidate.Revision != nil && candidate.Revision.CommitOID == good.CommitOID
			}
			if candidate.CandidateID == bad.CandidateID {
				visibleBad = candidate.Status == "failed" && candidate.Revision == nil && len(candidate.Diagnostics) == 1 && candidate.Diagnostics[0].Code == "child_run_failed"
			}
		}
	}
	if !visibleGood || !visibleBad {
		t.Fatalf("partial wave hidden from API: %+v", beforeSelect.Turns)
	}
	for _, owner := range [][2]string{{"foreign-account", "user"}, {"account", "foreign-user"}} {
		if rows, err := sessions.Store().ListArtifactV3CandidateProjections(owner[0], owner[1], grant.ArtifactID); err == nil || len(rows) != 0 {
			t.Fatalf("foreign candidate enumeration: %+v %v", rows, err)
		}
	}
	// Requirement: read/render authority for A survives selecting B and restart;
	// conversion authority still rejects A. Use the real Git/Pebble adapter and
	// private receipt store, with only rendering faked at this narrow boundary.
	oldDerivatives, err := newArtifactV3DerivativeStore(filepath.Join(root, "old-video-derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	oldVideo := artifactv3video.New(adapter, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, oldDerivatives)
	oldSelection, err := adapter.videoSelection(identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef)
	if err != nil {
		t.Fatal(err)
	}
	oldConversion, err := oldVideo.Convert(context.Background(), "account", oldSelection)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := adapter.SelectArtifactV3DirectHead(context.Background(), "account", "user", "artifact-v3-runtime", grant.ArtifactID, preparedFollowup.TurnID, preparedFollowup.CandidateID)
	if err != nil || selected.CommitOID != repair.Revision.CommitOID {
		t.Fatalf("direct repair selection=%+v repair=%+v err=%v", selected, repair, err)
	}
	artifact, err = adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil || artifact.Head.CommitOID != repair.Revision.CommitOID || len(artifact.Head.Parents) != 1 || artifact.Head.Parents[0] != finished.Revision.CommitOID {
		t.Fatalf("selected direct repair lineage artifact=%+v err=%v", artifact, err)
	}
	artifactBeforeVideo := artifact
	if body, err := oldVideo.ReadVideoReference(context.Background(), "account", "user", oldConversion.MP4); err != nil || string(body[4:8]) != "ftyp" {
		t.Fatalf("A failed after selecting B: %v", err)
	}
	if _, err := oldVideo.Convert(context.Background(), "account", oldSelection); err == nil {
		t.Fatal("stale A reconverted after selecting B")
	}
	for _, mutate := range []func(*pebblestore.ArtifactV3VideoReference){
		func(r *pebblestore.ArtifactV3VideoReference) { r.DurationMs++ },
		func(r *pebblestore.ArtifactV3VideoReference) { r.CaptureStateID = "forged" },
		func(r *pebblestore.ArtifactV3VideoReference) { r.MediaType = "image/png" },
	} {
		ref := oldConversion.MP4
		mutate(&ref)
		if _, err := oldVideo.ReadVideoReference(context.Background(), "account", "user", ref); err == nil {
			t.Fatal("forged receipt read")
		}
	}

	// Requirement: the native runtime bridge converts this exact selected Git
	// head into one pending V3-only proposal and does not mutate the source head.
	projectService := videoproject.NewService(sessions.Store())
	derivatives, err := newArtifactV3DerivativeStore(filepath.Join(root, "video-derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	videoService := artifactv3video.New(adapter, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, derivatives)
	projectService.SetArtifactV3Authority(videoService)
	bridge := &artifactV3VideoBridge{artifacts: adapter, service: videoService, projects: projectService}
	principalIdentity := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user", SessionID: "artifact-v3-runtime"}
	videoProject, videoBase, err := projectService.CreateProject(context.Background(), principalIdentity, videoproject.CreateProjectInput{SessionID: "artifact-v3-runtime", Title: "Native V3", OutputPreset: pebblestore.VideoPresetLandscape1080p, ProjectID: "native-v3-project"})
	if err != nil || videoBase == nil {
		t.Fatalf("create native V3 video project: project=%+v base=%+v err=%v", videoProject, videoBase, err)
	}
	conversionInput := tool.ArtifactV3VideoConversionInput{RequestID: "convert-native-v3", VideoSessionID: "artifact-v3-runtime", ProjectID: videoProject.ID, BaseRevisionID: videoBase.ID, ArtifactSessionID: "artifact-v3-runtime", ArtifactID: grant.ArtifactID, RevisionRef: artifact.Head.RevisionRef}
	staleInput := conversionInput
	staleInput.BaseRevisionID = "stale-base"
	if _, staleErr := bridge.ConvertToPendingProposal(context.Background(), principalIdentity, staleInput); staleErr == nil || !strings.Contains(staleErr.Error(), "stale") {
		t.Fatalf("stale project base error=%v", staleErr)
	}
	foreignInput := conversionInput
	foreignInput.ArtifactSessionID = "foreign"
	if _, foreignErr := bridge.ConvertToPendingProposal(context.Background(), principalIdentity, foreignInput); foreignErr == nil {
		t.Fatal("foreign source session was accepted")
	}
	if proposals, listErr := projectService.ListEditProposals(principalIdentity, "artifact-v3-runtime", videoProject.ID, 10); listErr != nil || len(proposals) != 0 {
		t.Fatalf("rejected conversion mutated proposals: proposals=%+v err=%v", proposals, listErr)
	}
	if entries, readErr := os.ReadDir(derivatives.root); readErr != nil || len(entries) != 0 {
		t.Fatalf("rejected conversion wrote derivatives: entries=%v err=%v", entries, readErr)
	}
	if afterRejected, readErr := adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID); readErr != nil || !reflect.DeepEqual(afterRejected, artifactBeforeVideo) {
		t.Fatalf("rejected conversion mutated source projection: artifact=%+v before=%+v err=%v", afterRejected, artifactBeforeVideo, readErr)
	}
	proposal, err := bridge.ConvertToPendingProposal(context.Background(), principalIdentity, conversionInput)
	if err != nil {
		t.Fatalf("convert native V3 video: %v", err)
	}
	if proposal.Status != pebblestore.VideoEditProposalStatusPending || proposal.Intent != pebblestore.VideoEditProposalIntentArtifactV3Convert || proposal.AcceptedRevisionID != "" || proposal.WorkingRevisionID == "" || proposal.Plan == nil || len(proposal.Plan.Parts) != 1 {
		t.Fatalf("native V3 proposal=%+v", proposal)
	}
	proposals, listErr := projectService.ListEditProposals(principalIdentity, "artifact-v3-runtime", videoProject.ID, 10)
	if listErr != nil || len(proposals) != 1 || proposals[0].ID != proposal.ID {
		t.Fatalf("native V3 conversion did not create exactly one proposal: proposals=%+v err=%v", proposals, listErr)
	}
	videoPart := proposal.Plan.Parts[0]
	if videoPart.ArtifactV3Source == nil || videoPart.ArtifactV3Still == nil || videoPart.ArtifactV3Visual == nil || videoPart.Visual != nil || videoPart.ArtifactV2Source != nil || videoPart.ArtifactV2Still != nil || videoPart.ArtifactV2Visual != nil || videoPart.ArtifactV3Source.CommitOID != artifact.Head.CommitOID || videoPart.ArtifactV3Source.TreeOID != artifact.Head.TreeOID || videoPart.ArtifactV3Source.BuildID != artifact.Head.Build.ID || videoPart.ArtifactV3Source.ValidationID != artifact.Head.Validation.ID || videoPart.ArtifactV3Visual.DurationMs != videoPart.DurationMs || videoPart.SourceEndMs != videoPart.DurationMs {
		t.Fatalf("native V3 proposal identity drifted: %+v", videoPart)
	}
	if fallback, readErr := videoService.ReadVideoReference(context.Background(), "account", "user", *videoPart.ArtifactV3Still); readErr != nil || len(fallback) < 8 || string(fallback[1:4]) != "PNG" {
		t.Fatalf("fallback bytes=%q err=%v", fallback, readErr)
	}
	if mp4, readErr := videoService.ReadVideoReference(context.Background(), "account", "user", *videoPart.ArtifactV3Visual); readErr != nil || len(mp4) < 12 || string(mp4[4:8]) != "ftyp" {
		t.Fatalf("mp4 bytes=%q err=%v", mp4, readErr)
	}
	// Requirement: cancellation after atomic derivative publication must not
	// publish a proposal or mutate the accepted cut. Derivatives already committed
	// to their private store remain readable; the two stores are not one transaction.
	cancelStore, err := newArtifactV3DerivativeStore(filepath.Join(root, "cancelled-proposal-derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancelProposal := context.WithCancel(context.Background())
	cancelVideo := artifactv3video.New(adapter, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, afterPublishDerivativeStore{DerivativeStore: cancelStore, afterPublish: cancelProposal})
	cancelBridge := &artifactV3VideoBridge{artifacts: adapter, service: cancelVideo, projects: projectService}
	cancelInput := conversionInput
	cancelInput.RequestID = "cancel-after-publication"
	cancelInput.BaseRevisionID = proposal.WorkingRevisionID
	if _, err := cancelBridge.ConvertToPendingProposal(cancelCtx, principalIdentity, cancelInput); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-publication cancellation err=%v", err)
	}
	if proposals, err := projectService.ListEditProposals(principalIdentity, "artifact-v3-runtime", videoProject.ID, 10); err != nil || len(proposals) != 1 || proposals[0].ID != proposal.ID {
		t.Fatalf("cancellation published proposal: %+v err=%v", proposals, err)
	}
	if current, ok, err := projectService.GetProject(principalIdentity, "artifact-v3-runtime", videoProject.ID); err != nil || !ok || current.ConfirmedRevisionID != videoBase.ID || current.CurrentRevisionID != proposal.WorkingRevisionID {
		t.Fatalf("cancellation changed accepted cut: %+v err=%v", current, err)
	}
	if body, err := cancelVideo.ReadVideoReference(context.Background(), "account", "user", *videoPart.ArtifactV3Visual); err != nil || len(body) < 12 {
		t.Fatalf("committed derivative was not retained: %v", err)
	}
	if afterConversion, readErr := adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID); readErr != nil || !reflect.DeepEqual(afterConversion, artifactBeforeVideo) {
		t.Fatalf("conversion mutated source projection: artifact=%+v before=%+v err=%v", afterConversion, artifactBeforeVideo, readErr)
	}

	// Requirement: the explicit startup migration preserves pre-receipt media
	// using durable revision references, never a caller's claimed provenance.
	legacyStore, err := newArtifactV3DerivativeStore(filepath.Join(root, "pre-receipt-media"))
	if err != nil {
		t.Fatal(err)
	}
	legacyIDs := []string{videoPart.ArtifactV3Still.DerivativeID, videoPart.ArtifactV3Visual.DerivativeID}
	sort.Strings(legacyIDs)
	legacyDir := filepath.Join(legacyStore.root, artifactV3StorageKey(videoPart.ArtifactV3Source.SessionID, grant.ArtifactID), artifactV3StorageKey(legacyIDs...))
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []*pebblestore.ArtifactV3VideoReference{videoPart.ArtifactV3Still, videoPart.ArtifactV3Visual} {
		body, err := videoService.ReadVideoReference(context.Background(), "account", "user", *ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacyDir, ref.DerivativeID), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "complete"), []byte(strings.Join(legacyIDs, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyStore.Read(context.Background(), videoPart.ArtifactV3Visual.SessionID, grant.ArtifactID, *videoPart.ArtifactV3Visual); err == nil {
		t.Fatal("receipt-less media readable before explicit migration")
	}
	for i := 0; i < 2; i++ {
		if err := migrateArtifactV3VideoReceipts(context.Background(), adapter, legacyStore); err != nil {
			t.Fatalf("explicit receipt migration: %v", err)
		}
	}
	migratedVideo := artifactv3video.New(adapter, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, legacyStore)
	if _, err := migratedVideo.ReadVideoReference(context.Background(), "account", "user", *videoPart.ArtifactV3Visual); err != nil {
		t.Fatalf("migrated exact media unavailable: %v", err)
	}
	forged := *videoPart.ArtifactV3Visual
	forged.EventSeq++
	if _, err := migratedVideo.ReadVideoReference(context.Background(), "account", "user", forged); err == nil {
		t.Fatal("migration blessed a caller-forged event sequence")
	}

	followup.TaskCallID, followup.TargetPartIDs, followup.BaseCommitOID, followup.ProjectionSeq = "unknown-target", []string{"missing"}, artifact.Head.CommitOID, artifact.Revision
	if _, err := adapter.PrepareArtifactV3Turn(context.Background(), followup); !errors.Is(err, pebblestore.ErrArtifactV3Invalid) {
		t.Fatalf("unknown manifest target error=%v", err)
	}
	preview, err := adapter.OpenPreview(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef, "", "")
	if err != nil || !strings.Contains(string(preview.Body), "Artifact V3") || !strings.Contains(string(preview.Body), "preview/files/styles/theme.css") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	asset, err := adapter.OpenPreview(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef, "styles/theme.css", "")
	if err != nil || !strings.HasPrefix(asset.MediaType, "text/css") || !strings.Contains(string(asset.Body), "navy") {
		t.Fatalf("asset=%+v err=%v", asset, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := pebblestore.Open(filepath.Join(root, "session.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedService, err := pebblestore.NewArtifactV3Service(pebblestore.NewSessionStore(reopened), repositoryRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newArtifactV3RuntimeAdapter(restartedService, pebblestore.NewSessionStore(reopened), repositoryRoot, evidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	restartedOldVideo := artifactv3video.New(restarted, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, oldDerivatives)
	if _, err := restartedOldVideo.ReadVideoReference(context.Background(), "account", "user", oldConversion.MP4); err != nil {
		t.Fatalf("immutable A failed after restart: %v", err)
	}
	if _, err := restartedOldVideo.ReadVideoReference(context.Background(), "account", "foreign", oldConversion.MP4); err == nil {
		t.Fatal("foreign user read old derivative")
	}
	if err := recoverArtifactV3Repositories(context.Background(), restarted); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil || recovered.Head.CommitOID != repair.Revision.CommitOID {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	restartedProposal, proposalOK, proposalErr := pebblestore.NewSessionStore(reopened).GetVideoEditProposal("account", "artifact-v3-runtime", videoProject.ID, proposal.ID)
	if proposalErr != nil || !proposalOK || restartedProposal.Status != pebblestore.VideoEditProposalStatusPending || restartedProposal.Intent != pebblestore.VideoEditProposalIntentArtifactV3Convert || restartedProposal.Plan == nil || restartedProposal.Plan.Parts[0].ArtifactV3Visual == nil || restartedProposal.Plan.Parts[0].ArtifactV3Visual.CommitOID != artifact.Head.CommitOID {
		t.Fatalf("restart lost native V3 proposal: proposal=%+v ok=%v err=%v", restartedProposal, proposalOK, proposalErr)
	}
	if restartedDerivatives, derivativeErr := newArtifactV3DerivativeStore(filepath.Join(root, "video-derivatives")); derivativeErr != nil {
		t.Fatal(derivativeErr)
	} else {
		restartedVideoService := artifactv3video.New(restarted, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, restartedDerivatives)
		mp4, readErr := restartedVideoService.ReadVideoReference(context.Background(), "account", "user", *restartedProposal.Plan.Parts[0].ArtifactV3Visual)
		if readErr != nil || len(mp4) < 12 || string(mp4[4:8]) != "ftyp" {
			t.Fatalf("restart lost authenticated MP4 derivative: bytes=%q err=%v", mp4, readErr)
		}
	}
	evidence, err := restarted.ReadArtifactV3PreviewEvidence(context.Background(), "account", "user", "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef)
	if err != nil || string(evidence) != "real-renderer-evidence" {
		t.Fatalf("preview evidence=%q err=%v", evidence, err)
	}
	if _, err := restarted.ReadArtifactV3PreviewEvidence(context.Background(), "account", "foreign", "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef); err == nil {
		t.Fatal("foreign owner read Artifact V3 preview evidence")
	}
	if entries, err := os.ReadDir(repositoryRoot); err != nil {
		t.Fatalf("repos=%v err=%v", entries, err)
	} else {
		var repositories int
		for _, entry := range entries {
			if entry.IsDir() && strings.HasSuffix(entry.Name(), ".git") {
				repositories++
			}
		}
		if repositories != 1 {
			t.Fatalf("repos=%v", entries)
		}
	}
}

// Requirement: build and Git finish must share one strict manifest authority.
// The regression threat is a permissive preview accepting shorthand selectors
// or presentation metadata that strict commit validation rejects later.
func TestArtifactV3RuntimeBuildRejectsManifestThatFinishWouldReject(t *testing.T) {
	adapter := newArtifactV3RuntimeAdapter(nil, nil, t.TempDir(), t.TempDir(), pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	project := map[string][]byte{
		"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","title":"Legacy shorthand","parts":[{"id":"hero","selector":"#hero"}]}`),
		"index.html":          []byte(`<!doctype html><html><body><main id="hero">Hero</main></body></html>`),
	}
	build, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "failed" || len(build.Diagnostics) != 1 || build.Diagnostics[0].Code != "manifest_invalid" {
		t.Fatalf("build=%+v", build)
	}
	if !strings.Contains(build.Diagnostics[0].Message, "label") || !strings.Contains(build.Diagnostics[0].Message, "locator") {
		t.Fatalf("diagnostic is not actionable: %+v", build.Diagnostics[0])
	}
}

type artifactV3SafeRendererFailure struct{}

func (artifactV3SafeRendererFailure) Capture(context.Context, htmlcapture.Request) ([]htmlcapture.Result, error) {
	return nil, htmlcapture.NewError("capture_viewport_overflow", "capture document overflows the required viewport")
}

// Requirement: safe renderer diagnostics must reach the authoring turn so the
// AI repairs the requested artifact instead of publishing a diagnostic probe.
func TestArtifactV3RuntimePreviewPreservesSafeRendererDiagnostic(t *testing.T) {
	adapter := newArtifactV3RuntimeAdapter(nil, nil, t.TempDir(), t.TempDir(), pebblestore.ArtifactV3Limits{}, artifactV3SafeRendererFailure{})
	project := map[string][]byte{
		"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[{"id":"hero","label":"Hero","locator":{"kind":"selector","path":"index.html","value":"#hero"}}]}`),
		"index.html":          []byte(`<!doctype html><html><body><main id="hero">Hero</main></body></html>`),
	}
	build, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Project: project})
	if err != nil || build.Status != "succeeded" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	preview, err := adapter.Preview(context.Background(), tool.ArtifactV3PreviewRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Build: build})
	if err != nil || preview.Status != "failed" || len(preview.Diagnostics) != 1 || preview.Diagnostics[0].Code != "capture_viewport_overflow" || !strings.Contains(preview.Diagnostics[0].Message, "overflows") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

// Requirement: source bytes alone are never readiness evidence; a browser
// preview adapter is mandatory before a candidate can be finished.
type artifactV3AnimationTestRenderer struct {
	preflight htmlcapture.AnimationRequest
	render    htmlcapture.AnimationRequest
}

func (r *artifactV3AnimationTestRenderer) PreflightAnimation(_ context.Context, request htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	r.preflight = request
	return htmlcapture.AnimationResult{}, nil
}
func (r *artifactV3AnimationTestRenderer) RenderAnimation(_ context.Context, request htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	r.render = request
	return htmlcapture.AnimationResult{PreviewPNG: []byte("png"), MP4: []byte("mp4"), DurationMS: request.DurationMS, FPS: request.FPS, FrameCount: (request.DurationMS*request.FPS + 999) / 1000}, nil
}

// Requirement: native V3 render wiring injects a deterministic seek adapter only
// into cloned render bytes and delegates both preflight and MP4 rendering.
func TestArtifactV3AnimationRendererLeavesGitProjectBytesUnchanged(t *testing.T) {
	entry := []byte(`<!doctype html><html><head></head><body><div style="animation:fade 1s"></div></body></html>`)
	manifest := []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[]}`)
	project := artifactv3video.Project{Files: map[string][]byte{"swarm-artifact.json": manifest, "index.html": entry}}
	fake := &artifactV3AnimationTestRenderer{}
	renderer := artifactV3AnimationRenderer{renderer: fake}
	request := artifactv3video.RenderRequest{Project: project, DurationMs: 2000, FPS: 30, AnimationAdapter: htmlcapture.AnimationVersion}
	if err := renderer.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	rendered, err := renderer.Render(context.Background(), request)
	if err != nil || string(rendered.FallbackPNG) != "png" || string(rendered.SilentMP4) != "mp4" || rendered.DurationMs != 2000 || rendered.FPS != 30 {
		t.Fatalf("render=%+v err=%v", rendered, err)
	}
	if strings.Contains(string(project.Files["index.html"]), "data-swarm-artifact-v3-animation") {
		t.Fatal("source project bytes were mutated")
	}
	for _, got := range []htmlcapture.AnimationRequest{fake.preflight, fake.render} {
		if got.DurationMS != 2000 || got.FPS != 30 || got.OutputFPS != 30 || got.RequireLivePlayback || !strings.Contains(string(got.Files["index.html"]), "__SWARM_ANIMATION_BIND__") {
			t.Fatalf("trusted request=%+v body=%s", got, got.Files["index.html"])
		}
	}
}

// Requirement: CSS-only native V3 animations rely on the server-owned seek
// adapter and must not be rejected for lacking an author-owned rAF loop.
// Threat: importing the legacy motion_ui live-playback requirement would make
// valid CSS/WAAPI artifacts impossible to convert despite deterministic seek.
func TestArtifactV3AnimationRendererDoesNotRequireAuthorRAF(t *testing.T) {
	entry := []byte(`<!doctype html><html><head><style>@keyframes fade{to{opacity:.2}}#hero{animation:fade 2s infinite}</style></head><body><div id="hero">Motion</div></body></html>`)
	manifest := []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[{"id":"hero","label":"Hero","locator":{"kind":"selector","path":"index.html","value":"#hero"}}]}`)
	fake := &artifactV3AnimationTestRenderer{}
	renderer := artifactV3AnimationRenderer{renderer: fake}
	request := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: map[string][]byte{"swarm-artifact.json": manifest, "index.html": entry}}, DurationMs: 2000, FPS: 30, AnimationAdapter: htmlcapture.AnimationVersion}
	if err := renderer.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if fake.preflight.RequireLivePlayback {
		t.Fatal("server-owned CSS seek adapter must not require author rAF playback")
	}
	injected := string(fake.preflight.Files["index.html"])
	if !strings.Contains(injected, "document.getAnimations") || !strings.Contains(injected, "animation.currentTime=timeMs") || !strings.Contains(injected, "setTimeout(resolve,0)") || !strings.Contains(injected, "document.documentElement.offsetHeight") || strings.Contains(injected, "requestAnimationFrame") || strings.Count(injected, "animation.currentTime=timeMs") != 2 {
		t.Fatal("trusted CSS/WAAPI seek and compositor-settlement adapter was not injected")
	}
}

// Requirement: cancellation cannot publish even one native V3 derivative.
// Threat: staging or final directories could survive a stopped conversion.
func TestArtifactV3DerivativeStoreCancelledPublicationWritesNothing(t *testing.T) {
	store, err := newArtifactV3DerivativeStore(filepath.Join(t.TempDir(), "derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := []byte("png")
	digest := sha256.Sum256(body)
	hexDigest := hex.EncodeToString(digest[:])
	mp4Body := []byte("mp4")
	mp4Digest := sha256.Sum256(mp4Body)
	mp4HexDigest := hex.EncodeToString(mp4Digest[:])
	err = store.PutAtomic(ctx, "session", "artifact", []artifactv3video.Derivative{{ID: "av3der_" + hexDigest, MediaType: "image/png", DigestSHA256: hexDigest, Bytes: body}, {ID: "av3der_" + mp4HexDigest, MediaType: "video/mp4", DigestSHA256: mp4HexDigest, Bytes: mp4Body}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled derivative publication err=%v", err)
	}
	entries, readErr := os.ReadDir(store.root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("cancelled publication left entries=%v err=%v", entries, readErr)
	}
}

// Requirement: two V3 derivatives become visible together or not at all,
// remain private, and digest-verify idempotent publication replays.
func TestArtifactV3DerivativeStorePublishesAtomicPrivateSet(t *testing.T) {
	store, err := newArtifactV3DerivativeStore(filepath.Join(t.TempDir(), "derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	makeDerivative := func(media string, body []byte) artifactv3video.Derivative {
		digest := sha256.Sum256(body)
		hexDigest := hex.EncodeToString(digest[:])
		return artifactv3video.Derivative{ID: "av3der_" + hexDigest, MediaType: media, DigestSHA256: hexDigest, Bytes: body, Reference: pebblestore.ArtifactV3VideoReference{DerivativeID: "av3der_" + hexDigest}}
	}
	png, mp4 := makeDerivative("image/png", []byte("png")), makeDerivative("video/mp4", []byte("mp4"))
	if err := store.PutAtomic(context.Background(), "session", "artifact", []artifactv3video.Derivative{png, mp4}); err != nil {
		t.Fatal(err)
	}
	if body, err := store.Read(context.Background(), "session", "artifact", mp4.Reference); err != nil || string(body) != "mp4" {
		t.Fatalf("read=%q err=%v", body, err)
	}
	if err := store.PutAtomic(context.Background(), "session", "artifact", []artifactv3video.Derivative{png, mp4}); err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(store.root, artifactV3StorageKey("session", "artifact")))
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("sets=%v err=%v", entries, err)
	}
	info, err := os.Stat(filepath.Join(store.root, artifactV3StorageKey("session", "artifact"), entries[0].Name(), png.ID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("derivative mode=%v err=%v", info, err)
	}
}

func TestArtifactV3RuntimeAdapterFailsClosedWithoutBrowser(t *testing.T) {
	root := t.TempDir()
	adapter := newArtifactV3RuntimeAdapter(nil, nil, root, root, pebblestore.ArtifactV3Limits{}, nil)
	project := map[string][]byte{"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[]}`), "index.html": []byte(`<!doctype html><html><body>ok</body></html>`)}
	build, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Project: project})
	if err != nil || build.Status != "succeeded" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	preview, err := adapter.Preview(context.Background(), tool.ArtifactV3PreviewRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Build: build})
	if err != nil || preview.Status != "failed" || len(preview.EvidenceDigests) != 0 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

// Inject precisely after the real store's atomic publication, not before rendering.
type afterPublishDerivativeStore struct {
	artifactv3video.DerivativeStore
	afterPublish func()
}

func (s afterPublishDerivativeStore) PutAtomic(ctx context.Context, sessionID, artifactID string, derivatives []artifactv3video.Derivative) error {
	if err := s.DerivativeStore.PutAtomic(ctx, sessionID, artifactID, derivatives); err != nil {
		return err
	}
	s.afterPublish()
	return nil
}

// Requirement: the trusted animation adapter renders the exact temporal entry
// without mutating Git bytes. Threat: a capture-state hint silently renders the
// root or an undeclared file. Pure request construction is the narrowest layer.
func TestArtifactV3TemporalRendererUsesExactDeclaredEntry(t *testing.T) {
	files := map[string][]byte{
		"swarm-artifact.json":   []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[]}`),
		"index.html":            []byte("<html><head></head><body>Root</body></html>"),
		"closing.html":          []byte("<html><head></head><body>Closing</body></html>"),
		"swarm-storyboard.json": []byte(`{"schema_version":"swarm.artifact-storyboard/v3","sections":[{"id":"closing","title":"Closing","capture_state_id":"closing-state","entrypoint":"closing.html","duration_ms":2000,"production_state":"ready","filming_requirements":["Keep closing"]}]}`),
	}
	before := cloneArtifactProject(files)
	renderer := artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}
	input := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: files}, CaptureStateID: "closing-state", Entrypoint: "closing.html", DurationMs: 2000, FPS: 30, AnimationAdapter: htmlcapture.AnimationVersion}
	request, err := renderer.request(input)
	if err != nil || request.Entry != "closing.html" || !strings.Contains(string(request.Files["closing.html"]), "data-swarm-artifact-v3-animation") || string(request.Files["index.html"]) != string(before["index.html"]) {
		t.Fatalf("wrong render state: %+v %v", request, err)
	}
	for _, mutate := range []func(*artifactv3video.RenderRequest){func(r *artifactv3video.RenderRequest) { r.Entrypoint = "index.html" }, func(r *artifactv3video.RenderRequest) { r.CaptureStateID = "missing" }, func(r *artifactv3video.RenderRequest) { r.PartID = "hero" }} {
		bad := input
		mutate(&bad)
		if _, err := renderer.request(bad); err == nil {
			t.Fatal("undeclared render target accepted")
		}
	}
	if !reflect.DeepEqual(files, before) {
		t.Fatal("render request mutated source")
	}
}
