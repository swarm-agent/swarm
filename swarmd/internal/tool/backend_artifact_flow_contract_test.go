package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageArtifactBackendLifecycleAndPromotionContract(t *testing.T) {
	authority := &fakeArtifactAuthority{readBody: []byte("managed body")}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = t.TempDir()

	execute := func(callID, arguments string) map[string]any {
		t.Helper()
		output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: callID, Name: "manage_artifact", Arguments: arguments})
		if err != nil {
			t.Fatalf("%s: %v", callID, err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(output), &payload); err != nil {
			t.Fatalf("decode %s: %v", callID, err)
		}
		if payload["status"] != "ok" || payload["path_id"] == "" {
			t.Fatalf("%s response = %#v", callID, payload)
		}
		return payload
	}

	created := execute("contract-create", `{"action":"create","collection_name":"Drafts","filename":"draft.txt","media_type":"text/plain","content":"managed body"}`)
	reference := created["reference"].(map[string]any)
	collectionID, variantID := reference["collection_id"].(string), reference["variant_id"].(string)
	if collectionID == "" || variantID == "" || reference["session_id"] == "" || reference["event_seq"] == float64(0) || authority.created.RequestID == "" {
		t.Fatalf("create did not produce exact durable reference: %#v", created)
	}
	if created["artifact"].(map[string]any)["session_id"] == "" {
		t.Fatalf("artifact variant payload missing session_id: %#v", created)
	}

	authority.variant = pebblestore.SessionArtifactVariant{
		ID: variantID, CollectionID: collectionID, SessionID: "session-1",
		Status: pebblestore.SessionArtifactStatusReady, EventSeq: 41,
		Filename: "draft.txt", MediaType: "text/plain",
	}
	listed := execute("contract-list", `{"action":"list","collection_id":"`+collectionID+`"}`)
	if listed["count"] != float64(1) {
		t.Fatalf("list response = %#v", listed)
	}
	got := execute("contract-get", `{"action":"get","variant_id":"`+variantID+`"}`)
	read := execute("contract-read", `{"action":"read","variant_id":"`+variantID+`","max_bytes":64}`)
	if got["artifact"] == nil || read["content"] != "managed body" {
		t.Fatalf("get/read responses = %#v / %#v", got, read)
	}
	selected := execute("contract-select", `{"action":"select","collection_id":"`+collectionID+`","variant_id":"`+variantID+`"}`)
	if selected["reference"].(map[string]any)["variant_id"] != variantID {
		t.Fatalf("selection response = %#v", selected)
	}

	promoted := execute("contract-promote", `{"action":"promote","session_id":"session-1","collection_id":"`+collectionID+`","variant_id":"`+variantID+`","event_seq":41,"destination":"designs/final.txt"}`)
	if authority.workspaceRoot != scope.PrimaryPath || authority.destination != "designs/final.txt" || authority.materializedRef.EventSeq != 41 {
		t.Fatalf("promotion did not bind exact reference and trusted workspace: root=%q destination=%q ref=%+v", authority.workspaceRoot, authority.destination, authority.materializedRef)
	}
	encoded, err := json.Marshal(promoted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), scope.PrimaryPath) || strings.Contains(string(encoded), "storage_path") {
		t.Fatalf("promotion exposed a private or absolute path: %s", encoded)
	}

	execute("contract-delete", `{"action":"delete","collection_id":"`+collectionID+`","variant_id":"`+variantID+`"}`)
	if authority.deleted != collectionID+"/"+variantID {
		t.Fatalf("delete target = %q", authority.deleted)
	}
}

func TestManageArtifactPromotionRejectsManagedChildAndUntrustedDestination(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(scope.PrimaryPath, 0o755); err != nil {
		t.Fatal(err)
	}

	managed := WithArtifactRunContext(ctx, ArtifactRunContext{
		SessionID: "session-1", ChildSessionID: "session-1", TaskCallID: "task-call",
		CollectionID: "managed-collection", VariantID: "managed-variant",
	})
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(managed, scope, Call{CallID: "managed-promote", Name: "manage_artifact", Arguments: `{"action":"promote","session_id":"session-1","collection_id":"managed-collection","variant_id":"managed-variant","event_seq":1,"destination":"out.txt"}`})
	if err == nil || !strings.Contains(err.Error(), "explicit parent workspace action") {
		t.Fatalf("managed child promotion error = %v", err)
	}

	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: scope.SessionID, Principal: scope.Principal}, Call{CallID: "missing-root-promote", Name: "manage_artifact", Arguments: `{"action":"promote","session_id":"session-1","collection_id":"collection","variant_id":"variant","event_seq":1,"destination":"out.txt"}`})
	if err == nil || !strings.Contains(err.Error(), "trusted workspace root") {
		t.Fatalf("missing trusted workspace error = %v", err)
	}
}
