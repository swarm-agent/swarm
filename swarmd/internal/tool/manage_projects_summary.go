package tool

import (
	"strings"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// projectContextPreviewBytes bounds the project.md text manage_projects get
// returns by default; detail="context" returns it whole.
const projectContextPreviewBytes = 2048

// projectSummary is the compact project shape manage_projects list and get
// return: identity, workspaces and current task counts. It is built field by
// field so stored data such as the icon image, theme or full project.md never
// reaches the model through list.
func (r *Runtime) projectSummary(accountScopeID string, project *pebblestore.ProjectRecord) (map[string]any, error) {
	workspaces := make([]map[string]any, 0, len(project.Workspaces))
	for _, workspace := range project.Workspaces {
		row := map[string]any{"path": workspace.Path}
		if workspace.WorkspaceID != "" {
			row["id"] = workspace.WorkspaceID
		}
		if workspace.Role != "" {
			row["role"] = workspace.Role
		}
		workspaces = append(workspaces, row)
	}
	tasks, scanTruncated, err := r.projects.ListProjectTaskRecordsForList(accountScopeID, project.ID)
	if err != nil {
		return nil, err
	}
	page := pebblestore.ProjectTaskListFilter{Scope: pebblestore.ProjectTaskScopeCurrent}.Select(tasks, projectTaskListNow().UnixMilli())
	counts := map[string]any{"total": page.Total, "by_status": page.StatusCounts}
	if scanTruncated {
		counts["scan_truncated"] = true
	}
	return map[string]any{
		"id":            project.ID,
		"name":          project.Name,
		"workspaces":    workspaces,
		"current_tasks": counts,
	}, nil
}

// truncateProjectContext cuts text to at most limit bytes on a rune boundary.
func truncateProjectContext(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimRight(text[:cut], " \t\r\n"), true
}
