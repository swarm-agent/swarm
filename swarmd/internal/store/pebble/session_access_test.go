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
