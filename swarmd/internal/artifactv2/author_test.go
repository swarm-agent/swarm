package artifactv2

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: managed Designer bytes are durable before validation, invalid
// results remain repairable, and correction preserves untouched exact parts.
// Threat: a validator failure could lose authored bytes, advance a published
// head, or force whole-output re-authoring. This service/store integration test
// is the narrowest proof of exact revision lineage and no publication mutation.
func TestAuthorInvalidRepairPreservesUntouchedPartBytes(t *testing.T) {
	store, sessions, service := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	working, err := service.AllocateWorking(context.Background(), principal, "allocate", "document", "brief", PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	validator := &sequenceValidator{statuses: []string{pebblestore.ArtifactV2ValidationInvalid, pebblestore.ArtifactV2ValidationValid}}
	author := NewAuthorService(service.core, nil, validator)
	grant := AuthorGrant{ID: "grant-1", ArtifactID: working.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{})}
	ctx, err := author.DeclareParts(context.Background(), principal, grant, "declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}, {Key: "footer", Label: "Footer", MediaClass: "text", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	hero, footer := ctx.Parts[0], ctx.Parts[1]
	if _, err := author.WritePart(context.Background(), principal, grant, "hero-1", AuthorPartWrite{PartID: hero.ID, MediaType: "text/plain", Body: []byte("hero-v1")}); err != nil {
		t.Fatal(err)
	}
	ctx, err = author.Inspect(principal, grant)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.CompositionHeadRevision != 0 || ctx.CompositionHead != nil {
		t.Fatalf("partial first part exposed an unexpected composition head: %+v", ctx)
	}
	if _, err := author.WritePart(context.Background(), principal, grant, "footer-1", AuthorPartWrite{PartID: footer.ID, MediaType: "text/plain", ExpectedCompositionHeadRevision: ctx.CompositionHeadRevision, Body: []byte("footer-v1")}); err != nil {
		t.Fatal(err)
	}
	ctx, err = author.Inspect(principal, grant)
	if err != nil || ctx.CompositionHead == nil || ctx.CompositionHeadRevision != ctx.CompositionHead.HeadRevision || ctx.CompositionHeadRevision == 0 {
		t.Fatalf("complete composition did not expose its exact head revision: context=%+v err=%v", ctx, err)
	}
	oldHero, oldFooter, head := ctx.Parts[0].CurrentRevisionID, ctx.Parts[1].CurrentRevisionID, ctx.CompositionHeadRevision
	candidate, err := author.RequestBuild(context.Background(), principal, grant, "build-invalid")
	if err != nil || candidate.State != pebblestore.ArtifactV2StateInvalid || candidate.Diagnostic == nil {
		t.Fatalf("invalid candidate=%+v err=%v", candidate, err)
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, grant, "submit"); err == nil {
		t.Fatal("invalid candidate submitted")
	}
	ctx, _ = author.Inspect(principal, grant)
	if _, err := author.WritePart(context.Background(), principal, grant, "hero-2", AuthorPartWrite{PartID: hero.ID, ExpectedBaseRevisionID: oldHero, ExpectedCompositionHeadRevision: head, MediaType: "text/plain", Body: []byte("hero-v2")}); err != nil {
		t.Fatal(err)
	}
	ctx, _ = author.Inspect(principal, grant)
	if ctx.Parts[1].CurrentRevisionID != oldFooter {
		t.Fatalf("untouched footer changed: before=%s after=%s", oldFooter, ctx.Parts[1].CurrentRevisionID)
	}
	if ctx.Parts[0].CurrentRevisionID == oldHero {
		t.Fatal("hero repair did not create a derived revision")
	}
	candidate, err = author.RequestBuild(context.Background(), principal, grant, "build-valid")
	if err != nil || candidate.State != pebblestore.ArtifactV2StateReady {
		t.Fatalf("valid candidate=%+v err=%v", candidate, err)
	}
	submitted, err := author.SubmitCandidate(context.Background(), principal, grant, "submit")
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok, err := sessions.GetArtifactV2Working("account-1", working.ID)
	if err != nil || !ok || persisted.PublishedHead == nil || submitted.PublishedHeadID != persisted.PublishedHead.PublishedHeadID || persisted.State != pebblestore.ArtifactV2StatePublishedView {
		t.Fatalf("submission did not publish the exact validated head: submitted=%+v persisted=%+v ok=%v err=%v", submitted, persisted, ok, err)
	}
}

// Requirement: destination and policy are capability-owned. Threat: forged or
// stale caller fields could redirect writes or mutate state before rejection.
func TestAuthorRejectsForeignGrantAndStaleHeadWithoutStateChange(t *testing.T) {
	store, sessions, author := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "run-1", ActorClass: "designer"}
	working, err := author.AllocateWorking(context.Background(), principal, "allocate-negative", "document", "brief", PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	grant := AuthorGrant{ID: "grant", ArtifactID: working.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{})}
	ctx, err := author.DeclareParts(context.Background(), principal, grant, "declare-negative", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}})
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := sessions.GetArtifactV2Working("account-1", working.ID)
	foreign := grant
	foreign.OwnerSessionID = "other"
	if _, err := author.WritePart(context.Background(), principal, foreign, "foreign", AuthorPartWrite{PartID: ctx.Parts[0].ID, MediaType: "text/plain", Body: []byte("bad")}); err == nil {
		t.Fatal("foreign grant write succeeded")
	}
	if _, err := author.WritePart(context.Background(), principal, grant, "stale", AuthorPartWrite{PartID: ctx.Parts[0].ID, ExpectedCompositionHeadRevision: 1, MediaType: "text/plain", Body: []byte("bad")}); err == nil {
		t.Fatal("stale head write succeeded")
	}
	after, _, _ := sessions.GetArtifactV2Working("account-1", working.ID)
	if before.Revision != after.Revision || before.CompositionHead != nil || after.CompositionHead != nil {
		t.Fatalf("rejections changed state before=%+v after=%+v", before, after)
	}
}

// Requirement: server-owned submission bookkeeping completes a fully authored
// composition even when a Designer ends before explicitly requesting its build.
// Threat: valid immutable part bytes could remain stranded in authoring state
// because model compliance, rather than the server, controlled build initiation.
func TestSubmitCandidateBuildsCompleteAuthoringCompositionWhenNeeded(t *testing.T) {
	store, sessions, author := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	working, err := author.AllocateWorking(context.Background(), principal, "auto-build-base", "document", "brief", PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	grant := AuthorGrant{ID: "auto-build-grant", ArtifactID: working.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{})}
	ctx, err := author.DeclareParts(context.Background(), principal, grant, "auto-build-declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}, {Key: "footer", Label: "Footer", MediaClass: "text", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{"hero", "footer"} {
		ctx, err = author.WritePart(context.Background(), principal, grant, "auto-build-write-"+value, AuthorPartWrite{PartID: ctx.Parts[index].ID, ExpectedCompositionHeadRevision: ctx.CompositionHeadRevision, MediaType: "text/plain", Body: []byte(value)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if ctx.State != pebblestore.ArtifactV2StateAuthoring || ctx.CompositionHead == nil {
		t.Fatalf("complete authored context=%+v", ctx)
	}
	submitted, err := author.SubmitCandidate(context.Background(), principal, grant, "auto-build-submit")
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok, err := sessions.GetArtifactV2Working("account-1", working.ID)
	if err != nil || !ok || submitted.State != pebblestore.ArtifactV2StatePublishedView || submitted.BuildID == "" || submitted.ValidationID == "" || submitted.PublishedHeadID == "" || persisted.PublishedHead == nil {
		t.Fatalf("submitted=%+v persisted=%+v ok=%v err=%v", submitted, persisted, ok, err)
	}
}

func TestFinalizeIterationCandidateImportsOnlyTargetRevision(t *testing.T) {
	store, sessions, author := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	base, err := author.AllocateWorking(context.Background(), principal, "base", "document", "base", PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	baseGrant := AuthorGrant{ID: "base-grant", ArtifactID: base.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{})}
	ctx, err := author.DeclareParts(context.Background(), principal, baseGrant, "base-declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}, {Key: "footer", Label: "Footer", MediaClass: "text", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range []string{"hero-base", "footer-base"} {
		if _, err := author.WritePart(context.Background(), principal, baseGrant, "base-write-"+value, AuthorPartWrite{PartID: ctx.Parts[i].ID, MediaType: "text/plain", Body: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := author.RequestBuild(context.Background(), principal, baseGrant, "base-build"); err != nil {
		t.Fatal(err)
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, baseGrant, "base-submit"); err != nil {
		t.Fatal(err)
	}
	working, _, _ := sessions.GetArtifactV2Working("account-1", base.ID)
	iteration, err := author.PrepareIteration(context.Background(), principal, "round", base.ID, working.Revision, working.CompositionHead.HeadRevision, []AuthorIterationTarget{{PartID: ctx.Parts[0].ID, Label: "Hero"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := author.AllocateIterationCandidate(context.Background(), principal, "candidate", "candidate", iteration, 1, PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	candidateGrant := baseGrant
	candidateGrant.ID, candidateGrant.ArtifactID, candidateGrant.EditablePartIDs = "candidate-grant", candidate.ID, nil
	candidateCtx, err := author.DeclareParts(context.Background(), principal, candidateGrant, "candidate-declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := author.WritePart(context.Background(), principal, candidateGrant, "candidate-write-hero", AuthorPartWrite{PartID: candidateCtx.Parts[0].ID, MediaType: "text/plain", Body: []byte("hero-candidate")}); err != nil {
		t.Fatal(err)
	}
	if _, err := author.RequestBuild(context.Background(), principal, candidateGrant, "candidate-build"); err != nil {
		t.Fatal(err)
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, candidateGrant, "candidate-submit"); err != nil {
		t.Fatal(err)
	}
	if err := author.FinalizeIterationCandidate(context.Background(), Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", ActorClass: "orchestrator"}, iteration, AuthorIterationCandidate{ArtifactID: candidate.ID, SlotID: "candidate-1"}, "finalize"); err != nil {
		t.Fatal(err)
	}
	studioRound, ok, err := sessions.GetArtifactV2Iteration("account-1", base.ID, iteration.IterationID)
	if err != nil || !ok || studioRound.Status != pebblestore.ArtifactV2IterationAwaitingSelection || len(studioRound.Candidates) != 1 {
		t.Fatalf("iteration=%+v ok=%v err=%v", studioRound, ok, err)
	}
	composition, ok, err := sessions.GetArtifactV2Composition("account-1", base.ID, studioRound.Candidates[0].CompositionID)
	if err != nil || !ok || len(composition.Parts) != 2 || composition.Parts[0].PartRevisionID == iteration.BaseComposition.Parts[0].PartRevisionID || composition.Parts[1] != iteration.BaseComposition.Parts[1] {
		t.Fatalf("candidate composition did not change only the target: %+v ok=%v err=%v", composition, ok, err)
	}
}

// Requirement: a focused candidate may redundantly carry an unchanged preserved
// Part, but the canonical parent composition must still reuse the exact base
// revision rather than importing or rejecting that identical content.
// Threat: model overproduction could otherwise strand a valid candidate or
// silently duplicate preserved bytes into the candidate Composition.
func TestFinalizeIterationCandidateIgnoresIdenticalPreservedPart(t *testing.T) {
	store, sessions, author := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	base, err := author.AllocateWorking(context.Background(), principal, "preserved-base", "document", "base", PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	baseGrant := AuthorGrant{ID: "preserved-base-grant", ArtifactID: base.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{})}
	baseContext, err := author.DeclareParts(context.Background(), principal, baseGrant, "preserved-base-declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}, {Key: "footer", Label: "Footer", MediaClass: "text", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{"hero-base", "footer-base"} {
		baseContext, err = author.WritePart(context.Background(), principal, baseGrant, "preserved-base-write-"+value, AuthorPartWrite{PartID: baseContext.Parts[index].ID, ExpectedCompositionHeadRevision: baseContext.CompositionHeadRevision, MediaType: "text/plain", Body: []byte(value)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, baseGrant, "preserved-base-submit"); err != nil {
		t.Fatal(err)
	}
	working, _, _ := sessions.GetArtifactV2Working("account-1", base.ID)
	iteration, err := author.PrepareIteration(context.Background(), principal, "preserved-round", base.ID, working.Revision, working.CompositionHead.HeadRevision, []AuthorIterationTarget{{PartID: baseContext.Parts[0].ID, Label: "Hero"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := author.AllocateIterationCandidate(context.Background(), principal, "preserved-candidate", "candidate", iteration, 1, PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	grant := baseGrant
	grant.ID, grant.ArtifactID = "preserved-candidate-grant", candidate.ID
	candidateContext, err := author.DeclareParts(context.Background(), principal, grant, "preserved-candidate-declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}, {Key: "footer", Label: "Footer", MediaClass: "text", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{"hero-candidate", "footer-base"} {
		candidateContext, err = author.WritePart(context.Background(), principal, grant, "preserved-candidate-write-"+value, AuthorPartWrite{PartID: candidateContext.Parts[index].ID, ExpectedCompositionHeadRevision: candidateContext.CompositionHeadRevision, MediaType: "text/plain", Body: []byte(value)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, grant, "preserved-candidate-submit"); err != nil {
		t.Fatal(err)
	}
	if err := author.FinalizeIterationCandidate(context.Background(), Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", ActorClass: "orchestrator"}, iteration, AuthorIterationCandidate{ArtifactID: candidate.ID, SlotID: "candidate-1"}, "preserved-finalize"); err != nil {
		t.Fatal(err)
	}
	round, ok, err := sessions.GetArtifactV2Iteration("account-1", base.ID, iteration.IterationID)
	if err != nil || !ok || len(round.Candidates) != 1 {
		t.Fatalf("round=%+v ok=%v err=%v", round, ok, err)
	}
	composition, ok, err := sessions.GetArtifactV2Composition("account-1", base.ID, round.Candidates[0].CompositionID)
	if err != nil || !ok || len(composition.Parts) != 2 || composition.Parts[1] != iteration.BaseComposition.Parts[1] {
		t.Fatalf("identical preserved part was not reused exactly: composition=%+v ok=%v err=%v", composition, ok, err)
	}
}

// Requirement: sibling Designer completions must all attach to one durable
// iteration round even when they finish concurrently.
// Threat: optimistic working/round revisions can make all but the first valid
// candidate fail stale, leaving the source permanently generating.
func TestFinalizeIterationCandidatesRetriesConcurrentRevisionConflicts(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := pebblestore.NewSessionStore(store)
	if err := sessions.CreateSession(pebblestore.SessionSnapshot{ID: "owner", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: t.TempDir(), Mode: "auto", CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	committer := &iterationBarrierCommitter{sessions: sessions, ready: make(chan struct{}), release: make(chan struct{})}
	core := NewService(sessions, sessions, committer, NewGitBlobStore(authorTestOpener{root: t.TempDir()}))
	author := NewAuthorService(core, nil, nil)
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	base, err := author.AllocateWorking(context.Background(), principal, "concurrent-base", "document", "base", PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	baseGrant := AuthorGrant{ID: "base-grant", ArtifactID: base.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{})}
	baseContext, err := author.DeclareParts(context.Background(), principal, baseGrant, "concurrent-base-declare", []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}, {Key: "footer", Label: "Footer", MediaClass: "text", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{"hero-base", "footer-base"} {
		if _, err := author.WritePart(context.Background(), principal, baseGrant, "concurrent-base-write-"+value, AuthorPartWrite{PartID: baseContext.Parts[index].ID, MediaType: "text/plain", Body: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := author.RequestBuild(context.Background(), principal, baseGrant, "concurrent-base-build"); err != nil {
		t.Fatal(err)
	}
	if _, err := author.SubmitCandidate(context.Background(), principal, baseGrant, "concurrent-base-submit"); err != nil {
		t.Fatal(err)
	}
	working, _, _ := sessions.GetArtifactV2Working("account-1", base.ID)
	iteration, err := author.PrepareIteration(context.Background(), principal, "concurrent-round", base.ID, working.Revision, working.CompositionHead.HeadRevision, []AuthorIterationTarget{{PartID: baseContext.Parts[0].ID, Label: "Hero"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	candidates := make([]pebblestore.ArtifactV2WorkingArtifact, 2)
	for index := range candidates {
		candidate, err := author.AllocateIterationCandidate(context.Background(), principal, fmt.Sprintf("candidate-%d", index+1), "candidate", iteration, index+1, PolicySnapshot{})
		if err != nil {
			t.Fatal(err)
		}
		grant := baseGrant
		grant.ID, grant.ArtifactID = fmt.Sprintf("candidate-grant-%d", index+1), candidate.ID
		candidateContext, err := author.DeclareParts(context.Background(), principal, grant, fmt.Sprintf("candidate-declare-%d", index+1), []AuthorPartDeclaration{{Key: "hero", Label: "Hero", MediaClass: "text", Order: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := author.WritePart(context.Background(), principal, grant, fmt.Sprintf("candidate-write-%d", index+1), AuthorPartWrite{PartID: candidateContext.Parts[0].ID, MediaType: "text/plain", Body: []byte(fmt.Sprintf("hero-candidate-%d", index+1))}); err != nil {
			t.Fatal(err)
		}
		if _, err := author.RequestBuild(context.Background(), principal, grant, fmt.Sprintf("candidate-build-%d", index+1)); err != nil {
			t.Fatal(err)
		}
		if _, err := author.SubmitCandidate(context.Background(), principal, grant, fmt.Sprintf("candidate-submit-%d", index+1)); err != nil {
			t.Fatal(err)
		}
		candidates[index] = candidate
	}

	errorsBySlot := make(chan error, len(candidates))
	for index, candidate := range candidates {
		go func(index int, candidate pebblestore.ArtifactV2WorkingArtifact) {
			errorsBySlot <- author.FinalizeIterationCandidate(context.Background(), Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", ActorClass: "orchestrator"}, iteration, AuthorIterationCandidate{ArtifactID: candidate.ID, SlotID: fmt.Sprintf("candidate-%d", index+1)}, fmt.Sprintf("concurrent-finalize-%d", index+1))
		}(index, candidate)
	}
	<-committer.ready
	close(committer.release)
	for range candidates {
		if err := <-errorsBySlot; err != nil {
			t.Fatal(err)
		}
	}
	round, ok, err := sessions.GetArtifactV2Iteration("account-1", base.ID, iteration.IterationID)
	if err != nil || !ok || round.Status != pebblestore.ArtifactV2IterationAwaitingSelection || len(round.Candidates) != 2 {
		t.Fatalf("iteration=%+v ok=%v err=%v", round, ok, err)
	}
}

type iterationBarrierCommitter struct {
	sessions *pebblestore.SessionStore
	mu       sync.Mutex
	waiting  int
	ready    chan struct{}
	release  chan struct{}
}

func (c *iterationBarrierCommitter) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	if input.Kind == pebblestore.V3SessionMutationArtifactV2IterationCandidateAppended {
		c.mu.Lock()
		c.waiting++
		if c.waiting == 2 {
			close(c.ready)
		}
		c.mu.Unlock()
		<-c.release
	}
	return c.sessions.ApplyV3SessionMutation(input)
}

type sequenceValidator struct{ statuses []string }

func (v *sequenceValidator) Validate(_ context.Context, input ValidationInput) (ValidationProduct, error) {
	if len(v.statuses) == 0 {
		return ValidationProduct{}, errors.New("unexpected validation")
	}
	status := v.statuses[0]
	v.statuses = v.statuses[1:]
	product := ValidationProduct{Status: status, ValidatorVersion: DefaultValidator}
	if status == pebblestore.ArtifactV2ValidationInvalid {
		product.Diagnostics = []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("text_overflow", "layout", "error", "repairable", "The hero text exceeds its declared bounds.")}
	}
	if input.Build.Output != nil {
		product.EvidenceDigests = []string{input.Build.Output.DigestSHA256}
	}
	return product, nil
}

type authorTestCommitter struct{ sessions *pebblestore.SessionStore }

func (c authorTestCommitter) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	return c.sessions.ApplyV3SessionMutation(input)
}

type authorTestOpener struct{ root string }

func (o authorTestOpener) Repository(ctx context.Context, id string) (*artifactgit.Repository, error) {
	return artifactgit.Open(ctx, o.root, id, artifactgit.Limits{})
}

func newAuthorTestService(t *testing.T) (*pebblestore.Store, *pebblestore.SessionStore, *AuthorService) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := pebblestore.NewSessionStore(store)
	err = sessions.CreateSession(pebblestore.SessionSnapshot{ID: "owner", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: t.TempDir(), Mode: "auto", CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	core := NewService(sessions, sessions, authorTestCommitter{sessions: sessions}, NewGitBlobStore(authorTestOpener{root: t.TempDir()}))
	return store, sessions, NewAuthorService(core, nil, nil)
}
