package pebblestore

import "testing"

func TestSessionAccess(t *testing.T) {
	if _, err := NormalizeSessionAccess(SessionAccess{Tasks: "always"}); err == nil {
		t.Fatal("accepted an unknown tasks choice")
	}
	if _, err := NormalizeSessionAccess(SessionAccess{Machine: "root"}); err == nil {
		t.Fatal("accepted an unknown machine choice")
	}
	change, err := NormalizeSessionAccess(SessionAccess{Machine: " Full "})
	if err != nil || change.Machine != SessionMachineFull {
		t.Fatalf("normalize = %+v, %v", change, err)
	}
	merged := MergeSessionAccess(SessionAccess{Tasks: SessionTasksFree, Machine: SessionMachineNone}, change)
	if merged != (SessionAccess{Tasks: SessionTasksFree, Machine: SessionMachineFull}) {
		t.Fatalf("merge = %+v", merged)
	}
	metadata := map[string]any{SessionAccessMetadataKey: SessionAccessMetadata(merged)}
	if got := SessionAccessFromMetadata(metadata); got != merged {
		t.Fatalf("round trip = %+v", got)
	}
	if got := SessionAccessFromMetadata(map[string]any{SessionAccessMetadataKey: map[string]any{"tasks": "bogus"}}); got != (SessionAccess{}) {
		t.Fatalf("malformed access read as %+v", got)
	}
	if !SessionTasksStartFreely(SessionSnapshot{Metadata: metadata}) || SessionTasksStartFreely(SessionSnapshot{}) {
		t.Fatal("SessionTasksStartFreely")
	}
}

func TestProjectConversationListKeepsAccess(t *testing.T) {
	session := SessionSnapshot{ID: "s", Metadata: map[string]any{"project_id": "p", SessionAccessMetadataKey: map[string]any{"tasks": "free"}}}
	if got := SessionAccessFromMetadata(compactProjectConversation(session).Metadata); got.Tasks != SessionTasksFree {
		t.Fatalf("listed access = %+v", got)
	}
}

// A browser lease is a level, its sites and its end; a lease past its end
// is none, and "none" clears the sites and the end.
func TestSessionAccessBrowserLease(t *testing.T) {
	if _, err := NormalizeSessionAccess(SessionAccess{Browser: "always"}); err == nil {
		t.Fatal("accepted an unknown browser level")
	}
	change, err := NormalizeSessionAccess(SessionAccess{Browser: " Confirm ", BrowserSites: "GitHub.com  example.org", BrowserUntil: 2_000})
	if err != nil || change != (SessionAccess{Browser: SessionBrowserConfirm, BrowserSites: "github.com example.org", BrowserUntil: 2_000}) {
		t.Fatalf("normalize = %+v, %v", change, err)
	}
	merged := MergeSessionAccess(SessionAccess{Tasks: SessionTasksFree, Machine: SessionMachineAsk}, change)
	if merged.Machine != SessionMachineAsk || merged.Browser != SessionBrowserConfirm || merged.BrowserSites != "github.com example.org" {
		t.Fatalf("merge = %+v", merged)
	}
	if !merged.BrowserLeaseLive(1_999) || merged.BrowserLeaseLive(2_000) {
		t.Fatal("lease liveness against its end")
	}
	metadata := map[string]any{SessionAccessMetadataKey: SessionAccessMetadata(merged)}
	if got := SessionAccessFromMetadata(metadata); got != merged {
		t.Fatalf("round trip = %+v", got)
	}
	ended := MergeSessionAccess(merged, SessionAccess{Browser: SessionBrowserNone})
	if ended.Browser != SessionBrowserNone || ended.BrowserSites != "" || ended.BrowserUntil != 0 || ended.BrowserLeaseLive(0) {
		t.Fatalf("none = %+v", ended)
	}
}
