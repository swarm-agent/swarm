package tool

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A project conversation binds no workspace to its session. Its bash, read,
// search and find calls that name no folder run in the project's primary_code
// folder: resolved per call from the project record and the account's
// workspace catalog, never stored on the session. Other folders of the
// project still need an explicit path (bash workspace_path, or inspect_files).

// projectDefaultFolderTools are the tools that default to the primary folder.
func projectDefaultFolderTool(name string) bool {
	switch name {
	case "bash", "read", "search", "find":
		return true
	}
	return false
}

// projectPrimaryFolder is the primary_code folder of the project whose
// conversation scope is, authorized by the principal's workspace catalog. It
// is "" with no error when scope is not an unrestricted project conversation
// (any other session keeps its own workspace rules).
func (r *Runtime) projectPrimaryFolder(scope WorkspaceScope) (string, error) {
	if r == nil || r.sessions == nil || r.projects == nil || r.workspace == nil || strings.TrimSpace(scope.PrimaryPath) != "" ||
		!scope.ExplicitRepositoryRecovery || scope.ReviewCheckout != "" || scope.TaskHistoryOnly || len(scope.MutationScopes) != 0 || len(scope.ReadOnlyRoots) != 0 ||
		strings.TrimSpace(scope.SessionID) == "" || !scope.Principal.Valid() {
		return "", nil
	}
	session, found, err := r.sessions.GetSession(strings.TrimSpace(scope.SessionID))
	if err != nil {
		return "", err
	}
	if !found || session.AccountScopeID != scope.Principal.AccountScopeID || session.UserID != scope.Principal.UserID {
		return "", nil
	}
	projectID := pebblestore.ProjectConversationID(session)
	if projectID == "" {
		return "", nil
	}
	project, found, err := r.projects.GetProject(session.AccountScopeID, projectID)
	if err != nil {
		return "", err
	}
	if !found || project == nil {
		return "", fmt.Errorf("project %q of this conversation was not found", projectID)
	}
	if len(project.Workspaces) == 0 {
		return "", errProjectWithoutWorkspace
	}
	primary := ""
	for _, w := range project.Workspaces {
		if w.Role == "primary_code" && strings.TrimSpace(w.Path) != "" {
			primary = strings.TrimSpace(w.Path)
			break
		}
	}
	if primary == "" {
		return "", errors.New("this project has no primary_code folder to default to; name the folder: bash workspace_path, or manage_projects inspect_files with its workspace_path")
	}
	canonical, err := canonicalExistingPath(primary)
	if err != nil || canonical != filepath.Clean(primary) {
		return "", fmt.Errorf("the project's primary_code folder %s is missing or not a canonical path; the person fixes the project's folders in the app", primary)
	}
	owned, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, canonical)
	if err != nil {
		return "", err
	}
	if !owned.Matched || filepath.Clean(owned.WorkspacePath) != canonical {
		return "", fmt.Errorf("the project's primary_code folder %s is not one of your workspaces; the person adds it in the app", canonical)
	}
	return canonical, nil
}

// projectDefaultFolderApplies reports whether a read, search or find call
// names only paths inside primary: relative paths, none, or absolute paths
// within it. A call naming another folder keeps its own path rules.
func projectDefaultFolderApplies(name, primary string, args map[string]any) bool {
	paths := []string{asString(args["path"])}
	if name == "search" || name == "find" {
		paths = append(paths, asStringSlice(args["paths"])...)
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		rel, err := filepath.Rel(primary, filepath.Clean(path))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}
