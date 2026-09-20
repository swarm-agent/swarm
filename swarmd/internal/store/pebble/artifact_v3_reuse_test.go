package pebblestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Requirement: Native Artifact V3 discovery and catalog search must traverse only
// sessions owned by the authenticated account and user, discover selected heads,
// historical ready revisions, and ready unselected swarm/turn candidates, enforce
// exact ready evidence, and provide opaque, deterministic pagination.
func TestArtifactV3DiscoveryAndCatalogSearch(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	root := t.TempDir()
	service, err := NewArtifactV3Service(sessions, root, ArtifactV3Limits{MaxFiles: 256, MaxParts: 256})
	if err != nil {
		t.Fatal(err)
	}

	createV3SessionForStoreTest(t, sessions, "session-a", "user-1", "account-1")
	createV3SessionForStoreTest(t, sessions, "session-b", "user-1", "account-1")
	createV3SessionForStoreTest(t, sessions, "session-other-user", "user-2", "account-1")
	createV3SessionForStoreTest(t, sessions, "session-other-account", "user-1", "account-2")

	ownerA := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-a"}
	ownerB := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-b"}
	ownerOtherUser := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-2", SessionID: "session-other-user"}
	ownerOtherAccount := ArtifactV3Owner{AccountScopeID: "account-2", UserID: "user-1", SessionID: "session-other-account"}

	// Artifact 1 in session-a: Genesis + 1 unselected candidate
	art1, err := service.Create(context.Background(), ArtifactV3CreateInput{
		Owner:         ownerA,
		ArtifactID:    "artifact-alpha",
		TransactionID: "genesis-alpha",
		Project:       artifactV3TestProject(t, "Alpha Starter", "free"),
		Message:       "Genesis Alpha",
		Build:         preparedArtifactV3Evidence("build-alpha"),
		Preview:       preparedArtifactV3Evidence("preview-alpha"),
		NowUnixMs:     1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Open turn and submit an unselected candidate on artifact-alpha (e.g. from swarm iteration)
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{
		Owner:        ownerA,
		ArtifactID:   "artifact-alpha",
		TurnID:       "turn-swarm-1",
		ExpectedHead: art1.Repository.HeadCommitOID,
		NowUnixMs:    1100,
	}); err != nil {
		t.Fatal(err)
	}

	repoA, err := service.open(context.Background(), ownerA, "artifact-alpha")
	if err != nil {
		t.Fatal(err)
	}
	candProject := artifactV3TestProject(t, "Alpha Swarm Variant", "paid")
	candOID, err := repoA.commitProject(context.Background(), candProject, []string{art1.Repository.HeadCommitOID}, "swarm variant")
	if err != nil {
		t.Fatal(err)
	}
	candEvidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: candOID, DigestSHA256: "cand-digest", Reference: "cand-ref"}
	if _, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{
		Owner:         ownerA,
		ArtifactID:    "artifact-alpha",
		TurnID:        "turn-swarm-1",
		CandidateID:   "candidate-swarm-1",
		TransactionID: "tx-cand-1",
		ExpectedHead:  art1.Repository.HeadCommitOID,
		Project:       candProject,
		Message:       "swarm variant",
		Build:         candEvidence,
		Preview:       candEvidence,
		NowUnixMs:     1200,
	}); err != nil {
		t.Fatal(err)
	}

	// Artifact 2 in session-b: Genesis + revision selected (creates historical revision)
	art2, err := service.Create(context.Background(), ArtifactV3CreateInput{
		Owner:         ownerB,
		ArtifactID:    "artifact-beta",
		TransactionID: "genesis-beta",
		Project:       artifactV3TestProject(t, "Beta Starter", "free"),
		Message:       "Genesis Beta",
		Build:         preparedArtifactV3Evidence("build-beta"),
		Preview:       preparedArtifactV3Evidence("preview-beta"),
		NowUnixMs:     2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{
		Owner:        ownerB,
		ArtifactID:   "artifact-beta",
		TurnID:       "turn-beta-1",
		ExpectedHead: art2.Repository.HeadCommitOID,
		NowUnixMs:    2100,
	}); err != nil {
		t.Fatal(err)
	}
	repoB, err := service.open(context.Background(), ownerB, "artifact-beta")
	if err != nil {
		t.Fatal(err)
	}
	beta2Project := artifactV3TestProject(t, "Beta Pro", "paid")
	beta2OID, err := repoB.commitProject(context.Background(), beta2Project, []string{art2.Repository.HeadCommitOID}, "beta pro revision")
	if err != nil {
		t.Fatal(err)
	}
	beta2Evidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: beta2OID, DigestSHA256: "beta2-digest", Reference: "beta2-ref"}
	if _, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{
		Owner:         ownerB,
		ArtifactID:    "artifact-beta",
		TurnID:        "turn-beta-1",
		CandidateID:   "candidate-beta-1",
		TransactionID: "tx-beta-1",
		ExpectedHead:  art2.Repository.HeadCommitOID,
		Project:       beta2Project,
		Message:       "beta pro revision",
		Build:         beta2Evidence,
		Preview:       beta2Evidence,
		NowUnixMs:     2200,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Select(context.Background(), ArtifactV3SelectInput{
		Owner:         ownerB,
		ArtifactID:    "artifact-beta",
		TurnID:        "turn-beta-1",
		CandidateID:   "candidate-beta-1",
		TransactionID: "select-beta-1",
		ExpectedHead:  art2.Repository.HeadCommitOID,
		NowUnixMs:     2300,
	}); err != nil {
		t.Fatal(err)
	}

	// Artifacts in foreign sessions
	serviceOtherUser, _ := NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
	if _, err := serviceOtherUser.Create(context.Background(), ArtifactV3CreateInput{
		Owner:         ownerOtherUser,
		ArtifactID:    "artifact-foreign-user",
		TransactionID: "genesis-fu",
		Project:       artifactV3TestProject(t, "Foreign User", "free"),
		Build:         preparedArtifactV3Evidence("build-fu"),
		Preview:       preparedArtifactV3Evidence("preview-fu"),
		NowUnixMs:     3000,
	}); err != nil {
		t.Fatal(err)
	}

	serviceOtherAccount, _ := NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
	if _, err := serviceOtherAccount.Create(context.Background(), ArtifactV3CreateInput{
		Owner:         ownerOtherAccount,
		ArtifactID:    "artifact-foreign-account",
		TransactionID: "genesis-fa",
		Project:       artifactV3TestProject(t, "Foreign Account", "free"),
		Build:         preparedArtifactV3Evidence("build-fa"),
		Preview:       preparedArtifactV3Evidence("preview-fa"),
		NowUnixMs:     4000,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. SearchCatalog traverses only sessions owned by account-1 and user-1
	catalog, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{})
	if err != nil {
		t.Fatalf("SearchCatalog: %v", err)
	}

	for _, item := range catalog.Items {
		if item.SessionID == "session-other-user" || item.SessionID == "session-other-account" {
			t.Fatalf("catalog leaked foreign session item: %+v", item)
		}
		if item.Reference.SessionID != item.SessionID || item.Reference.ArtifactID != item.ArtifactID {
			t.Fatalf("item reference mismatch: %+v", item)
		}
	}

	// Verify discovery includes:
	// - head of artifact-alpha
	// - ready unselected candidate of artifact-alpha
	// - head of artifact-beta (which is beta2)
	// - historical revision of artifact-beta (genesis commit of beta)
	foundAlphaHead := false
	foundAlphaCand := false
	foundBetaHead := false
	foundBetaHist := false

	for _, item := range catalog.Items {
		if item.ArtifactID == "artifact-alpha" && item.SourceKind == "head" {
			foundAlphaHead = true
		}
		if item.ArtifactID == "artifact-alpha" && item.SourceKind == "candidate" && item.CandidateID == "candidate-swarm-1" {
			foundAlphaCand = true
			if item.Status != "ready" {
				t.Fatalf("expected candidate status ready, got %s", item.Status)
			}
		}
		if item.ArtifactID == "artifact-beta" && item.SourceKind == "head" {
			foundBetaHead = true
			if item.CommitOID != beta2OID {
				t.Fatalf("expected beta head commit to be %s, got %s", beta2OID, item.CommitOID)
			}
		}
		if item.ArtifactID == "artifact-beta" && item.SourceKind == "historical_revision" {
			foundBetaHist = true
			if item.CommitOID != art2.Repository.HeadCommitOID {
				t.Fatalf("expected historical revision commit to be %s, got %s", art2.Repository.HeadCommitOID, item.CommitOID)
			}
		}
	}

	if !foundAlphaHead {
		t.Error("missing artifact-alpha head in catalog")
	}
	if !foundAlphaCand {
		t.Error("missing artifact-alpha unselected candidate in catalog")
	}
	if !foundBetaHead {
		t.Error("missing artifact-beta head in catalog")
	}
	if !foundBetaHist {
		t.Error("missing artifact-beta historical revision in catalog")
	}

	// 2. Filter tests
	candCatalog, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{
		SourceKind: "candidate",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range candCatalog.Items {
		if item.SourceKind != "candidate" {
			t.Fatalf("expected only candidate items, got %s", item.SourceKind)
		}
	}

	sessionBCatalog, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{
		SessionID: "session-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range sessionBCatalog.Items {
		if item.SessionID != "session-b" {
			t.Fatalf("expected only session-b items, got %s", item.SessionID)
		}
	}

	queryCatalog, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{
		Query: "swarm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(queryCatalog.Items) == 0 {
		t.Fatal("query for 'swarm' returned no items")
	}
	for _, item := range queryCatalog.Items {
		if !strings.Contains(item.CandidateID, "swarm") && !strings.Contains(item.TurnID, "swarm") {
			t.Fatalf("query item does not match: %+v", item)
		}
	}

	// 3. Cursor pagination tests
	p1, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 2 || !p1.HasMore || p1.NextCursor == "" {
		t.Fatalf("page 1 unexpected: %+v", p1)
	}

	p2, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{
		Limit:  2,
		Cursor: p1.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) == 0 {
		t.Fatal("page 2 empty")
	}

	// Verify no duplicates between p1 and p2
	seen := make(map[string]bool)
	for _, it := range p1.Items {
		key := it.ArtifactID + "/" + it.CommitOID + "/" + it.CandidateID
		seen[key] = true
	}
	for _, it := range p2.Items {
		key := it.ArtifactID + "/" + it.CommitOID + "/" + it.CandidateID
		if seen[key] {
			t.Fatalf("duplicate item across pages: %s", key)
		}
	}

	// Cursor with changed filter must be rejected
	if _, err := service.SearchCatalog(context.Background(), "account-1", "user-1", ArtifactV3CatalogOptions{
		Limit:  2,
		Cursor: p1.NextCursor,
		Query:  "different-filter",
	}); err == nil {
		t.Fatal("cursor accepted changed filter")
	}
}

// Requirement: ResolveRetainedSource must resolve selected heads, historical ready
// revisions, and unselected candidates from any retained session owned by the authenticated
// account and user, while strictly rejecting foreign accounts, foreign users, unpublished
// artifacts, and revisions without ready evidence.
func TestArtifactV3ResolveRetainedSource(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	root := t.TempDir()
	service, err := NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}

	createV3SessionForStoreTest(t, sessions, "session-source", "user-1", "account-1")
	createV3SessionForStoreTest(t, sessions, "session-other", "user-2", "account-1")

	ownerSource := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-source"}
	created, err := service.Create(context.Background(), ArtifactV3CreateInput{
		Owner:         ownerSource,
		ArtifactID:    "artifact-res",
		TransactionID: "genesis-res",
		Project:       artifactV3TestProject(t, "Starter", "free"),
		Build:         preparedArtifactV3Evidence("build"),
		Preview:       preparedArtifactV3Evidence("preview"),
		NowUnixMs:     1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	headOID := created.Repository.HeadCommitOID

	// 1. Resolve head when commitOID is omitted
	headSource, err := service.ResolveRetainedSource("account-1", "user-1", "session-source", "artifact-res", "", 0)
	if err != nil {
		t.Fatalf("resolve head: %v", err)
	}
	if headSource.CommitOID != headOID || headSource.ArtifactID != "artifact-res" || headSource.SessionID != "session-source" {
		t.Fatalf("unexpected head source: %+v", headSource)
	}

	// 2. Resolve head with explicit commit OID
	exactSource, err := service.ResolveRetainedSource("account-1", "user-1", "session-source", "artifact-res", headOID, 0)
	if err != nil {
		t.Fatalf("resolve exact: %v", err)
	}
	if exactSource.CommitOID != headOID {
		t.Fatalf("commit mismatch: got %s want %s", exactSource.CommitOID, headOID)
	}

	// 3. Foreign account rejection
	if _, err := service.ResolveRetainedSource("account-2", "user-1", "session-source", "artifact-res", "", 0); err == nil {
		t.Fatal("foreign account resolution succeeded")
	}

	// 4. Foreign user rejection
	if _, err := service.ResolveRetainedSource("account-1", "user-2", "session-source", "artifact-res", "", 0); err == nil {
		t.Fatal("foreign user resolution succeeded")
	}

	// 5. Foreign session mismatch rejection
	if _, err := service.ResolveRetainedSource("account-1", "user-1", "session-other", "artifact-res", "", 0); err == nil {
		t.Fatal("wrong session resolution succeeded")
	}

	// 6. Unknown artifact rejection
	if _, err := service.ResolveRetainedSource("account-1", "user-1", "session-source", "non-existent", "", 0); !errors.Is(err, ErrArtifactV3NotFound) {
		t.Fatalf("expected ErrArtifactV3NotFound, got: %v", err)
	}

	// 7. Invalid commit OID format rejection
	if _, err := service.ResolveRetainedSource("account-1", "user-1", "session-source", "artifact-res", "not-a-hash", 0); !errors.Is(err, ErrArtifactV3Invalid) {
		t.Fatalf("expected ErrArtifactV3Invalid, got: %v", err)
	}
}

// Requirement: ArtifactV3Service.Import must import an exact version (head, historical revision,
// or ready unselected candidate) from a retained source session into a distinct destination session
// as a finalized, selected ready starting head with trusted lineage, preserving exact files, manifest,
// parts, and timing, while keeping the source artifact and session 100% UNCHANGED. The destination
// must be immediately editable with subsequent turns.
func TestArtifactV3ImportCreatesFinalizedEditableReadyHead(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	root := t.TempDir()
	service, err := NewArtifactV3Service(sessions, root, ArtifactV3Limits{MaxFiles: 256, MaxParts: 256})
	if err != nil {
		t.Fatal(err)
	}

	createV3SessionForStoreTest(t, sessions, "session-source", "user-1", "account-1")
	createV3SessionForStoreTest(t, sessions, "session-dest", "user-1", "account-1")
	createV3SessionForStoreTest(t, sessions, "session-foreign", "user-2", "account-1")

	ownerSource := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-source"}
	ownerDest := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-dest"}

	// Create source artifact
	sourceProject := artifactV3TestProject(t, "Original Design", "starter")
	createdSource, err := service.Create(context.Background(), ArtifactV3CreateInput{
		Owner:         ownerSource,
		ArtifactID:    "artifact-original",
		TransactionID: "genesis-orig",
		Project:       sourceProject,
		Message:       "Genesis Original",
		Build:         preparedArtifactV3Evidence("build-orig"),
		Preview:       preparedArtifactV3Evidence("preview-orig"),
		NowUnixMs:     1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceHeadOID := createdSource.Repository.HeadCommitOID

	// Create an unselected candidate on source artifact
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{
		Owner:        ownerSource,
		ArtifactID:   "artifact-original",
		TurnID:       "turn-orig-1",
		ExpectedHead: sourceHeadOID,
		NowUnixMs:    1100,
	}); err != nil {
		t.Fatal(err)
	}
	srcRepo, err := service.open(context.Background(), ownerSource, "artifact-original")
	if err != nil {
		t.Fatal(err)
	}
	candProject := artifactV3TestProject(t, "Candidate Design", "pro")
	candOID, err := srcRepo.commitProject(context.Background(), candProject, []string{sourceHeadOID}, "candidate commit")
	if err != nil {
		t.Fatal(err)
	}
	candEvidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: candOID, DigestSHA256: "cand-digest", Reference: "cand-ref"}
	if _, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{
		Owner:         ownerSource,
		ArtifactID:    "artifact-original",
		TurnID:        "turn-orig-1",
		CandidateID:   "candidate-orig-1",
		TransactionID: "tx-cand-1",
		ExpectedHead:  sourceHeadOID,
		Project:       candProject,
		Message:       "candidate commit",
		Build:         candEvidence,
		Preview:       candEvidence,
		NowUnixMs:     1200,
	}); err != nil {
		t.Fatal(err)
	}

	// Capture source repository and candidate state before import
	sourceRepoBefore, _, _ := sessions.GetArtifactV3Repository("account-1", "user-1", "artifact-original")
	sourceCandBefore, _, _ := sessions.GetArtifactV3Candidate("account-1", "user-1", "artifact-original", "turn-orig-1", "candidate-orig-1")
	sourceEventsBefore, _ := sessions.ListSessionEvents("session-source", 0, 100)

	// 1. Positive: Import from source head into session-dest
	imported, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-imported-head",
		TransactionID:         "import-tx-1",
		Message:               "Imported head",
		NowUnixMs:             2000,
	})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify imported artifact structure
	if imported.Repository == nil || imported.Revision == nil || imported.Turn == nil || imported.Candidate == nil {
		t.Fatalf("imported projection incomplete: %+v", imported)
	}
	if imported.Repository.ArtifactID != "artifact-imported-head" || imported.Repository.OwnerSessionID != "session-dest" {
		t.Fatalf("imported repository owner mismatch: %+v", imported.Repository)
	}
	if imported.Turn.Status != "selected" || imported.Candidate.Status != "selected" {
		t.Fatalf("imported turn/candidate not selected: turn=%+v cand=%+v", imported.Turn, imported.Candidate)
	}
	if imported.Repository.Lineage == nil || imported.Repository.Lineage.SourceSessionID != "session-source" || imported.Repository.Lineage.SourceArtifactID != "artifact-original" || imported.Repository.Lineage.SourceCommitOID != sourceHeadOID {
		t.Fatalf("imported lineage missing or incorrect: %+v", imported.Repository.Lineage)
	}

	// Verify destination Git repository contains exact project files
	dstGit, err := service.open(context.Background(), ownerDest, "artifact-imported-head")
	if err != nil {
		t.Fatal(err)
	}
	readBackProject, err := dstGit.ReadProject(context.Background(), imported.Repository.HeadCommitOID)
	if err != nil {
		t.Fatalf("read back imported project: %v", err)
	}
	for path, expectedBytes := range sourceProject.Files {
		gotBytes, ok := readBackProject.Files[path]
		if !ok || string(gotBytes) != string(expectedBytes) {
			t.Fatalf("file %s mismatch or missing in imported project", path)
		}
	}

	// Verify source artifact and session are 100% UNCHANGED
	sourceRepoAfter, _, _ := sessions.GetArtifactV3Repository("account-1", "user-1", "artifact-original")
	sourceCandAfter, _, _ := sessions.GetArtifactV3Candidate("account-1", "user-1", "artifact-original", "turn-orig-1", "candidate-orig-1")
	sourceEventsAfter, _ := sessions.ListSessionEvents("session-source", 0, 100)

	if sourceRepoBefore.HeadCommitOID != sourceRepoAfter.HeadCommitOID || sourceRepoBefore.EventSeq != sourceRepoAfter.EventSeq {
		t.Fatalf("source repository mutated by import: before=%+v after=%+v", sourceRepoBefore, sourceRepoAfter)
	}
	if sourceCandBefore.Status != sourceCandAfter.Status || sourceCandAfter.Status != "ready" {
		t.Fatalf("source candidate mutated by import: before=%+v after=%+v", sourceCandBefore, sourceCandAfter)
	}
	if len(sourceEventsBefore) != len(sourceEventsAfter) {
		t.Fatalf("source session events increased: before=%d after=%d", len(sourceEventsBefore), len(sourceEventsAfter))
	}

	// 2. Verify destination artifact can immediately open turn and be revised
	openedTurn, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{
		Owner:        ownerDest,
		ArtifactID:   "artifact-imported-head",
		TurnID:       "turn-imported-1",
		ExpectedHead: imported.Repository.HeadCommitOID,
		NowUnixMs:    2100,
	})
	if err != nil {
		t.Fatalf("failed to open turn on imported artifact: %v", err)
	}
	if openedTurn.Turn.Status != "open" {
		t.Fatalf("opened turn status = %s", openedTurn.Turn.Status)
	}

	revisedProject := artifactV3TestProject(t, "Revised Imported Design", "pro")
	revisedOID, err := dstGit.commitProject(context.Background(), revisedProject, []string{imported.Repository.HeadCommitOID}, "revised imported")
	if err != nil {
		t.Fatal(err)
	}
	revisedEvidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: revisedOID, DigestSHA256: "rev-digest", Reference: "rev-ref"}
	submittedCand, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{
		Owner:         ownerDest,
		ArtifactID:    "artifact-imported-head",
		TurnID:        "turn-imported-1",
		CandidateID:   "candidate-imported-1",
		TransactionID: "tx-imp-rev-1",
		ExpectedHead:  imported.Repository.HeadCommitOID,
		Project:       revisedProject,
		Message:       "revised imported",
		Build:         revisedEvidence,
		Preview:       revisedEvidence,
		NowUnixMs:     2200,
	})
	if err != nil {
		t.Fatalf("failed to submit candidate on imported artifact: %v", err)
	}
	if submittedCand.Candidate.Status != "ready" || submittedCand.Turn.Status != "awaiting_selection" {
		t.Fatalf("candidate submission on imported artifact unexpected: cand=%+v turn=%+v", submittedCand.Candidate, submittedCand.Turn)
	}

	// 3. Positive: Import from unselected swarm candidate
	createV3SessionForStoreTest(t, sessions, "session-dest-cand", "user-1", "account-1")
	ownerDestCand := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-dest-cand"}
	importedCand, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		SourceCandidateID:     "candidate-orig-1",
		SourceTurnID:          "turn-orig-1",
		DestinationOwner:      ownerDestCand,
		DestinationArtifactID: "artifact-from-cand",
		TransactionID:         "import-cand-tx",
		Message:               "Imported from swarm candidate",
		NowUnixMs:             3000,
	})
	if err != nil {
		t.Fatalf("import from candidate failed: %v", err)
	}
	if importedCand.Repository.HeadCommitOID != candOID {
		t.Fatalf("imported candidate head OID mismatch: got %s want %s", importedCand.Repository.HeadCommitOID, candOID)
	}
	if importedCand.Repository.Lineage.SourceCandidateID != "candidate-orig-1" {
		t.Fatalf("lineage candidate ID mismatch: %+v", importedCand.Repository.Lineage)
	}

	// 4. Idempotency: re-running identical import returns existing projection
	reImported, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-imported-head",
		TransactionID:         "import-tx-1",
		Message:               "Imported head",
		NowUnixMs:             2000,
	})
	if err != nil {
		t.Fatalf("idempotent re-import failed: %v", err)
	}
	if reImported.Repository.HeadCommitOID != imported.Repository.HeadCommitOID {
		t.Fatalf("idempotent head mismatch: %+v vs %+v", reImported, imported)
	}

	// 5. Conflict: different transaction ID on existing destination head
	if _, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-imported-head",
		TransactionID:         "different-tx",
	}); !errors.Is(err, ErrArtifactV3Conflict) {
		t.Fatalf("expected ErrArtifactV3Conflict on existing head, got: %v", err)
	}

	// 6. Negative: Cross-account import rejected
	if _, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceAccountScopeID:  "account-2",
		SourceUserID:          "user-1",
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-cross-account",
		TransactionID:         "tx-cross-acc",
	}); !errors.Is(err, ErrArtifactV3Unauthorized) {
		t.Fatalf("expected ErrArtifactV3Unauthorized for cross-account, got: %v", err)
	}

	// 7. Negative: Cross-user import rejected
	if _, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceAccountScopeID:  "account-1",
		SourceUserID:          "user-2",
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-cross-user",
		TransactionID:         "tx-cross-user",
	}); !errors.Is(err, ErrArtifactV3Unauthorized) {
		t.Fatalf("expected ErrArtifactV3Unauthorized for cross-user, got: %v", err)
	}

	// 8. Negative: Non-existent source artifact rejected
	if _, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceSessionID:       "session-source",
		SourceArtifactID:      "non-existent-artifact",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-missing-src",
		TransactionID:         "tx-missing",
	}); !errors.Is(err, ErrArtifactV3NotFound) {
		t.Fatalf("expected ErrArtifactV3NotFound for missing source, got: %v", err)
	}

	// 9. Negative: Failed candidate rejected
	if _, err := service.RecordCandidateTerminal(ownerSource, "artifact-original", "turn-orig-1", "candidate-failed", "tx-fail", "failed", "err", 1300); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(context.Background(), ArtifactV3ImportInput{
		SourceSessionID:       "session-source",
		SourceArtifactID:      "artifact-original",
		SourceCandidateID:     "candidate-failed",
		SourceTurnID:          "turn-orig-1",
		DestinationOwner:      ownerDest,
		DestinationArtifactID: "artifact-from-fail",
		TransactionID:         "tx-fail-import",
	}); err == nil {
		t.Fatal("importing failed candidate succeeded")
	}

	// Verify no partial destination state created for rejected import
	if _, ok, err := sessions.GetArtifactV3Repository("account-1", "user-1", "artifact-from-fail"); ok || err != nil {
		t.Fatalf("partial state created for failed import: ok=%v err=%v", ok, err)
	}
}

// Requirement: ArtifactV3Repository.ReadProject must read all files from an exact commit,
// validate manifest integrity, and enforce file count, file size, and total tree byte limits.
func TestArtifactV3RepositoryReadProjectLimitsAndIntegrity(t *testing.T) {
	root := t.TempDir()
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-limits"}
	repo, err := OpenArtifactV3Repository(context.Background(), root, "artifact-limits", owner, ArtifactV3Limits{
		MaxFiles:     10,
		MaxFileBytes: 1024,
		MaxTreeBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}

	project := artifactV3TestProject(t, "Limits Test", "free")
	rev, err := repo.Genesis(context.Background(), ArtifactV3GenesisRequest{
		TransactionID: "genesis-limits",
		Project:       project,
		Message:       "genesis limits",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Successful read
	readProject, err := repo.ReadProject(context.Background(), rev.CommitOID)
	if err != nil {
		t.Fatalf("ReadProject: %v", err)
	}
	if len(readProject.Files) != len(project.Files) {
		t.Fatalf("file count mismatch: got %d want %d", len(readProject.Files), len(project.Files))
	}

	// Invalid commit OID
	if _, err := repo.ReadProject(context.Background(), "invalid-oid"); !errors.Is(err, ErrArtifactV3Invalid) {
		t.Fatalf("expected ErrArtifactV3Invalid, got: %v", err)
	}

	// Non-existent commit OID
	fakeOID := strings.Repeat("0", 40)
	if _, err := repo.ReadProject(context.Background(), fakeOID); !errors.Is(err, ErrArtifactV3NotFound) {
		t.Fatalf("expected ErrArtifactV3NotFound, got: %v", err)
	}
}
