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

	"swarm-refactor/swarmtui/pkg/localupdate"
)

const (
	managedHostGitSyncApplyPath = "/v1/swarm/managed-hosts/git/sync/apply"
	managedHostUpdateRunPath    = "/v1/swarm/managed-hosts/update/run"

	managedDevPhaseInspect   = "inspect"
	managedDevPhaseSync      = "sync"
	managedDevPhaseRebuild   = "rebuild"
	managedDevPhaseReconnect = "reconnect"
	managedDevPhaseVerify    = "verify"
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
	Job   *managedDevUpdateJobSummary `json:"job,omitempty"`
	Error string                      `json:"error,omitempty"`
}

type managedDevUpdateJobSummary struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Status    string `json:"status,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	HelperPID int    `json:"helper_pid,omitempty"`
	LogPath   string `json:"log_path,omitempty"`
}

func runManagedDevHostUpdatePhase(profile Profile) error {
	if strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(profile.URL) == "" || strings.TrimSpace(profile.Root) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	plan, err := inspectManagedDevPlan(ctx, profile, false)
	if err != nil {
		return err
	}
	if len(plan.ManagedTargets) == 0 {
		return nil
	}
	if err := validateManagedDevPlanClean(plan); err != nil {
		return err
	}
	if err := syncManagedDevPlan(ctx, profile, plan); err != nil {
		return err
	}
	return startManagedDevRemoteUpdates(ctx, profile, plan)
}

type managedDevPlan struct {
	Inspect        managedDevGitInspectResponse                 `json:"inspect"`
	Bindings       []managedDevTopologyWorkspaceBindingResponse `json:"bindings,omitempty"`
	Targets        []managedDevSwarmTarget                      `json:"targets,omitempty"`
	ManagedTargets []managedDevSwarmTarget                      `json:"managed_targets,omitempty"`
}

func inspectManagedDevPlan(ctx context.Context, profile Profile, requireClean bool) (managedDevPlan, error) {
	inspect, err := inspectManagedDevGitSource(ctx, profile, requireClean)
	if err != nil {
		return managedDevPlan{}, fmt.Errorf("inspect dev git source before managed host update: %w", err)
	}
	if strings.TrimSpace(inspect.RepoRoot) == "" {
		return managedDevPlan{}, errors.New("inspect dev git source before managed host update: repo_root is empty")
	}

	bindings, err := listManagedDevTopologyWorkspaceBindings(ctx, profile, inspect.RepoRoot)
	if err != nil {
		return managedDevPlan{}, fmt.Errorf("list managed dev workspace bindings: %w", err)
	}
	targets, err := listManagedDevSwarmTargets(ctx, profile)
	if err != nil {
		return managedDevPlan{}, fmt.Errorf("list managed dev swarm targets: %w", err)
	}
	return managedDevPlan{
		Inspect:        inspect,
		Bindings:       bindings,
		Targets:        targets,
		ManagedTargets: managedDevTargetsForBindings(bindings, targets),
	}, nil
}

func validateManagedDevPlanClean(plan managedDevPlan) error {
	inspect := plan.Inspect
	if !inspect.Clean {
		return fmt.Errorf("managed dev update requires clean source checkout at %s; uncommitted changes: %s", inspect.RepoRoot, strings.Join(inspect.StatusShort, "; "))
	}
	if strings.TrimSpace(inspect.Branch) == "" || strings.TrimSpace(inspect.Head) == "" || strings.TrimSpace(inspect.Tree) == "" {
		return errors.New("managed dev update requires source branch, commit, and tree identity")
	}
	return nil
}

func syncManagedDevPlan(ctx context.Context, profile Profile, plan managedDevPlan) error {
	headLabel := firstNonEmptyString(plan.Inspect.HeadShort, shortGitIdentity(plan.Inspect.Head), plan.Inspect.Head)
	_ = writeLauncherUpdateJobStatus(profile, updateKindDev, updateJobStatusRunning, fmt.Sprintf("Syncing %d managed host dev checkout(s) to %s.", len(plan.ManagedTargets), headLabel), "")
	fmt.Fprintf(os.Stdout, "Syncing %d managed host dev checkout(s) to %s before rebuild...\n", len(plan.ManagedTargets), headLabel)
	for _, target := range plan.ManagedTargets {
		advanceManagedDevHostStatus(profile, target, managedDevPhaseInspect, updateJobStatusCompleted, "Managed host dev checkout selected for sync.", "")
		advanceManagedDevHostStatus(profile, target, managedDevPhaseSync, updateJobStatusRunning, fmt.Sprintf("Hard-resetting managed dev checkout to %s.", headLabel), "")
		if err := syncManagedDevHostGit(ctx, profile, target.SwarmID, plan.Inspect); err != nil {
			advanceManagedDevHostStatus(profile, target, managedDevPhaseSync, updateJobStatusFailed, "", err.Error())
			return err
		}
		advanceManagedDevHostStatus(profile, target, managedDevPhaseSync, updateJobStatusCompleted, fmt.Sprintf("Managed dev checkout is synced to %s.", headLabel), "")
	}
	return nil
}

func startManagedDevRemoteUpdates(ctx context.Context, profile Profile, plan managedDevPlan) error {
	_ = writeLauncherUpdateJobStatus(profile, updateKindDev, updateJobStatusRunning, fmt.Sprintf("Requesting dev rebuild on %d managed host(s).", len(plan.ManagedTargets)), "")
	for _, target := range plan.ManagedTargets {
		advanceManagedDevHostStatus(profile, target, managedDevPhaseRebuild, updateJobStatusRunning, "Remote dev rebuild requested.", "")
		if err := runManagedDevHostUpdate(ctx, profile, target.SwarmID); err != nil {
			advanceManagedDevHostStatus(profile, target, managedDevPhaseRebuild, updateJobStatusFailed, "", err.Error())
			return err
		}
		advanceManagedDevHostStatus(profile, target, managedDevPhaseRebuild, updateJobStatusCompleted, "Remote dev rebuild helper accepted the request.", "")
		advanceManagedDevHostStatus(profile, target, managedDevPhaseReconnect, updateJobStatusRunning, "Waiting for managed host backend to restart.", "")
		advanceManagedDevHostStatus(profile, target, managedDevPhaseVerify, updateJobStatusRunning, "Verify from the primary after local rebuild completes.", "")
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
	selected := managedDevTargetsForBindings(bindings, targets)
	out := make([]string, 0, len(selected))
	for _, target := range selected {
		out = append(out, target.SwarmID)
	}
	return out
}

func managedDevTargetsForBindings(bindings []managedDevTopologyWorkspaceBindingResponse, targets []managedDevSwarmTarget) []managedDevSwarmTarget {
	managed := map[string]managedDevSwarmTarget{}
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
		target.SwarmID = swarmID
		target.Name = firstNonEmptyString(target.Name, swarmID)
		managed[swarmID] = target
	}
	seen := map[string]struct{}{}
	out := make([]managedDevSwarmTarget, 0)
	for _, binding := range bindings {
		swarmID := firstNonEmptyString(binding.DestinationRuntimeSwarmID, binding.DestinationHostSwarmID)
		target, ok := managed[swarmID]
		if swarmID == "" || !ok {
			continue
		}
		if _, ok := seen[swarmID]; ok {
			continue
		}
		seen[swarmID] = struct{}{}
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SwarmID < out[j].SwarmID })
	return out
}

func updateLauncherHostStatus(profile Profile, host localupdate.UpdateJobHostStatus) error {
	jobID := strings.TrimSpace(os.Getenv(updateJobIDEnv))
	if jobID == "" || strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(host.HostID) == "" {
		return nil
	}
	existing, ok, _ := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(profile.DataDir))
	if !ok || existing.ID != jobID {
		return nil
	}
	host.HostID = strings.TrimSpace(host.HostID)
	if strings.TrimSpace(host.Name) == "" {
		host.Name = host.HostID
	}
	host.Role = firstNonEmptyString(host.Role, "managed")
	nextHosts := make([]localupdate.UpdateJobHostStatus, 0, len(existing.Hosts)+1)
	replaced := false
	for _, existingHost := range existing.Hosts {
		if strings.TrimSpace(existingHost.HostID) == host.HostID {
			nextHosts = append(nextHosts, host)
			replaced = true
			continue
		}
		nextHosts = append(nextHosts, existingHost)
	}
	if !replaced {
		nextHosts = append(nextHosts, host)
	}
	existing.Hosts = nextHosts
	existing.UpdatedAtUnix = time.Now().UnixMilli()
	return localupdate.WriteUpdateJobStatus(profile.DataDir, existing)
}

func newManagedDevHostStatus(target managedDevSwarmTarget, phaseName, phaseStatus, message, errorMessage string) localupdate.UpdateJobHostStatus {
	now := time.Now().UnixMilli()
	phase := localupdate.UpdateJobHostPhase{
		Name:          strings.TrimSpace(phaseName),
		Status:        strings.TrimSpace(phaseStatus),
		Message:       strings.TrimSpace(message),
		Error:         strings.TrimSpace(errorMessage),
		StartedAtUnix: now,
		UpdatedAtUnix: now,
	}
	if phase.Status == updateJobStatusCompleted || phase.Status == updateJobStatusFailed {
		phase.CompletedAtUnix = now
	}
	hostStatus := phase.Status
	if phase.Status == updateJobStatusCompleted {
		hostStatus = updateJobStatusRunning
	}
	if strings.EqualFold(phase.Name, managedDevPhaseVerify) && phase.Status == updateJobStatusCompleted {
		hostStatus = updateJobStatusCompleted
	}
	return localupdate.UpdateJobHostStatus{
		HostID:       strings.TrimSpace(target.SwarmID),
		Name:         firstNonEmptyString(target.Name, target.SwarmID),
		Role:         "managed",
		CurrentPhase: phase.Name,
		Status:       hostStatus,
		Message:      strings.TrimSpace(message),
		Error:        strings.TrimSpace(errorMessage),
		Phases:       []localupdate.UpdateJobHostPhase{phase},
	}
}

func advanceManagedDevHostStatus(profile Profile, target managedDevSwarmTarget, phaseName, phaseStatus, message, errorMessage string) {
	jobID := strings.TrimSpace(os.Getenv(updateJobIDEnv))
	if jobID == "" || strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(target.SwarmID) == "" {
		return
	}
	existing, ok, _ := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(profile.DataDir))
	if !ok || existing.ID != jobID {
		return
	}
	host := newManagedDevHostStatus(target, phaseName, phaseStatus, message, errorMessage)
	for _, existingHost := range existing.Hosts {
		if strings.TrimSpace(existingHost.HostID) != strings.TrimSpace(target.SwarmID) {
			continue
		}
		host.Phases = mergeUpdateHostPhases(existingHost.Phases, host.Phases[0])
		break
	}
	_ = updateLauncherHostStatus(profile, host)
}

func markManagedDevHostPhase(profile Profile, phaseName, phaseStatus, message, errorMessage string) {
	jobID := strings.TrimSpace(os.Getenv(updateJobIDEnv))
	if jobID == "" || strings.TrimSpace(profile.DataDir) == "" {
		return
	}
	existing, ok, _ := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(profile.DataDir))
	if !ok || existing.ID != jobID {
		return
	}
	for _, host := range existing.Hosts {
		if !strings.EqualFold(strings.TrimSpace(host.Role), "managed") {
			continue
		}
		advanceManagedDevHostStatus(profile, managedDevSwarmTarget{SwarmID: host.HostID, Name: host.Name}, phaseName, phaseStatus, message, errorMessage)
	}
}

func mergeUpdateHostPhases(existing []localupdate.UpdateJobHostPhase, next localupdate.UpdateJobHostPhase) []localupdate.UpdateJobHostPhase {
	out := make([]localupdate.UpdateJobHostPhase, 0, len(existing)+1)
	replaced := false
	for _, phase := range existing {
		if strings.EqualFold(strings.TrimSpace(phase.Name), strings.TrimSpace(next.Name)) {
			if phase.StartedAtUnix > 0 && (next.StartedAtUnix == 0 || next.StartedAtUnix > phase.StartedAtUnix) {
				next.StartedAtUnix = phase.StartedAtUnix
			}
			out = append(out, next)
			replaced = true
			continue
		}
		out = append(out, phase)
	}
	if !replaced {
		out = append(out, next)
	}
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
func RunManagedDevUpdateStep(profile Profile, rawStep string) error {
	step := strings.ToLower(strings.TrimSpace(rawStep))
	if step == "" {
		return errors.New("managed dev update step is required")
	}
	if strings.TrimSpace(profile.URL) == "" || strings.TrimSpace(profile.Root) == "" {
		return errors.New("managed dev update steps require a running backend and source checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	plan, err := inspectManagedDevPlan(ctx, profile, false)
	if err != nil {
		return err
	}
	printManagedDevPlanSummary(plan)

	switch step {
	case "inspect", "plan":
		return nil
	case "sync":
		if err := validateManagedDevPlanClean(plan); err != nil {
			return err
		}
		return syncManagedDevPlan(ctx, profile, plan)
	case "remote-start", "remote", "start-remote":
		if err := validateManagedDevPlanClean(plan); err != nil {
			return err
		}
		return startManagedDevRemoteUpdates(ctx, profile, plan)
	case "verify", "status", "remote-status":
		return verifyManagedDevPlan(ctx, profile, plan)
	default:
		return fmt.Errorf("unknown managed dev update step %q (expected inspect, sync, remote-start, verify/status)", rawStep)
	}
}

func printManagedDevPlanSummary(plan managedDevPlan) {
	inspect := plan.Inspect
	fmt.Fprintf(os.Stdout, "source repo: %s\n", firstNonEmptyString(inspect.RepoRoot, inspect.Path))
	fmt.Fprintf(os.Stdout, "source branch: %s\n", inspect.Branch)
	fmt.Fprintf(os.Stdout, "source head: %s\n", firstNonEmptyString(inspect.HeadShort, shortGitIdentity(inspect.Head), inspect.Head))
	fmt.Fprintf(os.Stdout, "source tree: %s\n", shortGitIdentity(inspect.Tree))
	fmt.Fprintf(os.Stdout, "source clean: %t\n", inspect.Clean)
	if len(inspect.StatusShort) > 0 {
		fmt.Fprintf(os.Stdout, "source changes: %s\n", strings.Join(inspect.StatusShort, "; "))
	}
	fmt.Fprintf(os.Stdout, "workspace bindings: %d\n", len(plan.Bindings))
	fmt.Fprintf(os.Stdout, "managed targets: %d\n", len(plan.ManagedTargets))
	for _, target := range plan.ManagedTargets {
		fmt.Fprintf(os.Stdout, "- %s (%s)\n", firstNonEmptyString(target.Name, target.SwarmID), target.SwarmID)
	}
}

func verifyManagedDevPlan(ctx context.Context, profile Profile, plan managedDevPlan) error {
	if len(plan.ManagedTargets) == 0 {
		return nil
	}
	var failed []string
	for _, target := range plan.ManagedTargets {
		remoteStatus, err := inspectManagedDevHostUpdateStatus(ctx, profile, target.SwarmID)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", firstNonEmptyString(target.Name, target.SwarmID), err))
			continue
		}
		fmt.Fprintf(os.Stdout, "%s update job: status=%s message=%q error=%q pid=%d log=%s\n", firstNonEmptyString(target.Name, target.SwarmID), remoteStatus.Status, remoteStatus.Message, remoteStatus.Error, remoteStatus.HelperPID, remoteStatus.LogPath)
		if remoteStatus.Status == updateJobStatusFailed || strings.TrimSpace(remoteStatus.Error) != "" {
			failed = append(failed, fmt.Sprintf("%s: %s", firstNonEmptyString(target.Name, target.SwarmID), firstNonEmptyString(remoteStatus.Error, remoteStatus.Status)))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("managed dev verify failed: %s", strings.Join(failed, "; "))
	}
	return nil
}

func inspectManagedDevHostUpdateStatus(ctx context.Context, profile Profile, targetSwarmID string) (managedDevUpdateJobSummary, error) {
	payload := map[string]any{"target_swarm_id": strings.TrimSpace(targetSwarmID)}
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+"/v1/swarm/managed-hosts/update/status", map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, payload)
	if err != nil {
		return managedDevUpdateJobSummary{}, fmt.Errorf("inspect managed host %s update status: %w", targetSwarmID, err)
	}
	var response managedDevManagedHostUpdateRunResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return managedDevUpdateJobSummary{}, fmt.Errorf("decode managed host update status response for %s: %w", targetSwarmID, decodeErr)
		}
	}
	if status < 200 || status >= 300 || !response.OK {
		return managedDevUpdateJobSummary{}, fmt.Errorf("inspect managed host %s update status failed (%d): %s", targetSwarmID, status, firstNonEmptyString(response.Error, responseErrorMessage(body)))
	}
	if response.Job == nil {
		return managedDevUpdateJobSummary{}, nil
	}
	return *response.Job, nil
}
