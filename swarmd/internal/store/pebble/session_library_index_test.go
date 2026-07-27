package pebblestore

import "testing"

func TestResolveV3SessionLineageUsesRootAndMarksBrokenLineage(t *testing.T) {
	sessions := map[string]SessionSnapshot{
		"root":          {ID: "root", UpdatedAt: 100},
		"child":         {ID: "child", UpdatedAt: 300, Metadata: map[string]any{"parent_session_id": "root", "lineage_kind": "delegated_subagent"}},
		"managed":       {ID: "managed", UpdatedAt: 350, Metadata: map[string]any{"parent_session_id": "root", "lineage_kind": "session_deploy"}},
		"managed-child": {ID: "managed-child", UpdatedAt: 375, Metadata: map[string]any{"parent_session_id": "managed", "lineage_kind": "delegated_subagent"}},
		"orphan":        {ID: "orphan", UpdatedAt: 200, Metadata: map[string]any{"parent_session_id": "missing"}},
		"cycle-a":       {ID: "cycle-a", UpdatedAt: 400, Metadata: map[string]any{"parent_session_id": "cycle-b"}},
		"cycle-b":       {ID: "cycle-b", UpdatedAt: 500, Metadata: map[string]any{"parent_session_id": "cycle-a"}},
	}

	lineage := ResolveV3SessionLineage(sessions)
	if got := lineage["child"]; got.RootSessionID != "root" || got.UnlinkedChild || got.LineageKind != "delegated_subagent" {
		t.Fatalf("child lineage = %+v", got)
	}
	if got := lineage["managed"]; got.ParentSessionID != "root" || got.RootSessionID != "managed" || got.UnlinkedChild || got.LineageKind != "session_deploy" {
		t.Fatalf("managed lineage = %+v", got)
	}
	if got := lineage["managed-child"]; got.RootSessionID != "managed" || got.UnlinkedChild {
		t.Fatalf("managed child lineage = %+v", got)
	}
	if got := lineage["orphan"]; !got.UnlinkedChild || got.RootSessionID != "orphan" {
		t.Fatalf("orphan lineage = %+v", got)
	}
	if !lineage["cycle-a"].UnlinkedChild || !lineage["cycle-b"].UnlinkedChild {
		t.Fatalf("cycle lineage = a:%+v b:%+v", lineage["cycle-a"], lineage["cycle-b"])
	}
}
