package artifactv2

import (
	"context"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: one deterministic Artifact V2 journey keeps durable working
// visibility, multipart bytes, invalid repair, targeted candidates, selection,
// animation preview/fallback evidence, and publication on one authority.
// Threat: any phase could fall back to V1, lose bytes on invalidation, mutate a
// preserved part, or advance a head from stale evidence. This end-to-end service
// test is the narrowest hermetic proof across those named product transitions.
func TestArtifactV2DeterministicGoldenJourney(t *testing.T) {
	store, sessions, baseAuthor := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	policy := normalizedPolicy(PolicySnapshot{AnimationProfile: "motion_ui"})
	working, err := baseAuthor.AllocateWorking(context.Background(), principal, "golden-allocate", "animation", "golden", policy)
	if err != nil {
		t.Fatal(err)
	}
	if working.State != pebblestore.ArtifactV2StateAllocated {
		t.Fatalf("working=%+v", working)
	}

	renderer := &fakeMotionRenderer{result: MotionRenderResult{PreviewPNG: []byte("preview"), Frames: []MotionFrame{{Slot: "opening", PNG: []byte("opening")}, {Slot: "middle", PNG: []byte("middle")}, {Slot: "exit", PNG: []byte("exit")}}}}
	validator := &sequenceMotionValidator{statuses: []string{pebblestore.ArtifactV2ValidationInvalid, pebblestore.ArtifactV2ValidationValid, pebblestore.ArtifactV2ValidationValid}}
	author := NewAuthorService(baseAuthor.core, MotionCompiler{}, validator)
	grant := AuthorGrant{ID: "golden-grant", ArtifactID: working.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: policy}
	ctx, err := author.DeclareParts(context.Background(), principal, grant, "golden-declare", []AuthorPartDeclaration{{Key: "scene", Label: "Scene", MediaClass: "motion", Order: 1}, {Key: "behavior", Label: "Behavior", MediaClass: "motion", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	sceneID, behaviorID := ctx.Parts[0].ID, ctx.Parts[1].ID
	compile := motionCompileTestInput(t)
	if _, err := author.WritePart(context.Background(), principal, grant, "golden-scene", AuthorPartWrite{PartID: sceneID, MediaType: MotionSceneMediaType, Body: compile.Parts[0].Body}); err != nil {
		t.Fatal(err)
	}
	if _, err := author.WritePart(context.Background(), principal, grant, "golden-behavior", AuthorPartWrite{PartID: behaviorID, MediaType: MotionBehaviorMediaType, Body: compile.Parts[1].Body}); err != nil {
		t.Fatal(err)
	}
	ctx, _ = author.Inspect(principal, grant)
	baseScene, baseBehavior, baseHead := ctx.Parts[0].CurrentRevisionID, ctx.Parts[1].CurrentRevisionID, ctx.CompositionHead.HeadRevision
	candidate, err := author.RequestBuild(context.Background(), principal, grant, "golden-invalid")
	if err != nil || candidate.State != pebblestore.ArtifactV2StateInvalid {
		t.Fatalf("invalid=%+v err=%v", candidate, err)
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, grant, "golden-submit"); err == nil {
		t.Fatal("invalid candidate submitted")
	}

	if _, err := author.WritePart(context.Background(), principal, grant, "golden-repair", AuthorPartWrite{PartID: sceneID, ExpectedBaseRevisionID: baseScene, ExpectedCompositionHeadRevision: baseHead, MediaType: MotionSceneMediaType, Body: compile.Parts[0].Body}); err != nil {
		t.Fatal(err)
	}
	ctx, _ = author.Inspect(principal, grant)
	if ctx.Parts[1].CurrentRevisionID != baseBehavior {
		t.Fatal("repair changed untouched behavior")
	}
	candidate, err = author.RequestBuild(context.Background(), principal, grant, "golden-valid")
	if err != nil || candidate.State != pebblestore.ArtifactV2StateReady {
		t.Fatalf("ready=%+v err=%v", candidate, err)
	}

	current, _, _ := sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	round, err := baseAuthor.core.OpenIteration(context.Background(), principal, OpenIterationInput{RequestID: "golden-round", ArtifactID: working.ID, ExpectedWorkingRevision: current.Revision, RequestedCandidates: 1, TargetPartIDs: []string{sceneID}})
	if err != nil {
		t.Fatal(err)
	}
	base, ok, err := sessions.GetArtifactV2Composition(principal.AccountScopeID, working.ID, round.BaseCompositionID)
	if err != nil || !ok {
		t.Fatal("missing iteration base")
	}
	revisions, _ := sessions.ListArtifactV2PartRevisions(principal.AccountScopeID, working.ID, sceneID, 256)
	latestScene := revisions[len(revisions)-1]
	derived, err := baseAuthor.core.AppendPartRevision(context.Background(), principal, AppendPartRevisionInput{RequestID: "golden-iteration-part", ArtifactID: working.ID, PartID: sceneID, ExpectedWorkingRevision: current.Revision + 1, ExpectedBaseRevisionID: latestScene.ID, MediaType: MotionSceneMediaType, Body: compile.Parts[0].Body})
	if err != nil {
		t.Fatal(err)
	}
	current, _, _ = sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	parts := append([]pebblestore.ArtifactV2CompositionPart(nil), base.Parts...)
	for index := range parts {
		if parts[index].PartID == sceneID {
			parts[index].PartRevisionID, parts[index].DigestSHA256 = derived.ID, derived.Blob.DigestSHA256
		}
	}
	round, err = baseAuthor.core.AppendIterationCandidate(context.Background(), principal, AppendIterationCandidateInput{RequestID: "golden-candidate", ArtifactID: working.ID, IterationID: round.ID, SlotID: "candidate-1", ExpectedWorkingRevision: current.Revision, ExpectedIterationRevision: round.Revision, Composition: pebblestore.ArtifactV2Composition{ConstructionVersion: base.ConstructionVersion, Parts: parts}})
	if err != nil {
		t.Fatal(err)
	}
	current, _, _ = sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	selected, err := baseAuthor.core.SelectIterationCandidate(context.Background(), principal, SelectIterationCandidateInput{RequestID: "golden-select", ArtifactID: working.ID, IterationID: round.ID, SlotID: "candidate-1", ExpectedWorkingRevision: current.Revision, ExpectedIterationRevision: round.Revision})
	if err != nil || selected.ID == base.ID {
		t.Fatalf("selection=%+v err=%v", selected, err)
	}

	current, _, _ = sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	candidate, err = author.RequestBuild(context.Background(), principal, grant, "golden-selected-build")
	if err != nil || candidate.State != pebblestore.ArtifactV2StateReady {
		t.Fatalf("selected build=%+v err=%v", candidate, err)
	}
	current, _, _ = sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	preview, err := baseAuthor.core.CreateDerivative(context.Background(), principal, CreateDerivativeInput{RequestID: "golden-preview", ArtifactID: working.ID, ExpectedWorkingRevision: current.Revision, Kind: "preview", Renderer: renderer})
	if err != nil || preview.Status != "ready" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	current, _, _ = sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	fallback, err := baseAuthor.core.CreateDerivative(context.Background(), principal, CreateDerivativeInput{RequestID: "golden-fallback", ArtifactID: working.ID, ExpectedWorkingRevision: current.Revision, Kind: "fallback", Renderer: renderer})
	if err != nil || fallback.Status != "ready" {
		t.Fatalf("fallback=%+v err=%v", fallback, err)
	}
	current, _, _ = sessions.GetArtifactV2Working(principal.AccountScopeID, working.ID)
	published, err := baseAuthor.core.Publish(context.Background(), principal, PublishInput{RequestID: "golden-publish", ArtifactID: working.ID, ExpectedWorkingRevision: current.Revision, AuthorizingActor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := baseAuthor.core.ResolveReady(principal, working.ID, published.ID)
	if err != nil || ready.PublishedHeadID != published.ID {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}

	// Reconnect/restart proof: a fresh service over the same durable stores reads
	// the exact selected and published heads without transcript or V1 projection.
	restarted := NewService(sessions, sessions, authorTestCommitter{sessions: sessions}, baseAuthor.core.blobs)
	recovered, err := restarted.ResolveReady(principal, working.ID, published.ID)
	if err != nil || recovered != ready {
		t.Fatalf("recovered=%+v want=%+v err=%v", recovered, ready, err)
	}
}

type sequenceMotionValidator struct{ statuses []string }

func (v *sequenceMotionValidator) Validate(_ context.Context, input ValidationInput) (ValidationProduct, error) {
	status := pebblestore.ArtifactV2ValidationValid
	if len(v.statuses) != 0 {
		status, v.statuses = v.statuses[0], v.statuses[1:]
	}
	product := ValidationProduct{Status: status, ValidatorVersion: MotionValidatorVersion, RendererSnapshot: "deterministic", EvidenceDigests: []string{input.Build.Output.DigestSHA256}}
	if status == pebblestore.ArtifactV2ValidationInvalid {
		product.Diagnostics = []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("golden_invalid", "layout", "error", "repairable", "Repair the scene.")}
	}
	return product, nil
}
