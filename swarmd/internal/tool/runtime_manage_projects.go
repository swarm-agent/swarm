package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type manageProjectStore interface {
	PutProject(accountScopeID string, proj *pebblestore.ProjectRecord) error
	GetProject(accountScopeID, id string) (*pebblestore.ProjectRecord, bool, error)
	ListProjects(accountScopeID string, limit int) ([]pebblestore.ProjectRecord, error)
	DeleteProject(accountScopeID, id string) error
	UpdateProject(accountScopeID, id string, mutate func(*pebblestore.ProjectRecord) error) (*pebblestore.ProjectRecord, error)
}

func manageProjectsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_projects",
		Description: "Inspect and manage Projects aggregating workspaces, context (project.md), and ongoing tasks. Supported actions: list, get, create, update, delete, synthesize_context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Action: list|get|create|update|delete|synthesize_context",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Project ID for get, update, delete",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Project display name",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional project description",
				},
				"workspaces": map[string]any{
					"type":        "array",
					"description": "Array of workspace objects [{path, role, label}]",
					"items":       map[string]any{"type": "object"},
				},
				"project_context": map[string]any{
					"type":        "string",
					"description": "Synthesized project.md markdown text",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		},
	}
}

func (r *Runtime) SetManageProjectStore(store manageProjectStore) {
	if r == nil {
		return
	}
	r.projects = store
}

func (r *Runtime) executeManageProjects(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.projects == nil {
		return "", errors.New("manage_projects service is not configured")
	}
	accountScopeID := strings.TrimSpace(scope.Principal.AccountScopeID)
	if accountScopeID == "" {
		return "", errors.New("manage_projects requires an authenticated account scope")
	}

	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}

	response := map[string]any{
		"tool":              "manage_projects",
		"action":            actionName,
		"status":            "ok",
		"path_id":           toolPathID("manage_projects"),
		"details_truncated": false,
	}

	switch actionName {
	case "list":
		projects, err := r.projects.ListProjects(accountScopeID, 100)
		if err != nil {
			return "", err
		}
		if projects == nil {
			projects = []pebblestore.ProjectRecord{}
		}
		response["projects"] = projects
		response["count"] = len(projects)

	case "get":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("project id is required for get")
		}
		proj, found, err := r.projects.GetProject(accountScopeID, id)
		if err != nil {
			return "", err
		}
		if !found || proj == nil {
			return "", fmt.Errorf("project %q not found", id)
		}
		response["project"] = proj

	case "create":
		name := strings.TrimSpace(asString(args["name"]))
		if name == "" {
			return "", errors.New("project name is required for create")
		}
		desc := strings.TrimSpace(asString(args["description"]))
		pCtx := asString(args["project_context"])

		var wsRefs []pebblestore.ProjectWorkspaceRef
		if wsRaw, ok := args["workspaces"].([]any); ok {
			for _, item := range wsRaw {
				if m, ok := item.(map[string]any); ok {
					ref := pebblestore.ProjectWorkspaceRef{
						WorkspaceID: asString(m["workspace_id"]),
						Path:        asString(m["path"]),
						Role:        asString(m["role"]),
						Label:       asString(m["label"]),
					}
					if ref.Path != "" {
						wsRefs = append(wsRefs, ref)
					}
				}
			}
		}

		proj := &pebblestore.ProjectRecord{
			Name:           name,
			Description:    desc,
			Workspaces:     wsRefs,
			ProjectContext: pCtx,
		}
		if err := r.projects.PutProject(accountScopeID, proj); err != nil {
			return "", err
		}
		response["project"] = proj

	case "update":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("project id is required for update")
		}
		updated, err := r.projects.UpdateProject(accountScopeID, id, func(p *pebblestore.ProjectRecord) error {
			if v := strings.TrimSpace(asString(args["name"])); v != "" {
				p.Name = v
			}
			if v, ok := args["description"]; ok {
				p.Description = strings.TrimSpace(asString(v))
			}
			if v, ok := args["project_context"]; ok {
				p.ProjectContext = asString(v)
			}
			if wsRaw, ok := args["workspaces"].([]any); ok {
				var wsRefs []pebblestore.ProjectWorkspaceRef
				for _, item := range wsRaw {
					if m, ok := item.(map[string]any); ok {
						ref := pebblestore.ProjectWorkspaceRef{
							WorkspaceID: asString(m["workspace_id"]),
							Path:        asString(m["path"]),
							Role:        asString(m["role"]),
							Label:       asString(m["label"]),
						}
						if ref.Path != "" {
							wsRefs = append(wsRefs, ref)
						}
					}
				}
				p.Workspaces = wsRefs
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		response["project"] = updated

	case "delete":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("project id is required for delete")
		}
		if err := r.projects.DeleteProject(accountScopeID, id); err != nil {
			return "", err
		}
		response["id"] = id
		response["deleted"] = true

	case "synthesize_context":
		var wsPaths []string
		if wsRaw, ok := args["workspaces"].([]any); ok {
			for _, item := range wsRaw {
				if m, ok := item.(map[string]any); ok {
					if p := asString(m["path"]); p != "" {
						wsPaths = append(wsPaths, p)
					}
				} else if s, ok := item.(string); ok && s != "" {
					wsPaths = append(wsPaths, s)
				}
			}
		}

		projectName := strings.TrimSpace(asString(args["name"]))
		if projectName == "" {
			projectName = "Project"
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# %s\n\n", projectName))
		sb.WriteString("## Workspaces & Architecture\n")
		for _, p := range wsPaths {
			base := filepath.Base(p)
			sb.WriteString(fmt.Sprintf("- `%s` (`%s`)\n", base, p))
			// Bounded scan for README.md or AGENTS.md
			for _, docName := range []string{"README.md", "AGENTS.md"} {
				docPath := filepath.Join(p, docName)
				if fi, err := os.Stat(docPath); err == nil && !fi.IsDir() {
					if data, err := os.ReadFile(docPath); err == nil {
						lines := strings.Split(string(data), "\n")
						for i, l := range lines {
							if i > 5 {
								break
							}
							trimmed := strings.TrimSpace(l)
							if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
								sb.WriteString(fmt.Sprintf("  > %s\n", trimmed))
								break
							}
						}
					}
				}
			}
		}
		sb.WriteString("\n## Operating Rules\n- Local-first architecture; durable V3 sessions.\n- Keep changes minimal, tested, and high-craft.\n")

		response["synthesized_context"] = sb.String()

	default:
		return "", fmt.Errorf("unknown manage_projects action: %q", actionName)
	}

	raw, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
