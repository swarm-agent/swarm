package pebblestore

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchSessionArtifactCatalogPaginatesOwnedReadyReferencesWithoutGaps(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	for _, session := range []SessionSnapshot{
		{ID: "owned-a", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: t.TempDir(), CreatedAt: 1, UpdatedAt: 1},
		{ID: "owned-b", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: t.TempDir(), CreatedAt: 2, UpdatedAt: 2},
		{ID: "other-user", UserID: "user-2", AccountScopeID: "account-1", WorkspacePath: t.TempDir(), CreatedAt: 3, UpdatedAt: 3},
		{ID: "other-account", UserID: "user-1", AccountScopeID: "account-2", WorkspacePath: t.TempDir(), CreatedAt: 4, UpdatedAt: 4},
	} {
		if err := sessions.CreateSession(session); err != nil {
			t.Fatal(err)
		}
	}
	putReady := func(account, session, collection, variant string, created int64, name, description, filename, mediaType, label string) {
		t.Helper()
		collectionRow := SessionArtifactCollection{Version: SessionArtifactVersion, ID: collection, AccountScopeID: account, SessionID: session, Status: SessionArtifactStatusReady, Name: name, Description: description, VariantCount: 1, ReadyCount: 1, CreatedAt: created, UpdatedAt: created, EventSeq: uint64(created)}
		variantRow := SessionArtifactVariant{Version: SessionArtifactVersion, ID: variant, CollectionID: collection, AccountScopeID: account, SessionID: session, Status: SessionArtifactStatusReady, Filename: filename, MediaType: mediaType, Presentation: SessionArtifactPresentation{Label: label}, GraphState: SessionArtifactGraphProjection, RepositoryID: "catalog-repository", CommitOID: strings.Repeat("a", 64), CreatedAt: created, UpdatedAt: created, EventSeq: uint64(created)}
		if err := store.PutJSON(KeySessionArtifactCollection(account, session, collection), collectionRow); err != nil {
			t.Fatal(err)
		}
		if err := store.PutJSON(KeySessionArtifactVariant(account, session, collection, variant), variantRow); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= 125; index++ {
		session := "owned-a"
		if index%2 == 0 {
			session = "owned-b"
		}
		putReady("account-1", session, fmt.Sprintf("collection-%03d", index), fmt.Sprintf("variant-%03d", index), int64(index), "Campaign", "Summer editorial library", fmt.Sprintf("campaign-%03d.png", index), "image/png", "Editorial card")
	}
	putReady("account-1", "other-user", "forbidden-user", "forbidden-user", 500, "Campaign", "Summer editorial library", "forbidden.png", "image/png", "Editorial card")
	putReady("account-2", "other-account", "forbidden-account", "forbidden-account", 501, "Campaign", "Summer editorial library", "forbidden.png", "image/png", "Editorial card")

	seen := make(map[string]bool)
	cursor := ""
	for {
		page, err := sessions.SearchSessionArtifactCatalog("account-1", "user-1", SessionArtifactCatalogOptions{Query: "editorial", Status: SessionArtifactStatusReady, MediaType: "image/png", Limit: 37, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			key := item.Variant.SessionID + "/" + item.Variant.CollectionID + "/" + item.Variant.ID
			if seen[key] {
				t.Fatalf("duplicate catalog item %s", key)
			}
			seen[key] = true
			if item.Reference == nil || item.Reference.SessionID != item.Variant.SessionID || item.Reference.CollectionID != item.Variant.CollectionID || item.Reference.VariantID != item.Variant.ID || item.Reference.EventSeq != item.Variant.EventSeq {
				t.Fatalf("incomplete ready reference: %+v", item)
			}
			if item.Variant.SessionID == "other-user" || item.Variant.SessionID == "other-account" {
				t.Fatalf("catalog leaked unowned session: %+v", item)
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("has_more page omitted cursor")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 125 {
		t.Fatalf("catalog count = %d, want 125", len(seen))
	}
}

func TestSearchSessionArtifactCatalogOmitsRowsWithoutExactGitProjection(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	if err := sessions.CreateSession(SessionSnapshot{ID: "owned-invalid", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: t.TempDir()}); err != nil { t.Fatal(err) }
	collection := SessionArtifactCollection{Version: SessionArtifactVersion, ID: "collection", AccountScopeID: "account-1", SessionID: "owned-invalid", Status: SessionArtifactStatusReady, Name: "Invalid", VariantCount: 1, ReadyCount: 1}
	variant := SessionArtifactVariant{Version: SessionArtifactVersion, ID: "variant", CollectionID: collection.ID, AccountScopeID: collection.AccountScopeID, SessionID: collection.SessionID, Status: SessionArtifactStatusReady, Filename: "invalid.txt", MediaType: "text/plain", EventSeq: 1}
	if err := store.PutJSON(KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID), collection); err != nil { t.Fatal(err) }
	if err := store.PutJSON(KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID), variant); err != nil { t.Fatal(err) }
	page, err := sessions.SearchSessionArtifactCatalog("account-1", "user-1", SessionArtifactCatalogOptions{Limit: 10})
	if err != nil || len(page.Items) != 0 { t.Fatalf("catalog page=%+v err=%v", page, err) }
	stored, ok, err := sessions.GetSessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID)
	if err != nil || !ok || stored.Status != SessionArtifactStatusReady { t.Fatalf("preserved invalid row=%+v ok=%t err=%v", stored, ok, err) }
}

func TestSearchSessionArtifactCatalogSnapshotAndCursorFilterContract(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	if err := sessions.CreateSession(SessionSnapshot{ID: "owned", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: t.TempDir(), CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	put := func(id string, created int64, name string) {
		collection := SessionArtifactCollection{Version: SessionArtifactVersion, ID: "collection-" + id, AccountScopeID: "account-1", SessionID: "owned", Status: SessionArtifactStatusReady, Name: name, VariantCount: 1, ReadyCount: 1, CreatedAt: created, UpdatedAt: created, EventSeq: uint64(created)}
		variant := SessionArtifactVariant{Version: SessionArtifactVersion, ID: "variant-" + id, CollectionID: collection.ID, AccountScopeID: "account-1", SessionID: "owned", Status: SessionArtifactStatusReady, Filename: id + ".txt", MediaType: "text/plain", GraphState: SessionArtifactGraphProjection, RepositoryID: "catalog-repository", CommitOID: strings.Repeat("b", 64), CreatedAt: created, UpdatedAt: created, EventSeq: uint64(created)}
		if err := store.PutJSON(KeySessionArtifactCollection("account-1", "owned", collection.ID), collection); err != nil {
			t.Fatal(err)
		}
		if err := store.PutJSON(KeySessionArtifactVariant("account-1", "owned", collection.ID, variant.ID), variant); err != nil {
			t.Fatal(err)
		}
	}
	put("one", 100, "Shared name")
	put("two", 90, "Shared name")
	first, err := sessions.SearchSessionArtifactCatalog("account-1", "user-1", SessionArtifactCatalogOptions{Query: "shared name", Limit: 1})
	if err != nil || !first.HasMore || len(first.Items) != 1 {
		t.Fatalf("first page = %+v err=%v", first, err)
	}
	put("new", 110, "Shared name")
	second, err := sessions.SearchSessionArtifactCatalog("account-1", "user-1", SessionArtifactCatalogOptions{Query: "shared name", Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].Variant.ID != "variant-two" {
		t.Fatalf("stable continuation = %+v err=%v", second, err)
	}
	if _, err := sessions.SearchSessionArtifactCatalog("account-1", "user-1", SessionArtifactCatalogOptions{Query: "different", Limit: 1, Cursor: first.NextCursor}); err == nil {
		t.Fatal("cursor accepted changed filters")
	}
	ambiguous, err := sessions.SearchSessionArtifactCatalog("account-1", "user-1", SessionArtifactCatalogOptions{Query: "shared name", Limit: 10})
	if err != nil || len(ambiguous.Items) != 3 {
		t.Fatalf("ambiguous name candidates = %+v err=%v", ambiguous, err)
	}
}
