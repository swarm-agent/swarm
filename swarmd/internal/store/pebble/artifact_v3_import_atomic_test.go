package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Requirement: native imports retain honest source evidence and lineage across
// the Git->Pebble crash window. Threat: synthetic success, source substitution,
// partial events/projections and changed source on transaction retry.
// ArtifactV3Service.Import/Recover, import receipt refs and ApplyV3SessionMutation
// are the authorities; real Git and reopened Pebble prove this durable boundary.
func TestArtifactV3ImportCrashRecoveryAndConflict(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "db")
	root := t.TempDir()
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
	})
	sessions := NewSessionStore(db)
	createV3SessionForStoreTest(t, sessions, "source", "user", "account")
	createV3SessionForStoreTest(t, sessions, "destination", "user", "account")
	service, _ := NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
	source := ArtifactV3Owner{AccountScopeID: "account", UserID: "user", SessionID: "source"}
	dest := source
	dest.SessionID = "destination"
	project := artifactV3TestProject(t, "Exact Import", "source")
	project.Files["assets/data.bin"] = []byte{0, 1, 2, 255}
	// Synthetic trusted scene evidence tests exact copying, not browser rendering.
	manifest := ArtifactV3Manifest{SchemaVersion: ArtifactV3ManifestVersion, Entrypoint: "index.html", AnimationProfile: &SessionArtifactAnimationProfile{ProfileID: "motion_ui"}, SceneContract: &ArtifactV3SceneContract{DurationMS: 100, Scenes: []ArtifactV3TemporalScene{{SceneID: "opening", StartMS: 0, EndMS: 100}}}, Parts: []ArtifactV3Part{{ID: "opening", Label: "Opening", Temporal: &ArtifactV3TemporalScene{SceneID: "opening", StartMS: 0, EndMS: 100}, Locator: ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#opening"}}}}
	project.Files[ArtifactV3ManifestFilename] = mustArtifactV3JSON(t, manifest)
	preview := preparedArtifactV3Evidence("actual-preview")
	preview.Scenes = []ArtifactV3SceneEvidence{{PartID: "opening", SampleMS: 50, DigestSHA256: strings.Repeat("a", 64)}}
	created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: source, ArtifactID: "source-artifact", TransactionID: "source-genesis", Project: project, Build: preparedArtifactV3Evidence("actual-build"), Preview: preview})
	if err != nil {
		t.Fatal(err)
	}
	before, err := sessions.ListV3SessionEvents(source.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	destBefore, _ := sessions.ListV3SessionEvents(dest.SessionID, 0, 100)
	outboxBefore, err := sessions.ListV3RealtimeOutboxForSessionAfterSeq(dest.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	input := ArtifactV3ImportInput{SourceSessionID: source.SessionID, SourceArtifactID: "source-artifact", SourceCommitOID: created.Revision.CommitOID, DestinationOwner: dest, DestinationArtifactID: "imported-artifact", TransactionID: "import-tx"}
	restore := sessions.SetArtifactV3CommitHookForTest(func(string) error { return errors.New("injected durable commit failure") })
	if _, err := service.Import(ctx, input); err == nil {
		t.Fatal("expected injected failure")
	}
	restore()
	if _, ok, err := sessions.GetArtifactV3Repository(dest.AccountScopeID, dest.UserID, input.DestinationArtifactID); err != nil || ok {
		t.Fatalf("partial destination repository %v %v", ok, err)
	}
	after, _ := sessions.ListV3SessionEvents(dest.SessionID, 0, 100)
	if !reflect.DeepEqual(destBefore, after) {
		t.Fatal("partial destination events")
	}
	outboxAfter, err := sessions.ListV3RealtimeOutboxForSessionAfterSeq(dest.SessionID, 0, 100)
	if err != nil || !reflect.DeepEqual(outboxBefore, outboxAfter) {
		t.Fatal("partial destination outbox")
	}
	dstGit, err := service.open(ctx, dest, input.DestinationArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := dstGit.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions = NewSessionStore(db)
	service, _ = NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
	recovered, err := service.Recover(ctx, dest, input.DestinationArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Revision.CommitOID != committed || recovered.Revision.TreeOID != created.Revision.TreeOID || recovered.Repository.Lineage.SourceCommitOID != created.Revision.CommitOID {
		t.Fatal("recovery lost exact source/tree")
	}
	for _, pair := range [][2]ArtifactV3EvidenceProjection{{recovered.Revision.Build, created.Revision.Build}, {recovered.Revision.Preview, created.Revision.Preview}} {
		got, want := pair[0], pair[1]
		if got.Reference != want.Reference || got.DigestSHA256 != want.DigestSHA256 || got.InheritedFrom == nil || got.InheritedFrom.CommitOID != want.CommitOID || got.InheritedFrom.TreeOID != created.Revision.TreeOID || got.InheritedFrom.Owner != source {
			t.Fatalf("fabricated/rebound evidence %+v", got)
		}
	}
	importedProject, err := dstGit.ReadProject(ctx, committed)
	if err != nil || !reflect.DeepEqual(project, importedProject) {
		t.Fatalf("changed bytes %v", err)
	}
	replay, err := service.Import(ctx, input)
	if err != nil || replay.Revision.CommitOID != committed {
		t.Fatalf("replay: %v", err)
	}
	changed := input
	changed.Message = "changed"
	if _, err := service.Import(ctx, changed); !errors.Is(err, ErrArtifactV3TxReuse) {
		t.Fatalf("changed tx accepted: %v", err)
	}
	changed = input
	changed.SourceReference = &SessionArtifactSelectionReference{SessionID: "source", ArtifactID: "source-artifact", RevisionRef: "revision-" + strings.Repeat("a", 40)}
	if _, err := service.Import(ctx, changed); !errors.Is(err, ErrArtifactV3Invalid) {
		t.Fatalf("mixed exact identities accepted: %v", err)
	}
	after, _ = sessions.ListV3SessionEvents(dest.SessionID, 0, 100)
	if len(after) != len(destBefore)+1 {
		t.Fatal("recovery/replay duplicated publication")
	}
	outboxAfter, err = sessions.ListV3RealtimeOutboxForSessionAfterSeq(dest.SessionID, 0, 100)
	if err != nil || len(outboxAfter) != len(outboxBefore)+1 || outboxAfter[len(outboxAfter)-1].Event.EventType != V3SessionMutationArtifactV3Imported {
		t.Fatal("missing or duplicate imported outbox")
	}
	sourceAfter, _ := sessions.ListV3SessionEvents(source.SessionID, 0, 100)
	if !reflect.DeepEqual(before, sourceAfter) {
		t.Fatal("source changed")
	}
	// Corruption fails before destination publication, even for a projected ready source.
	srcGit, err := service.openRetained(ctx, source, "source-artifact")
	if err != nil {
		t.Fatal(err)
	}
	object, err := srcGit.GitObjectPath(created.Revision.ManifestBlobOID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(object, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	changed = input
	changed.DestinationArtifactID = "corrupt-import"
	changed.TransactionID = "corrupt-tx"
	if _, err := service.Import(ctx, changed); err == nil {
		t.Fatal("corrupt source accepted")
	}
	if _, ok, err := sessions.GetArtifactV3Repository(dest.AccountScopeID, dest.UserID, changed.DestinationArtifactID); err != nil || ok {
		t.Fatal("partial corrupt destination")
	}
}

// Requirement: discovery has a real scan budget, opaque principal/filter-bound
// continuation and no hidden truncation. Threat: unbounded tenant-wide scans or
// leaking foreign records. SearchArtifactV3Catalog is tested at actual Pebble keys
// with synthetic projection-only catalog fixtures; this does not prove Git reads.
func TestArtifactV3CatalogScanBudgetAndPrincipalCursor(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForStoreTest(t, sessions, "source", "user", "account")
	createV3SessionForStoreTest(t, sessions, "foreign", "other", "account")
	for i := 0; i < artifactV3CatalogScanBudget+5; i++ {
		id := fmt.Sprintf("artifact-%04d", i)
		owner := "foreign"
		user := "other"
		if i >= artifactV3CatalogScanBudget {
			owner = "source"
			user = "user"
		}
		oid := fmt.Sprintf("%040x", i+1)
		repo := ArtifactV3RepositoryProjection{AccountScopeID: "account", UserID: user, OwnerSessionID: owner, ArtifactID: id, RepositoryID: id, HeadCommitOID: oid}
		rev := ArtifactV3RevisionProjection{ArtifactID: id, RepositoryID: id, OwnerSessionID: owner, CommitOID: oid, Build: ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: oid, DigestSHA256: "digest", Reference: "build"}, Preview: ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: oid, DigestSHA256: "digest", Reference: "preview"}}
		for key, value := range map[string]any{KeyArtifactV3Repository("account", id): repo, KeyArtifactV3Revision("account", id, oid): rev} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutBytes(key, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, err := sessions.SearchArtifactV3Catalog("account", "user", ArtifactV3CatalogOptions{SourceKind: "head", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 0 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("scan budget did not stop %+v", first)
	}
	if _, err := sessions.SearchArtifactV3Catalog("account", "other", ArtifactV3CatalogOptions{SourceKind: "head", Cursor: first.NextCursor}); err == nil {
		t.Fatal("cross-user cursor accepted")
	}
	seen := map[string]bool{}
	cursor := first.NextCursor
	for n := 0; n < 10; n++ {
		page, err := sessions.SearchArtifactV3Catalog("account", "user", ArtifactV3CatalogOptions{SourceKind: "head", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.SessionID != "source" || seen[item.ArtifactID] {
				t.Fatal("leaked/duplicate item")
			}
			seen[item.ArtifactID] = true
		}
		if !page.HasMore {
			cursor = ""
			break
		}
		cursor = page.NextCursor
	}
	if cursor != "" || len(seen) != 5 {
		t.Fatalf("truncated catalog: %d", len(seen))
	}
	if _, err := sessions.SearchArtifactV3Catalog("account", "user", ArtifactV3CatalogOptions{Cursor: strings.Repeat("x", 8193)}); err == nil {
		t.Fatal("oversize cursor accepted")
	}
}

// Requirement: exact project extraction rejects over-limit trees rather than
// silently copying the first page. ReadRevision/ReadProject own these bounds.
func TestArtifactV3ReadProjectRejectsTruncatedTree(t *testing.T) {
	ctx := context.Background()
	owner := ArtifactV3Owner{AccountScopeID: "account", UserID: "user", SessionID: "source"}
	root := t.TempDir()
	repo, err := OpenArtifactV3Repository(ctx, root, "quota", owner, ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	project := artifactV3TestProject(t, "quota", "source")
	revision, err := repo.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: "genesis", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	repo.limits.MaxFileBytes = 1
	if _, err := repo.ReadFile(ctx, revision.CommitOID, "index.html"); !errors.Is(err, ErrArtifactV3Quota) {
		t.Fatalf("unbounded blob read: %v", err)
	}
	repo.limits = (ArtifactV3Limits{}).normalized()
	repo.limits.MaxFiles = 1
	if _, err := repo.ReadProject(ctx, revision.CommitOID); !errors.Is(err, ErrArtifactV3Quota) {
		t.Fatalf("truncated project accepted: %v", err)
	}
}

// Requirement: source-only retained lookup includes archives without relaxing
// destination ownership; missing, deleted, failed, and forged versions have no
// visible destination effects. Real catalog/Import and Git/Pebble fixtures own it.
func TestArtifactV3ImportRetainedRejections(t *testing.T) {
	for _, kind := range []string{"archived", "deleted", "failed", "missing-repository", "foreign-destination", "forged-revision", "symlink-repository"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db := openV3SessionEventTestStore(t)
			sessions := NewSessionStore(db)
			createV3SessionForStoreTest(t, sessions, "source", "user", "account")
			createV3SessionForStoreTest(t, sessions, "destination", "user", "account")
			createV3SessionForStoreTest(t, sessions, "foreign", "other", "account")
			root := t.TempDir()
			service, _ := NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
			source := ArtifactV3Owner{AccountScopeID: "account", UserID: "user", SessionID: "source"}
			dest := source
			dest.SessionID = "destination"
			created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: source, ArtifactID: "original", TransactionID: "genesis", Project: artifactV3TestProject(t, "exact", "source"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
			if err != nil {
				t.Fatal(err)
			}
			input := ArtifactV3ImportInput{SourceSessionID: source.SessionID, SourceArtifactID: "original", SourceCommitOID: created.Revision.CommitOID, DestinationOwner: dest, DestinationArtifactID: "imported", TransactionID: "import"}
			path := filepath.Join(root, "original.git")
			switch kind {
			case "archived":
				if err := sessions.ArchiveSession(source.SessionID); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := sessions.DeleteSession(source.SessionID); err != nil {
					t.Fatal(err)
				}
			case "failed":
				rev := *created.Revision
				rev.Preview.Status = "failed"
				raw, err := json.Marshal(rev)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.PutBytes(KeyArtifactV3Revision(source.AccountScopeID, "original", rev.CommitOID), raw); err != nil {
					t.Fatal(err)
				}
			case "foreign-destination":
				input.DestinationOwner.SessionID = "foreign"
			case "forged-revision":
				input.SourceCommitOID = strings.Repeat("a", 40)
			case "missing-repository", "symlink-repository":
				if err := os.Rename(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink-repository" {
					if err := os.Symlink(path+"-retained", path); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := sessions.ListV3SessionEvents(input.DestinationOwner.SessionID, 0, 100)
			got, err := service.Import(ctx, input)
			if kind == "archived" {
				if err != nil || got.Revision.TreeOID != created.Revision.TreeOID {
					t.Fatalf("archive import: %v", err)
				}
				page, err := service.SearchCatalog(ctx, "account", "user", ArtifactV3CatalogOptions{SessionID: "source", SourceKind: "head"})
				if err != nil || len(page.Items) != 1 {
					t.Fatalf("archive discovery %+v %v", page, err)
				}
				if _, live, err := sessions.GetSession("source"); err != nil || live {
					t.Fatal("source reactivated")
				}
				return
			}
			if err == nil {
				t.Fatal("invalid source/destination accepted")
			}
			after, _ := sessions.ListV3SessionEvents(input.DestinationOwner.SessionID, 0, 100)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial events")
			}
			if _, ok, err := sessions.GetArtifactV3Repository("account", "user", "imported"); err != nil || ok {
				t.Fatal("partial repository")
			}
			if kind == "missing-repository" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("recreated source")
				}
			}
		})
	}
}

// Requirement: competing imports publish once and exact retry recovers any Git
// CAS loser without overwriting the winner. No provider or rendering is involved.
func TestArtifactV3ImportConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForStoreTest(t, sessions, "source", "user", "account")
	createV3SessionForStoreTest(t, sessions, "destination", "user", "account")
	service, _ := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	source := ArtifactV3Owner{AccountScopeID: "account", UserID: "user", SessionID: "source"}
	dest := source
	dest.SessionID = "destination"
	created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: source, ArtifactID: "original", TransactionID: "genesis", Project: artifactV3TestProject(t, "exact", "source"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	input := ArtifactV3ImportInput{SourceSessionID: "source", SourceArtifactID: "original", SourceCommitOID: created.Revision.CommitOID, DestinationOwner: dest, DestinationArtifactID: "imported", TransactionID: "import"}
	before, _ := sessions.ListV3SessionEvents(dest.SessionID, 0, 100)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, err := service.Import(ctx, input); results <- err }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		<-results
	}
	got, err := service.Import(ctx, input)
	if err != nil || got.Revision.TreeOID != created.Revision.TreeOID {
		t.Fatalf("retry: %v", err)
	}
	after, _ := sessions.ListV3SessionEvents(dest.SessionID, 0, 100)
	if len(after) != len(before)+1 {
		t.Fatal("duplicate publication")
	}
}
