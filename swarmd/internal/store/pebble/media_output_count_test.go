package pebblestore

import "testing"

// Purpose: PutMediaUsage's V3 mutation must bill both speech receipts but count
// only the audio output. A real store proves durable totals and retry deduplication.
func TestManageMediaSpeechOutputCount(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	if err := s.CreateSessionForAccount(SessionSnapshot{ID: "session"}, "user", "account"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []SessionMediaUsageRecord{
		{ID: "speech-0", SessionID: "session", AccountScopeID: "account", UserID: "user", TaskID: "speech", Kind: "audio", MediaType: "audio/wav", CostUSD: 0.25},
		{ID: "speech-1", SessionID: "session", AccountScopeID: "account", UserID: "user", TaskID: "speech", Kind: "audio", MediaType: "application/json", CostUSD: 0.125},
	} {
		for range 2 {
			if err := s.PutMediaUsage(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	summary, found, err := s.GetUsageSummary("session")
	if err != nil || !found || summary.SoundCount != 1 || summary.MediaCostUSD != 0.375 || summary.EstimatedCostUSD != 0.375 {
		t.Fatalf("summary %+v found %v error %v", summary, found, err)
	}
}
