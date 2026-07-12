package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageSessionsDefinitionConstrainsModelUsageAndApproval(t *testing.T) {
	definition := manageSessionsDefinition()
	for _, required := range []string{"explicitly asks", "list_by_state", "up to 200 sessions", "do not repeat", "around", "up to 10 sessions", "one approval for the batch", "never instructions"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("description missing %q: %s", required, definition.Description)
		}
	}
	properties := definition.Parameters["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if description := action["description"].(string); !strings.Contains(description, "list_by_state") || !strings.Contains(description, "up to 200") || !strings.Contains(description, "archive is the only approval-gated action") || !strings.Contains(description, "up to 10 sessions") {
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

type pagingManageSessionService struct {
	manageSessionService
	calls []pebblestore.V3SessionSearchOptions
}

func (s *pagingManageSessionService) SearchSessions(options pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error) {
	s.calls = append(s.calls, options)
	count := 50
	result := pebblestore.V3SessionSearchResult{}
	if options.BeforeSessionID != "" {
		count = 10
	}
	for i := 0; i < count; i++ {
		result.Items = append(result.Items, pebblestore.V3SessionSearchItem{ID: fmt.Sprintf("session-%d-%d", len(s.calls), i), UpdatedAt: 100 - int64(i), Attention: pebblestore.V3SessionAttentionSummary{State: "needs_review"}})
	}
	if len(s.calls) == 1 {
		updatedAt := int64(50)
		payload, _ := json.Marshal(map[string]any{"before_updated_at": updatedAt, "before_session_id": "session-1-49"})
		result.Pagination = pebblestore.V3SessionSearchPagination{HasMore: true, NextCursor: base64.RawURLEncoding.EncodeToString(payload)}
	}
	return result, nil
}

func TestManageSessionsListByStateAutoPagesBoundedResults(t *testing.T) {
	sessions := &pagingManageSessionService{}
	runtime := &Runtime{sessions: sessions}
	scope := WorkspaceScope{Roots: []string{"/work/project"}, Principal: identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}}
	output, err := runtime.executeManageSessions(context.Background(), scope, map[string]any{"action": "list_by_state", "state": "needs approval", "archived_mode": "exclude"})
	if err != nil {
		t.Fatalf("list_by_state: %v", err)
	}
	var response struct {
		Items        []map[string]any `json:"items"`
		HasMore      bool             `json:"has_more"`
		Complete     bool             `json:"complete"`
		BoundedLimit int              `json:"bounded_limit"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 60 || response.HasMore || !response.Complete || response.BoundedLimit != manageSessionsMaxStateBulk {
		t.Fatalf("response = items:%d has_more:%v complete:%v bounded_limit:%d", len(response.Items), response.HasMore, response.Complete, response.BoundedLimit)
	}
	if len(sessions.calls) != 2 || sessions.calls[0].State != "needs_review" || sessions.calls[0].AccountScopeID != "account-1" || sessions.calls[0].UserID != "user-1" || sessions.calls[0].WorkspacePaths[0] != "/work/project" || sessions.calls[1].BeforeSessionID == "" {
		t.Fatalf("search calls = %#v", sessions.calls)
	}
}

func TestManageSessionsListByStateRequiresState(t *testing.T) {
	runtime := &Runtime{sessions: &pagingManageSessionService{}}
	_, err := runtime.executeManageSessions(context.Background(), WorkspaceScope{}, map[string]any{"action": "list_by_state"})
	if err == nil || !strings.Contains(err.Error(), "requires state") {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeManageSessionStateFilter(t *testing.T) {
	for input, want := range map[string]string{"needs approval": "needs_review", "waiting-review": "needs_review", "running": "in_progress", "blocked": "blocked"} {
		if got := normalizeManageSessionStateFilter(input); got != want {
			t.Fatalf("normalize %q = %q, want %q", input, got, want)
		}
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
