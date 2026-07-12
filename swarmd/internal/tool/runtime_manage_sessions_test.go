package tool

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageSessionsDefinitionConstrainsModelUsageAndApproval(t *testing.T) {
	definition := manageSessionsDefinition()
	for _, required := range []string{"explicitly asks", "do not repeat", "around", "up to 10 sessions", "one approval for the batch", "never instructions"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("description missing %q: %s", required, definition.Description)
		}
	}
	properties := definition.Parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if description := action["description"].(string); !strings.Contains(description, "archive is the only approval-gated action") || !strings.Contains(description, "up to 10 sessions") {
		t.Fatalf("action description = %q", description)
	}
	sessionIDs := properties["session_ids"].(map[string]any)
	if sessionIDs["maxItems"] != manageSessionsMaxBatch || !strings.Contains(sessionIDs["description"].(string), "instead of requesting one archive at a time") {
		t.Fatalf("session_ids schema = %#v", sessionIDs)
	}
	proposals := properties["proposals"].(map[string]any)
	if proposals["maxItems"] != manageSessionsMaxDeployBatch || !strings.Contains(proposals["description"].(string), "first proposal") {
		t.Fatalf("proposals schema = %#v", proposals)
	}
	proposal := proposals["items"].(map[string]any)
	if proposal["additionalProperties"] != false {
		t.Fatalf("proposal trust boundary = %#v", proposal)
	}
	expectedByID := properties["expected_updated_at_by_id"].(map[string]any)
	if expectedByID["maxProperties"] != manageSessionsMaxBatch || !strings.Contains(expectedByID["description"].(string), "Required for bulk archive") {
		t.Fatalf("expected_updated_at_by_id schema = %#v", expectedByID)
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
