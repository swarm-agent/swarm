package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestListTopSessionsByWorkspaceLimitCountsParentSessionsOnly(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	sessions := NewSessionStore(store)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("parent-%02d", i)
		if err := sessions.CreateSession(SessionSnapshot{
			ID:            id,
			WorkspacePath: workspacePath,
			WorkspaceName: "workspace",
			Title:         id,
			UpdatedAt:     int64(1000 - i),
			CreatedAt:     int64(1000 - i),
		}); err != nil {
			t.Fatalf("create parent session %s: %v", id, err)
		}
	}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("child-%02d", i)
		if err := sessions.CreateSession(SessionSnapshot{
			ID:            id,
			WorkspacePath: workspacePath,
			WorkspaceName: "workspace",
			Title:         id,
			UpdatedAt:     int64(2000 - i),
			CreatedAt:     int64(2000 - i),
			Metadata: map[string]any{
				"parent_session_id":  "parent-00",
				"requested_subagent": "parallel",
			},
		}); err != nil {
			t.Fatalf("create child session %s: %v", id, err)
		}
	}

	groups, err := sessions.ListTopSessionsByWorkspace([]string{workspacePath}, 25)
	if err != nil {
		t.Fatalf("list top sessions: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}

	parentCount := 0
	seen := make(map[string]bool)
	for _, session := range groups[0].Sessions {
		seen[session.ID] = true
		if sessionMetadataString(session.Metadata, "parent_session_id") == "" {
			parentCount++
		}
	}
	if parentCount != 25 {
		t.Fatalf("parent session count = %d, want 25; sessions=%v", parentCount, sessionIDs(groups[0].Sessions))
	}
	if !seen["parent-24"] {
		t.Fatalf("expected 25th parent session parent-24 to be included; sessions=%v", sessionIDs(groups[0].Sessions))
	}
	if seen["parent-25"] {
		t.Fatalf("did not expect 26th parent session parent-25 to be included; sessions=%v", sessionIDs(groups[0].Sessions))
	}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("child-%02d", i)
		if !seen[id] {
			t.Fatalf("expected selected parent child %s to be included; sessions=%v", id, sessionIDs(groups[0].Sessions))
		}
	}
}

func TestListTopSessionsByWorkspaceExcludesChildrenOfUnselectedParents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	sessions := NewSessionStore(store)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("parent-%02d", i)
		if err := sessions.CreateSession(SessionSnapshot{
			ID:            id,
			WorkspacePath: workspacePath,
			WorkspaceName: "workspace",
			Title:         id,
			UpdatedAt:     int64(100 - i),
			CreatedAt:     int64(100 - i),
		}); err != nil {
			t.Fatalf("create parent session %s: %v", id, err)
		}
	}
	if err := sessions.CreateSession(SessionSnapshot{
		ID:            "child-selected-parent",
		WorkspacePath: workspacePath,
		WorkspaceName: "workspace",
		Title:         "child-selected-parent",
		UpdatedAt:     200,
		CreatedAt:     200,
		Metadata: map[string]any{
			"parent_session_id":  "parent-00",
			"requested_subagent": "explorer",
		},
	}); err != nil {
		t.Fatalf("create selected parent child: %v", err)
	}
	if err := sessions.CreateSession(SessionSnapshot{
		ID:            "child-unselected-parent",
		WorkspacePath: workspacePath,
		WorkspaceName: "workspace",
		Title:         "child-unselected-parent",
		UpdatedAt:     300,
		CreatedAt:     300,
		Metadata: map[string]any{
			"parent_session_id":  "parent-02",
			"requested_subagent": "explorer",
		},
	}); err != nil {
		t.Fatalf("create unselected parent child: %v", err)
	}

	groups, err := sessions.ListTopSessionsByWorkspace([]string{workspacePath}, 2)
	if err != nil {
		t.Fatalf("list top sessions: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}

	seen := make(map[string]bool)
	for _, session := range groups[0].Sessions {
		seen[session.ID] = true
	}
	if !seen["parent-00"] || !seen["parent-01"] {
		t.Fatalf("expected top two parent sessions to be included; sessions=%v", sessionIDs(groups[0].Sessions))
	}
	if !seen["child-selected-parent"] {
		t.Fatalf("expected child of selected parent to be included; sessions=%v", sessionIDs(groups[0].Sessions))
	}
	if seen["child-unselected-parent"] {
		t.Fatalf("did not expect child of unselected parent to be included; sessions=%v", sessionIDs(groups[0].Sessions))
	}
}

func sessionIDs(sessions []SessionSnapshot) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}
