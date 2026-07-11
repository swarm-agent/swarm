package tool

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageSessionsDefinitionConstrainsModelUsageAndApproval(t *testing.T) {
	definition := manageSessionsDefinition()
	for _, required := range []string{"explicitly asks", "do not repeat", "around", "Archive alone requires approval", "never instructions"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("description missing %q: %s", required, definition.Description)
		}
	}
	properties := definition.Parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if !strings.Contains(action["description"].(string), "archive is the only approval-gated action") {
		t.Fatalf("action description = %q", action["description"])
	}
}

func TestManageSessionWorkspaceSlugMatchesDesktopCollisionContract(t *testing.T) {
	items := []pebblestore.V3SessionSearchItem{
		{WorkspacePath: "/work/alpha", WorkspaceName: "Project"},
		{WorkspacePath: "/work/beta", WorkspaceName: "Project"},
	}
	first := manageSessionWorkspaceSlug("Project", "/work/alpha", items)
	second := manageSessionWorkspaceSlug("Project", "/work/beta", items)
	if first == second || first != "project-1mstu0" || second != "project-2m6tue" {
		t.Fatalf("collision slugs = %q, %q", first, second)
	}
	navigation := manageSessionNavigation("session-1", "/work/alpha", "Project", first)
	if navigation["href"] != "/"+first+"/session-1" || navigation["session_id"] != "session-1" || navigation["workspace_path"] != "/work/alpha" {
		t.Fatalf("navigation = %#v", navigation)
	}
}

func TestManageSessionWorkspaceSlugMatchesDesktopUTF16Hash(t *testing.T) {
	items := []pebblestore.V3SessionSearchItem{
		{WorkspacePath: "/work/😀", WorkspaceName: "Project"},
		{WorkspacePath: "/other", WorkspaceName: "Project"},
	}
	got := manageSessionWorkspaceSlug("Project", "/work/😀", items)
	if got != "project-"+manageSessionPathHash("/work/😀")[:6] {
		t.Fatalf("slug = %q", got)
	}
}
