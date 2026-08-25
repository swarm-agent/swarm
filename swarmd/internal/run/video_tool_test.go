package run

import (
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

func TestMaterializeSessionVideoToolFiltersForbiddenStudioActions(t *testing.T) {
	definitions := convertToolDefinitions(tool.NewRuntime(1).Definitions())
	studio := MaterializeSessionVideoTool(definitions, map[string]any{"lineage_kind": "video_project"})

	var actions []string
	for _, definition := range studio {
		if definition.Name != "manage_video" {
			continue
		}
		properties, _ := definition.Parameters["properties"].(map[string]any)
		action, _ := properties["action"].(map[string]any)
		actions, _ = action["enum"].([]string)
	}
	if !reflect.DeepEqual(actions, tool.ManageVideoActionNames(true)) {
		t.Fatalf("studio action enum = %#v", actions)
	}
	for _, forbidden := range []string{"create_revision", "restore_revision", "start_render"} {
		for _, action := range actions {
			if action == forbidden {
				t.Fatalf("studio action enum exposes %q", forbidden)
			}
		}
	}

	chat := MaterializeSessionVideoTool(definitions, nil)
	if !reflect.DeepEqual(chat, definitions) {
		t.Fatal("non-Studio definitions changed")
	}
}
