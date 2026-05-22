package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ResourcePack           = "pack"
	ResourceVersion        = "version"
	ResourceTool           = "tool"
	ResourceAdapter        = "adapter"
	ResourcePromptFragment = "prompt_fragment"
	ResourceWorkspace      = "workspace"
	ResourceAssignment     = "assignment"
)

type Service struct {
	store *pebblestore.IntegrationStore
}

func NewService(store *pebblestore.IntegrationStore) *Service {
	return &Service{store: store}
}

type Request struct {
	AccountScopeID string         `json:"-"`
	UserID         string         `json:"-"`
	Action         string         `json:"action"`
	Resource       string         `json:"resource,omitempty"`
	PackID         string         `json:"pack_id,omitempty"`
	VersionID      string         `json:"version_id,omitempty"`
	ID             string         `json:"id,omitempty"`
	Content        map[string]any `json:"content,omitempty"`
	Limit          int            `json:"limit,omitempty"`
}

func (s *Service) Inspect(limit int) (map[string]any, error) {
	return s.InspectForAccount("", limit)
}

func (s *Service) HandleForPrincipal(principal identity.Principal, req Request) (map[string]any, error) {
	if !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, errors.New("authenticated account principal is required")
	}
	req.AccountScopeID = principal.AccountScopeID
	req.UserID = principal.UserID
	return s.Handle(req)
}

func (s *Service) InspectForAccount(accountScopeID string, limit int) (map[string]any, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	packs, err := s.store.ListPacksForAccount(accountScopeID, limit)
	if err != nil {
		return nil, err
	}
	workspaces, err := s.store.ListWorkspacesForAccount(accountScopeID, limit)
	if err != nil {
		return nil, err
	}
	assignments, err := s.store.ListAssignmentsForAccount(accountScopeID, limit)
	if err != nil {
		return nil, err
	}
	return baseResponse("inspect", "ok", map[string]any{
		"packs":             packMaps(packs),
		"pack_count":        len(packs),
		"workspaces":        workspaceMaps(workspaces),
		"workspace_count":   len(workspaces),
		"assignments":       assignmentMaps(assignments),
		"assignment_count":  len(assignments),
		"supported_actions": []string{"inspect", "list", "get", "create", "update", "delete"},
		"resources":         []string{ResourcePack, ResourceVersion, ResourceTool, ResourceAdapter, ResourcePromptFragment, ResourceWorkspace},
		"adapter_types":     []string{pebblestore.IntegrationAdapterTypeCLIWrapper, pebblestore.IntegrationAdapterTypeHostHTTPBridge, pebblestore.IntegrationAdapterTypeUnixSocketBridge, pebblestore.IntegrationAdapterTypeHostedAPI},
		"permission_modes":  []string{pebblestore.IntegrationPermissionModeAllow, pebblestore.IntegrationPermissionModeAskBlocking, pebblestore.IntegrationPermissionModeAskAsync, pebblestore.IntegrationPermissionModeDeny},
		"version_statuses":  []string{pebblestore.IntegrationVersionStatusDraft, pebblestore.IntegrationVersionStatusPublished},
		"capabilities": map[string]any{
			"draft_crud":         true,
			"execution_active":   false,
			"validation_active":  false,
			"publish_active":     false,
			"assignment_runtime": false,
		},
		"secret_policy": map[string]any{
			"raw_secret_storage":   false,
			"credential_refs_only": true,
			"adapter_output":       "credential reference names only; reference values are never returned",
		},
		"schema":       schemaMap(),
		"instructions": "Use manage-integrations for Integration Pack draft inspection and CRUD only. Execution, validation, publish, assignment runtime, and host routing are intentionally inactive in this checkpoint. Store credential reference names only; never store raw secrets in pack settings or credential_refs.",
		"summary":      fmt.Sprintf("found %d integration packs and %d workspaces", len(packs), len(workspaces)),
	}), nil
}

func (s *Service) List(req Request) (map[string]any, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	resource := normalizeResource(req.Resource)
	limit := normalizeLimit(req.Limit)
	if resource == "" || resource == "all" {
		packs, err := s.store.ListPacksForAccount(req.AccountScopeID, limit)
		if err != nil {
			return nil, err
		}
		workspaces, err := s.store.ListWorkspacesForAccount(req.AccountScopeID, limit)
		if err != nil {
			return nil, err
		}
		assignments, err := s.store.ListAssignmentsForAccount(req.AccountScopeID, limit)
		if err != nil {
			return nil, err
		}
		return baseResponse("list", "ok", map[string]any{"resource": "all", "packs": packMaps(packs), "workspaces": workspaceMaps(workspaces), "assignments": assignmentMaps(assignments), "summary": fmt.Sprintf("listed %d packs and %d workspaces", len(packs), len(workspaces))}), nil
	}
	switch resource {
	case ResourcePack:
		packs, err := s.store.ListPacksForAccount(req.AccountScopeID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, packMaps(packs)), nil
	case ResourceVersion:
		packID := normalizeID(req.PackID)
		if packID == "" {
			return nil, errors.New("pack_id is required to list versions")
		}
		versions, err := s.store.ListPackVersionsForAccount(req.AccountScopeID, packID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, versionMaps(versions)), nil
	case ResourceTool:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		tools, err := s.store.ListToolsForAccount(req.AccountScopeID, packID, versionID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, toolMaps(tools)), nil
	case ResourceAdapter:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		adapters, err := s.store.ListAdaptersForAccount(req.AccountScopeID, packID, versionID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, adapterMaps(adapters)), nil
	case ResourcePromptFragment:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		prompts, err := s.store.ListPromptFragmentsForAccount(req.AccountScopeID, packID, versionID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, promptMaps(prompts)), nil
	case ResourceWorkspace:
		workspaces, err := s.store.ListWorkspacesForAccount(req.AccountScopeID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, workspaceMaps(workspaces)), nil
	case ResourceAssignment:
		assignments, err := s.store.ListAssignmentsForAccount(req.AccountScopeID, limit)
		if err != nil {
			return nil, err
		}
		return listResponse(resource, assignmentMaps(assignments)), nil
	default:
		return nil, fmt.Errorf("unsupported integration resource %q", req.Resource)
	}
}

func (s *Service) Get(req Request) (map[string]any, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	resource := normalizeResource(req.Resource)
	id := normalizeID(req.ID)
	switch resource {
	case ResourcePack:
		id = firstNonEmpty(id, normalizeID(req.PackID))
		record, ok, err := s.store.GetPackForAccount(req.AccountScopeID, id)
		if err != nil || !ok {
			return nil, notFoundOrErr(resource, id, ok, err)
		}
		versions, _ := s.store.ListPackVersionsForAccount(req.AccountScopeID, record.PackID, normalizeLimit(req.Limit))
		out := packMap(record)
		out["versions"] = versionMaps(versions)
		return itemResponse("get", resource, out), nil
	case ResourceVersion:
		packID := normalizeID(req.PackID)
		versionID := firstNonEmpty(id, normalizeID(req.VersionID))
		record, ok, err := s.store.GetPackVersionForAccount(req.AccountScopeID, packID, versionID)
		if err != nil || !ok {
			return nil, notFoundOrErr(resource, packID+"/"+versionID, ok, err)
		}
		return itemResponse("get", resource, versionMap(record)), nil
	case ResourceTool:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		record, ok, err := s.store.GetToolForAccount(req.AccountScopeID, packID, versionID, id)
		if err != nil || !ok {
			return nil, notFoundOrErr(resource, id, ok, err)
		}
		return itemResponse("get", resource, toolMap(record)), nil
	case ResourceAdapter:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		record, ok, err := s.store.GetAdapterForAccount(req.AccountScopeID, packID, versionID, id)
		if err != nil || !ok {
			return nil, notFoundOrErr(resource, id, ok, err)
		}
		return itemResponse("get", resource, adapterMap(record)), nil
	case ResourcePromptFragment:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		record, ok, err := s.store.GetPromptFragmentForAccount(req.AccountScopeID, packID, versionID, id)
		if err != nil || !ok {
			return nil, notFoundOrErr(resource, id, ok, err)
		}
		return itemResponse("get", resource, promptMap(record)), nil
	case ResourceWorkspace:
		workspaceID := firstNonEmpty(id, normalizeID(stringField(req.Content, "workspace_id")))
		record, ok, err := s.store.GetWorkspaceForAccount(req.AccountScopeID, workspaceID)
		if err != nil || !ok {
			return nil, notFoundOrErr(resource, workspaceID, ok, err)
		}
		return itemResponse("get", resource, workspaceMap(record)), nil
	default:
		return nil, fmt.Errorf("unsupported integration resource %q", req.Resource)
	}
}

func (s *Service) Upsert(action string, req Request) (map[string]any, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	resource := normalizeResource(req.Resource)
	content := cloneAnyMap(req.Content)
	mergeTopLevelIDs(content, req)
	switch resource {
	case ResourcePack:
		record := packFromContent(content)
		record.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
		record, err := s.store.PutPack(record)
		if err != nil {
			return nil, err
		}
		return itemResponse(action, resource, packMap(record)), nil
	case ResourceVersion:
		record := versionFromContent(content)
		record.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
		record, err := s.store.PutPackVersion(record)
		if err != nil {
			return nil, err
		}
		_ = s.updatePackDraftPointer(req.AccountScopeID, record.PackID, record.VersionID, record.Status)
		return itemResponse(action, resource, versionMap(record)), nil
	case ResourceTool:
		record := toolFromContent(content)
		record.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
		record, err := s.store.PutTool(record)
		if err != nil {
			return nil, err
		}
		_ = s.addVersionChildID(req.AccountScopeID, record.PackID, record.VersionID, "tool", record.ToolID)
		return itemResponse(action, resource, toolMap(record)), nil
	case ResourceAdapter:
		record := adapterFromContent(content)
		record.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
		record, err := s.store.PutAdapter(record)
		if err != nil {
			return nil, err
		}
		_ = s.addVersionChildID(req.AccountScopeID, record.PackID, record.VersionID, "adapter", record.AdapterID)
		return itemResponse(action, resource, adapterMap(record)), nil
	case ResourcePromptFragment:
		record := promptFromContent(content)
		record.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
		record, err := s.store.PutPromptFragment(record)
		if err != nil {
			return nil, err
		}
		_ = s.addVersionChildID(req.AccountScopeID, record.PackID, record.VersionID, "prompt", record.PromptID)
		return itemResponse(action, resource, promptMap(record)), nil
	case ResourceWorkspace:
		record := workspaceFromContent(content)
		record.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
		record, err := s.store.PutWorkspace(record)
		if err != nil {
			return nil, err
		}
		return itemResponse(action, resource, workspaceMap(record)), nil
	default:
		return nil, fmt.Errorf("unsupported integration resource %q", req.Resource)
	}
}

func (s *Service) Delete(req Request) (map[string]any, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	resource := normalizeResource(req.Resource)
	id := normalizeID(req.ID)
	switch resource {
	case ResourcePack:
		id = firstNonEmpty(id, normalizeID(req.PackID))
		if err := s.store.DeletePackForAccount(req.AccountScopeID, id); err != nil {
			return nil, err
		}
	case ResourceVersion:
		if err := s.store.DeletePackVersionForAccount(req.AccountScopeID, normalizeID(req.PackID), firstNonEmpty(id, normalizeID(req.VersionID))); err != nil {
			return nil, err
		}
	case ResourceTool:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteToolForAccount(req.AccountScopeID, packID, versionID, id); err != nil {
			return nil, err
		}
	case ResourceAdapter:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteAdapterForAccount(req.AccountScopeID, packID, versionID, id); err != nil {
			return nil, err
		}
	case ResourcePromptFragment:
		packID, versionID, err := requirePackVersion(req)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeletePromptFragmentForAccount(req.AccountScopeID, packID, versionID, id); err != nil {
			return nil, err
		}
	case ResourceWorkspace:
		if err := s.store.DeleteWorkspaceForAccount(req.AccountScopeID, id); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported integration resource %q", req.Resource)
	}
	return baseResponse("delete", "ok", map[string]any{"resource": resource, "deleted": id, "summary": fmt.Sprintf("deleted integration %s %s", resource, id)}), nil
}

func (s *Service) Handle(req Request) (map[string]any, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "inspect"
	}
	switch action {
	case "inspect":
		return s.InspectForAccount(req.AccountScopeID, req.Limit)
	case "list":
		return s.List(req)
	case "get":
		return s.Get(req)
	case "create", "update":
		return s.Upsert(action, req)
	case "delete":
		return s.Delete(req)
	default:
		return nil, fmt.Errorf("manage-integrations action %q is unsupported", req.Action)
	}
}

func (s *Service) UpsertWorkspaceContext(record pebblestore.IntegrationWorkspaceRecord) (pebblestore.IntegrationWorkspaceRecord, error) {
	if err := s.configured(); err != nil {
		return pebblestore.IntegrationWorkspaceRecord{}, err
	}
	return s.store.PutWorkspace(record)
}

func (s *Service) GetWorkspace(workspaceID string) (pebblestore.IntegrationWorkspaceRecord, bool, error) {
	return s.GetWorkspaceForAccount("", workspaceID)
}

func (s *Service) GetWorkspaceForAccount(accountScopeID, workspaceID string) (pebblestore.IntegrationWorkspaceRecord, bool, error) {
	if err := s.configured(); err != nil {
		return pebblestore.IntegrationWorkspaceRecord{}, false, err
	}
	return s.store.GetWorkspaceForAccount(accountScopeID, workspaceID)
}

func (s *Service) ListWorkspaces(limit int) ([]pebblestore.IntegrationWorkspaceRecord, error) {
	return s.ListWorkspacesForAccount("", limit)
}

func (s *Service) ListWorkspacesForAccount(accountScopeID string, limit int) ([]pebblestore.IntegrationWorkspaceRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	return s.store.ListWorkspacesForAccount(accountScopeID, normalizeLimit(limit))
}

func (s *Service) AttachWorkspaceSession(record pebblestore.IntegrationWorkspaceSessionRecord) (pebblestore.IntegrationWorkspaceSessionRecord, error) {
	if err := s.configured(); err != nil {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, err
	}
	return s.store.PutWorkspaceSession(record)
}

func (s *Service) GetWorkspaceSession(workspaceID, sessionID string) (pebblestore.IntegrationWorkspaceSessionRecord, bool, error) {
	return s.GetWorkspaceSessionForAccount("", workspaceID, sessionID)
}

func (s *Service) GetWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID string) (pebblestore.IntegrationWorkspaceSessionRecord, bool, error) {
	if err := s.configured(); err != nil {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, false, err
	}
	return s.store.GetWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID)
}

func (s *Service) ListWorkspaceSessions(workspaceID string, limit int) ([]pebblestore.IntegrationWorkspaceSessionRecord, error) {
	return s.ListWorkspaceSessionsForAccount("", workspaceID, limit)
}

func (s *Service) ListWorkspaceSessionsForAccount(accountScopeID, workspaceID string, limit int) ([]pebblestore.IntegrationWorkspaceSessionRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	return s.store.ListWorkspaceSessionsForAccount(accountScopeID, workspaceID, normalizeLimit(limit))
}

func (s *Service) DeleteWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	return s.store.DeleteWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID)
}

func (s *Service) LatestWorkspaceSession(workspaceID string) (pebblestore.IntegrationWorkspaceSessionRecord, bool, error) {
	return s.LatestWorkspaceSessionForAccount("", workspaceID)
}

func (s *Service) LatestWorkspaceSessionForAccount(accountScopeID, workspaceID string) (pebblestore.IntegrationWorkspaceSessionRecord, bool, error) {
	if err := s.configured(); err != nil {
		return pebblestore.IntegrationWorkspaceSessionRecord{}, false, err
	}
	return s.store.LatestWorkspaceSessionForAccount(accountScopeID, workspaceID)
}

func (s *Service) configured() error {
	if s == nil || s.store == nil {
		return errors.New("integration service is not configured")
	}
	return nil
}

func baseResponse(action, status string, values map[string]any) map[string]any {
	out := map[string]any{"status": status, "action": action}
	for k, v := range values {
		out[k] = v
	}
	return out
}

func listResponse(resource string, items []map[string]any) map[string]any {
	return baseResponse("list", "ok", map[string]any{"resource": resource, "items": items, "count": len(items), "summary": fmt.Sprintf("listed %d integration %s records", len(items), resource)})
}

func itemResponse(action, resource string, item map[string]any) map[string]any {
	return baseResponse(action, "ok", map[string]any{"resource": resource, "item": item, "summary": fmt.Sprintf("%s integration %s", action, resource)})
}

func schemaMap() map[string]any {
	return map[string]any{
		ResourcePack:           []string{"account_scope_id", "pack_id", "slug", "display_name", "description", "latest_version_id", "draft_version_id", "metadata"},
		ResourceVersion:        []string{"account_scope_id", "pack_id", "version_id", "version", "status", "display_name", "description", "tool_ids", "adapter_ids", "prompt_ids", "metadata"},
		ResourceTool:           []string{"account_scope_id", "pack_id", "version_id", "tool_id", "name", "description", "adapter_id", "permission_mode", "input_schema", "metadata"},
		ResourceAdapter:        []string{"account_scope_id", "pack_id", "version_id", "adapter_id", "type", "display_name", "settings", "credential_refs", "metadata"},
		ResourcePromptFragment: []string{"account_scope_id", "pack_id", "version_id", "prompt_id", "title", "content", "metadata"},
		ResourceWorkspace:      []string{"account_scope_id", "workspace_id", "display_name", "pack_id", "draft_version_id", "metadata"},
	}
}

func packMap(record pebblestore.IntegrationPackRecord) map[string]any {
	return map[string]any{"account_scope_id": record.AccountScopeID, "pack_id": record.PackID, "slug": record.Slug, "display_name": record.DisplayName, "description": record.Description, "latest_version_id": record.LatestVersionID, "draft_version_id": record.DraftVersionID, "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}
func versionMap(record pebblestore.IntegrationPackVersionRecord) map[string]any {
	return map[string]any{"account_scope_id": record.AccountScopeID, "pack_id": record.PackID, "version_id": record.VersionID, "version": record.Version, "status": record.Status, "display_name": record.DisplayName, "description": record.Description, "tool_ids": record.ToolIDs, "adapter_ids": record.AdapterIDs, "prompt_ids": record.PromptIDs, "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}
func toolMap(record pebblestore.IntegrationToolRecord) map[string]any {
	return map[string]any{"account_scope_id": record.AccountScopeID, "pack_id": record.PackID, "version_id": record.VersionID, "tool_id": record.ToolID, "name": record.Name, "description": record.Description, "adapter_id": record.AdapterID, "permission_mode": record.PermissionMode, "input_schema": json.RawMessage(record.InputSchema), "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}
func adapterMap(record pebblestore.IntegrationAdapterRecord) map[string]any {
	keys := sortedKeys(record.CredentialRefs)
	return map[string]any{"account_scope_id": record.AccountScopeID, "pack_id": record.PackID, "version_id": record.VersionID, "adapter_id": record.AdapterID, "type": record.Type, "display_name": record.DisplayName, "settings": record.Settings, "credential_ref_keys": keys, "credential_ref_count": len(keys), "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}
func promptMap(record pebblestore.IntegrationPromptFragmentRecord) map[string]any {
	return map[string]any{"account_scope_id": record.AccountScopeID, "pack_id": record.PackID, "version_id": record.VersionID, "prompt_id": record.PromptID, "title": record.Title, "content": record.Content, "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}
func workspaceMap(record pebblestore.IntegrationWorkspaceRecord) map[string]any {
	return map[string]any{"account_scope_id": record.AccountScopeID, "workspace_id": record.WorkspaceID, "display_name": record.DisplayName, "pack_id": record.PackID, "draft_version_id": record.DraftVersionID, "latest_child_session_id": record.LatestChildSessionID, "latest_child_session_at": record.LatestChildSessionAt, "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}
func assignmentMap(record pebblestore.IntegrationAssignmentRecord) map[string]any {
	return map[string]any{"account_scope_id": record.AccountScopeID, "assignment_id": record.AssignmentID, "agent_name": record.AgentName, "pack_id": record.PackID, "version_id": record.VersionID, "status": record.Status, "metadata": record.Metadata, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt}
}

func packMaps(records []pebblestore.IntegrationPackRecord) []map[string]any {
	return mapSlice(records, packMap)
}
func versionMaps(records []pebblestore.IntegrationPackVersionRecord) []map[string]any {
	return mapSlice(records, versionMap)
}
func toolMaps(records []pebblestore.IntegrationToolRecord) []map[string]any {
	return mapSlice(records, toolMap)
}
func adapterMaps(records []pebblestore.IntegrationAdapterRecord) []map[string]any {
	return mapSlice(records, adapterMap)
}
func promptMaps(records []pebblestore.IntegrationPromptFragmentRecord) []map[string]any {
	return mapSlice(records, promptMap)
}
func workspaceMaps(records []pebblestore.IntegrationWorkspaceRecord) []map[string]any {
	return mapSlice(records, workspaceMap)
}
func assignmentMaps(records []pebblestore.IntegrationAssignmentRecord) []map[string]any {
	return mapSlice(records, assignmentMap)
}

func mapSlice[T any](records []T, fn func(T) map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, fn(record))
	}
	return out
}

func packFromContent(content map[string]any) pebblestore.IntegrationPackRecord {
	return pebblestore.IntegrationPackRecord{PackID: stringField(content, "pack_id"), Slug: stringField(content, "slug"), DisplayName: stringField(content, "display_name"), Description: stringField(content, "description"), LatestVersionID: stringField(content, "latest_version_id"), DraftVersionID: stringField(content, "draft_version_id"), Metadata: stringMapField(content, "metadata")}
}
func versionFromContent(content map[string]any) pebblestore.IntegrationPackVersionRecord {
	return pebblestore.IntegrationPackVersionRecord{PackID: stringField(content, "pack_id"), VersionID: stringField(content, "version_id"), Version: stringField(content, "version"), Status: stringField(content, "status"), DisplayName: stringField(content, "display_name"), Description: stringField(content, "description"), ToolIDs: stringSliceField(content, "tool_ids"), AdapterIDs: stringSliceField(content, "adapter_ids"), PromptIDs: stringSliceField(content, "prompt_ids"), Metadata: stringMapField(content, "metadata")}
}
func toolFromContent(content map[string]any) pebblestore.IntegrationToolRecord {
	return pebblestore.IntegrationToolRecord{PackID: stringField(content, "pack_id"), VersionID: stringField(content, "version_id"), ToolID: stringField(content, "tool_id"), Name: stringField(content, "name"), Description: stringField(content, "description"), AdapterID: stringField(content, "adapter_id"), PermissionMode: stringField(content, "permission_mode"), InputSchema: rawJSONField(content, "input_schema"), Metadata: stringMapField(content, "metadata")}
}
func adapterFromContent(content map[string]any) pebblestore.IntegrationAdapterRecord {
	return pebblestore.IntegrationAdapterRecord{PackID: stringField(content, "pack_id"), VersionID: stringField(content, "version_id"), AdapterID: stringField(content, "adapter_id"), Type: stringField(content, "type"), DisplayName: stringField(content, "display_name"), Settings: stringMapField(content, "settings"), CredentialRefs: stringMapField(content, "credential_refs"), Metadata: stringMapField(content, "metadata")}
}
func promptFromContent(content map[string]any) pebblestore.IntegrationPromptFragmentRecord {
	return pebblestore.IntegrationPromptFragmentRecord{PackID: stringField(content, "pack_id"), VersionID: stringField(content, "version_id"), PromptID: stringField(content, "prompt_id"), Title: stringField(content, "title"), Content: stringField(content, "content"), Metadata: stringMapField(content, "metadata")}
}
func workspaceFromContent(content map[string]any) pebblestore.IntegrationWorkspaceRecord {
	return pebblestore.IntegrationWorkspaceRecord{WorkspaceID: stringField(content, "workspace_id"), DisplayName: stringField(content, "display_name"), PackID: stringField(content, "pack_id"), DraftVersionID: stringField(content, "draft_version_id"), Metadata: stringMapField(content, "metadata")}
}

func (s *Service) updatePackDraftPointer(accountScopeID, packID, versionID, status string) error {
	pack, ok, err := s.store.GetPackForAccount(accountScopeID, packID)
	if err != nil || !ok {
		return err
	}
	if status == pebblestore.IntegrationVersionStatusPublished {
		pack.LatestVersionID = versionID
	} else {
		pack.DraftVersionID = versionID
	}
	_, err = s.store.PutPack(pack)
	return err
}

func (s *Service) addVersionChildID(accountScopeID, packID, versionID, kind, childID string) error {
	version, ok, err := s.store.GetPackVersionForAccount(accountScopeID, packID, versionID)
	if err != nil || !ok {
		return err
	}
	switch kind {
	case "tool":
		version.ToolIDs = appendIfMissing(version.ToolIDs, childID)
	case "adapter":
		version.AdapterIDs = appendIfMissing(version.AdapterIDs, childID)
	case "prompt":
		version.PromptIDs = appendIfMissing(version.PromptIDs, childID)
	}
	_, err = s.store.PutPackVersion(version)
	return err
}

func appendIfMissing(values []string, value string) []string {
	value = normalizeID(value)
	if value == "" {
		return values
	}
	for _, current := range values {
		if normalizeID(current) == value {
			return values
		}
	}
	return append(values, value)
}

func normalizeResource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return strings.ToLower(strings.TrimSpace(value))
	case "pack", "packs":
		return ResourcePack
	case "version", "versions", "pack_version", "pack-version", "draft", "draft_version":
		return ResourceVersion
	case "tool", "tools":
		return ResourceTool
	case "adapter", "adapters":
		return ResourceAdapter
	case "prompt", "prompts", "prompt_fragment", "prompt-fragment", "prompt_fragments":
		return ResourcePromptFragment
	case "workspace", "workspaces":
		return ResourceWorkspace
	case "assignment", "assignments":
		return ResourceAssignment
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeID(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func normalizeLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 200
	}
	return limit
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func requirePackVersion(req Request) (string, string, error) {
	packID := normalizeID(firstNonEmpty(req.PackID, stringField(req.Content, "pack_id")))
	versionID := normalizeID(firstNonEmpty(req.VersionID, stringField(req.Content, "version_id")))
	if packID == "" || versionID == "" {
		return "", "", errors.New("pack_id and version_id are required")
	}
	return packID, versionID, nil
}

func notFoundOrErr(resource, id string, ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("integration %s %q not found", resource, id)
	}
	return nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func mergeTopLevelIDs(content map[string]any, req Request) {
	if _, ok := content["pack_id"]; !ok && strings.TrimSpace(req.PackID) != "" {
		content["pack_id"] = req.PackID
	}
	if _, ok := content["version_id"]; !ok && strings.TrimSpace(req.VersionID) != "" {
		content["version_id"] = req.VersionID
	}
	if _, ok := content["id"]; !ok && strings.TrimSpace(req.ID) != "" {
		content["id"] = req.ID
	}
	switch normalizeResource(req.Resource) {
	case ResourcePack:
		if _, ok := content["pack_id"]; !ok {
			content["pack_id"] = req.ID
		}
	case ResourceVersion:
		if _, ok := content["version_id"]; !ok {
			content["version_id"] = req.ID
		}
	case ResourceTool:
		if _, ok := content["tool_id"]; !ok {
			content["tool_id"] = req.ID
		}
	case ResourceAdapter:
		if _, ok := content["adapter_id"]; !ok {
			content["adapter_id"] = req.ID
		}
	case ResourcePromptFragment:
		if _, ok := content["prompt_id"]; !ok {
			content["prompt_id"] = req.ID
		}
	case ResourceWorkspace:
		if _, ok := content["workspace_id"]; !ok {
			content["workspace_id"] = req.ID
		}
	}
}

func stringField(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func stringMapField(values map[string]any, key string) map[string]string {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	out := map[string]string{}
	switch typed := raw.(type) {
	case map[string]string:
		for k, v := range typed {
			if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	case map[string]any:
		for k, v := range typed {
			if strings.TrimSpace(k) != "" && strings.TrimSpace(fmt.Sprint(v)) != "" {
				out[strings.TrimSpace(k)] = strings.TrimSpace(fmt.Sprint(v))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stringSliceField(values map[string]any, key string) []string {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	out := []string{}
	switch typed := raw.(type) {
	case []string:
		out = append(out, typed...)
	case []any:
		for _, value := range typed {
			out = append(out, strings.TrimSpace(fmt.Sprint(value)))
		}
	}
	return out
}

func rawJSONField(values map[string]any, key string) json.RawMessage {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append(json.RawMessage(nil), typed...)
	case string:
		return json.RawMessage(strings.TrimSpace(typed))
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		return encoded
	}
}

func sortedKeys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, strings.TrimSpace(key))
	}
	sort.Strings(out)
	return out
}
