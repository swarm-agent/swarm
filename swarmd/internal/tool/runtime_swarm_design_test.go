package tool

import (
	"context"
	"strings"
	"testing"
)

// Requirement: swarm_design is the registered design tool (schema with a
// required brief, contract id tool.swarm-design.v1) and the tool runtime never
// executes it itself; manage_design, artifact_v3_author and manage_artifact's
// HTML authoring actions are gone from the definitions and refused with an
// error naming swarm_design, while read_v3 and media generation stay listed.
// Threat: a retired authoring path stays callable or falls through to another
// action, or swarm_design runs outside the calling run's usage and workspace
// boundary. Owners: Runtime.Definitions, Runtime.executeOne, toolPathID and
// executeManageArtifact; the runtime without services is the narrowest layer
// because every refusal happens before any service is consulted.
func TestSwarmDesignReplacesRetiredDesignAuthoringTools(t *testing.T) {
	r := NewRuntime(1)
	byName := map[string]Definition{}
	for _, def := range r.Definitions() {
		byName[def.Name] = def
	}
	def, ok := byName[SwarmDesignToolName]
	if !ok {
		t.Fatal("swarm_design is not registered")
	}
	params, _ := def.Parameters["properties"].(map[string]any)
	required, _ := def.Parameters["required"].([]string)
	for _, field := range []string{"brief", "count", "style", "variations", "batch_name", "folder", "edit_path"} {
		if _, ok := params[field]; !ok {
			t.Fatalf("swarm_design schema lacks %q", field)
		}
	}
	if len(required) != 1 || required[0] != "brief" || def.Parameters["additionalProperties"] != false || !strings.Contains(def.Description, "Never write HTML by hand instead") {
		t.Fatalf("swarm_design schema: required=%v definition=%+v", required, def)
	}
	if got := toolPathID(SwarmDesignToolName); got != "tool.swarm-design.v1" {
		t.Fatalf("contract id = %q", got)
	}
	for _, retired := range []string{"manage_design", "artifact_v3_author"} {
		if _, ok := byName[retired]; ok {
			t.Fatalf("retired tool %s is still registered", retired)
		}
	}
	actions, _ := byName["manage_artifact"].Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
	listed := strings.Join(actions, ",")
	for _, gone := range []string{"create", "revise_v3", "begin_v3", "author_v3", "resume_v3", "draft_status_v3"} {
		for _, action := range actions {
			if action == gone {
				t.Fatalf("manage_artifact still lists %s: %s", gone, listed)
			}
		}
	}
	for _, kept := range []string{"read_v3", "import", "generate_image", "generate_video", "generate_audio", "export_html_animation"} {
		if !strings.Contains(","+listed+",", ","+kept+",") {
			t.Fatalf("manage_artifact lost %s: %s", kept, listed)
		}
	}

	ctx := context.Background()
	scope := WorkspaceScope{PrimaryPath: t.TempDir(), SessionID: "session-a"}
	for _, call := range []Call{
		{CallID: "c1", Name: SwarmDesignToolName, Arguments: `{"brief":"a page"}`},
		{CallID: "c2", Name: "manage_design", Arguments: `{"action":"submit"}`},
		{CallID: "c3", Name: "artifact_v3_author", Arguments: `{"action":"inspect_context"}`},
	} {
		if _, err := r.executeOne(ctx, scope, call, nil); err == nil || !strings.Contains(err.Error(), "swarm_design") {
			t.Fatalf("%s: err = %v", call.Name, err)
		}
	}
	for _, action := range []string{"create", "revise_v3", "begin_v3", "author_v3", "resume_v3", "draft_status_v3"} {
		if _, err := r.executeManageArtifact(ctx, scope, "c4", map[string]any{"action": action, "content": "<!doctype html><html><body>x</body></html>"}); err == nil || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "swarm_design") {
			t.Fatalf("manage_artifact %s: err = %v", action, err)
		}
	}
	if _, err := r.executeManageArtifact(ctx, scope, "c5", map[string]any{"action": "read_v3", "narration_plan": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "narration_plan was removed") {
		t.Fatalf("narration_plan err = %v", err)
	}
}
