package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreListAllTurnUsageAndSummaries(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test-usage.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	store := NewSessionStore(db)
	now := time.Now().UnixMilli()

	// Add turn usage records
	err = store.PutTurnUsage(SessionTurnUsageSnapshot{
		SessionID:       "sess-1",
		AccountScopeID:  "acct-1",
		RunID:           "run-1",
		Provider:        "google",
		Model:           "gemini-3.8-flash",
		InputTokens:     1000,
		OutputTokens:    100,
		CacheReadTokens: 200,
		TotalTokens:     1100,
		CreatedAt:       now - 2000,
		UpdatedAt:       now - 2000,
	})
	if err != nil {
		t.Fatalf("put turn 1: %v", err)
	}

	err = store.PutTurnUsage(SessionTurnUsageSnapshot{
		SessionID:       "sess-2",
		AccountScopeID:  "acct-1",
		RunID:           "run-2",
		Provider:        "openai",
		Model:           "gpt-5.6-sol",
		InputTokens:     2000,
		OutputTokens:    200,
		CacheReadTokens: 500,
		TotalTokens:     2200,
		CreatedAt:       now - 1000,
		UpdatedAt:       now - 1000,
	})
	if err != nil {
		t.Fatalf("put turn 2: %v", err)
	}

	// Add usage summaries
	err = store.PutUsageSummary(SessionUsageSummary{
		SessionID:      "sess-1",
		AccountScopeID: "acct-1",
		Provider:       "google",
		Model:          "gemini-3.8-flash",
		TotalTokens:    1100,
		UpdatedAt:      now - 2000,
	})
	if err != nil {
		t.Fatalf("put summary 1: %v", err)
	}

	// Add media variants
	err = store.PutArtifactVariant(SessionArtifactVariant{
		Version:        1,
		ID:             "art-1",
		CollectionID:   "default",
		SessionID:      "sess-1",
		AccountScopeID: "acct-1",
		Status:         SessionArtifactStatusReady,
		MediaType:      "image/png",
		Filename:       "test.png",
		CreatedAt:      now - 500,
		UpdatedAt:      now - 500,
	})
	if err != nil {
		t.Fatalf("put variant: %v", err)
	}

	// Test ListAllTurnUsage
	turns, err := store.ListAllTurnUsage("acct-1", 100)
	if err != nil {
		t.Fatalf("list all turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].RunID != "run-2" || turns[1].RunID != "run-1" {
		t.Fatalf("turns not sorted by UpdatedAt desc: %+v", turns)
	}

	// Test ListAllUsageSummaries
	summaries, err := store.ListAllUsageSummaries("acct-1", 100)
	if err != nil {
		t.Fatalf("list all summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}

	// Test ListAllMediaArtifactVariants
	media, err := store.ListAllMediaArtifactVariants("acct-1", 100)
	if err != nil {
		t.Fatalf("list all media: %v", err)
	}
	if len(media) != 1 {
		t.Fatalf("expected 1 media item, got %d", len(media))
	}
	if media[0].Filename != "test.png" {
		t.Fatalf("expected test.png, got %s", media[0].Filename)
	}
}
