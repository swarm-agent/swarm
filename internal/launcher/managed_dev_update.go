package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	managedHostGitSyncApplyPath = "/v1/swarm/managed-hosts/git/sync/apply"
	managedHostUpdateRunPath    = "/v1/swarm/managed-hosts/update/run"
)

type managedDevGitInspectResponse struct {
	OK          bool     `json:"ok"`
	Path        string   `json:"path,omitempty"`
	RepoRoot    string   `json:"repo_root,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Head        string   `json:"head,omitempty"`
	HeadShort   string   `json:"head_short,omitempty"`
	Tree        string   `json:"tree,omitempty"`
	Clean       bool     `json:"clean"`
	StatusShort []string `json:"status_short,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type managedDevTopologyWorkspaceBindingsResponse struct {
	OK       bool                                         `json:"ok"`
	Bindings []managedDevTopologyWorkspaceBindingResponse `json:"bindings,omitempty"`
	Error    string                                       `json:"error,omitempty"`
}

type managedDevTopologyWorkspaceBindingResponse struct {
	DestinationRuntimeSwarmID string `json:"destination_runtime_swarm_id,omitempty"`
	DestinationHostSwarmID    string `json:"destination_host_swarm_id,omitempty"`
	DestinationWorkspacePath  string `json:"destination_workspace_path,omitempty"`
}

type managedDevSwarmTargetsResponse struct {
	OK      bool                    `json:"ok"`
	Targets []managedDevSwarmTarget `json:"targets,omitempty"`
	Error   string                  `json:"error,omitempty"`
}

type managedDevSwarmTarget struct {
	SwarmID      string `json:"swarm_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Relationship string `json:"relationship,omitempty"`
	Selectable   bool   `json:"selectable"`
}

type managedDevManagedHostGitSyncResponse struct {
	OK      bool                                 `json:"ok"`
	Source  managedDevGitInspectResponse         `json:"source,omitempty"`
	Targets []managedDevManagedHostGitSyncTarget `json:"targets,omitempty"`
	Error   string                               `json:"error,omitempty"`
}

type managedDevManagedHostGitSyncTarget struct {
	OK      bool   `json:"ok"`
	SwarmID string `json:"swarm_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Error   string `json:"error,omitempty"`
}

type managedDevManagedHostUpdateRunResponse struct {
	OK     bool `json:"ok"`
	Target struct {
		SwarmID string `json:"swarm_id,omitempty"`
		Name    string `json:"name,omitempty"`
	} `json:"target,omitempty"`
	Error string `json:"error,omitempty"`
}

func runManagedDevHostUpdatePhase(profile Profile) error {
	if strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(profile.URL) == "" || strings.TrimSpace(profile.Root) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	inspect, err := inspectManagedDevGitSource(ctx, profile, false)
	if err != nil {
		return fmt.Errorf("inspect dev git source before managed host update: %w", err)
	}
	if strings.TrimSpace(inspect.RepoRoot) == "" {
		return errors.New("inspect dev git source before managed host update: repo_root is empty")
	}

	bindings, err := listManagedDevTopologyWorkspaceBindings(ctx, profile, inspect.RepoRoot)
	if err != nil {
		return fmt.Errorf("list managed dev workspace bindings: %w", err)
	}
	if len(bindings) == 0 {
		return nil
	}
	targets, err := listManagedDevSwarmTargets(ctx, profile)
	if err != nil {
		return fmt.Errorf("list managed dev swarm targets: %w", err)
	}
	managedTargets := managedDevTargetIDsForBindings(bindings, targets)
	if len(managedTargets) == 0 {
		return nil
	}
	if !inspect.Clean {
		return fmt.Errorf("managed dev update requires clean source checkout at %s; uncommitted changes: %s", inspect.RepoRoot, strings.Join(inspect.StatusShort, "; "))
	}
	if strings.TrimSpace(inspect.Branch) == "" || strings.TrimSpace(inspect.Head) == "" || strings.TrimSpace(inspect.Tree) == "" {
		return errors.New("managed dev update requires source branch, commit, and tree identity")
	}

	headLabel := firstNonEmptyString(inspect.HeadShort, shortGitIdentity(inspect.Head), inspect.Head)
	_ = writeLauncherUpdateJobStatus(profile, updateKindDev, updateJobStatusRunning, fmt.Sprintf("Syncing %d managed host dev checkout(s) to %s.", len(managedTargets), headLabel), "")
	fmt.Fprintf(os.Stdout, "Syncing %d managed host dev checkout(s) to %s before rebuild...\n", len(managedTargets), headLabel)
	for _, targetSwarmID := range managedTargets {
		if err := syncManagedDevHostGit(ctx, profile, targetSwarmID, inspect); err != nil {
			return err
		}
	}

	_ = writeLauncherUpdateJobStatus(profile, updateKindDev, updateJobStatusRunning, fmt.Sprintf("Requesting dev rebuild on %d managed host(s).", len(managedTargets)), "")
	for _, targetSwarmID := range managedTargets {
		if err := runManagedDevHostUpdate(ctx, profile, targetSwarmID); err != nil {
			return err
		}
	}
	return nil
}

func inspectManagedDevGitSource(ctx context.Context, profile Profile, requireClean bool) (managedDevGitInspectResponse, error) {
	payload := map[string]any{"path": profile.Root, "require_clean": requireClean}
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+"/v1/git/sync/inspect", map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, payload)
	if err != nil {
		return managedDevGitInspectResponse{}, err
	}
	var response managedDevGitInspectResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return response, fmt.Errorf("decode git inspect response: %w", decodeErr)
		}
	}
	if status < 200 || status >= 300 {
		return response, fmt.Errorf("git inspect failed (%d): %s", status, firstNonEmptyString(response.Error, responseErrorMessage(body)))
	}
	return response, nil
}

func listManagedDevTopologyWorkspaceBindings(ctx context.Context, profile Profile, sourceRepoRoot string) ([]managedDevTopologyWorkspaceBindingResponse, error) {
	endpoint := profile.URL + "/v1/swarm/topology/workspace-bindings?source_workspace_path=" + url.QueryEscape(strings.TrimSpace(sourceRepoRoot))
	body, status, err := httpRequest(ctx, profile, http.MethodGet, endpoint, map[string]string{"Accept": "application/json"}, nil)
	if err != nil {
		return nil, err
	}
	var response managedDevTopologyWorkspaceBindingsResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return nil, fmt.Errorf("decode topology workspace bindings response: %w", decodeErr)
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("topology workspace bindings failed (%d): %s", status, firstNonEmptyString(response.Error, responseErrorMessage(body)))
	}
	return response.Bindings, nil
}

func listManagedDevSwarmTargets(ctx context.Context, profile Profile) ([]managedDevSwarmTarget, error) {
	body, status, err := httpRequest(ctx, profile, http.MethodGet, profile.URL+"/v1/swarm/targets", map[string]string{"Accept": "application/json"}, nil)
	if err != nil {
		return nil, err
	}
	var response managedDevSwarmTargetsResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return nil, fmt.Errorf("decode swarm targets response: %w", decodeErr)
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("swarm targets failed (%d): %s", status, firstNonEmptyString(response.Error, responseErrorMessage(body)))
	}
	return response.Targets, nil
}

func syncManagedDevHostGit(ctx context.Context, profile Profile, targetSwarmID string, inspect managedDevGitInspectResponse) error {
	payload := map[string]any{
		"target_swarm_id":       strings.TrimSpace(targetSwarmID),
		"source_workspace_path": strings.TrimSpace(inspect.RepoRoot),
		"branch":                strings.TrimSpace(inspect.Branch),
		"commit_sha":            strings.TrimSpace(inspect.Head),
		"tree_sha":              strings.TrimSpace(inspect.Tree),
		"destructive":           true,
	}
	if sourceRepo := strings.TrimSpace(os.Getenv("SWARM_MANAGED_GIT_SYNC_SOURCE_REPO")); sourceRepo != "" {
		payload["source_repo"] = sourceRepo
	}
	if syncRef := strings.TrimSpace(os.Getenv("SWARM_MANAGED_GIT_SYNC_REF")); syncRef != "" {
		payload["sync_ref"] = syncRef
	}
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+managedHostGitSyncApplyPath, map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, payload)
	if err != nil {
		return fmt.Errorf("sync managed host %s git checkout: %w", targetSwarmID, err)
	}
	var response managedDevManagedHostGitSyncResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return fmt.Errorf("decode managed host git sync response for %s: %w", targetSwarmID, decodeErr)
		}
	}
	if status < 200 || status >= 300 || !response.OK {
		return fmt.Errorf("sync managed host %s git checkout failed (%d): %s", targetSwarmID, status, firstNonEmptyString(response.Error, managedDevGitSyncTargetError(response.Targets), responseErrorMessage(body)))
	}
	return nil
}

func runManagedDevHostUpdate(ctx context.Context, profile Profile, targetSwarmID string) error {
	payload := map[string]any{"target_swarm_id": strings.TrimSpace(targetSwarmID)}
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+managedHostUpdateRunPath, map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, payload)
	if err != nil {
		return fmt.Errorf("request managed host %s dev update: %w", targetSwarmID, err)
	}
	var response managedDevManagedHostUpdateRunResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return fmt.Errorf("decode managed host update response for %s: %w", targetSwarmID, decodeErr)
		}
	}
	if status < 200 || status >= 300 || !response.OK {
		return fmt.Errorf("request managed host %s dev update failed (%d): %s", targetSwarmID, status, firstNonEmptyString(response.Error, responseErrorMessage(body)))
	}
	return nil
}

func managedDevTargetIDsForBindings(bindings []managedDevTopologyWorkspaceBindingResponse, targets []managedDevSwarmTarget) []string {
	managed := map[string]bool{}
	for _, target := range targets {
		swarmID := strings.TrimSpace(target.SwarmID)
		if swarmID == "" || !target.Selectable {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(target.Relationship), "managed") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
			continue
		}
		managed[swarmID] = true
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, binding := range bindings {
		swarmID := firstNonEmptyString(binding.DestinationRuntimeSwarmID, binding.DestinationHostSwarmID)
		if swarmID == "" || !managed[swarmID] {
			continue
		}
		if _, ok := seen[swarmID]; ok {
			continue
		}
		seen[swarmID] = struct{}{}
		out = append(out, swarmID)
	}
	sort.Strings(out)
	return out
}

func managedDevGitSyncTargetError(targets []managedDevManagedHostGitSyncTarget) string {
	for _, target := range targets {
		if strings.TrimSpace(target.Error) != "" {
			return strings.TrimSpace(target.Error)
		}
	}
	return ""
}

func shortGitIdentity(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
