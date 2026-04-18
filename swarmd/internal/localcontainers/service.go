package localcontainers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	PathRuntimeStatus         = "swarm.containers.local.runtime-status.v1"
	PathContainerList         = "swarm.containers.local.list.v1"
	PathContainerCreate       = "swarm.containers.local.create.v1"
	PathContainerAction       = "swarm.containers.local.action.v1"
	PathContainerDelete       = "swarm.containers.local.delete.v1"
	PathContainerPrune        = "swarm.containers.local.prune-missing.v1"
	defaultImageName          = "localhost/swarm-container-mvp:latest"
	defaultContainerPath      = "/workspaces"
	containerBackendPort      = startupconfig.DefaultPort
	containerDesktopPort      = startupconfig.DefaultDesktopPort
	defaultPackageManager     = "apt"
	supportedPackageBaseImage = "ubuntu:24.04"
)

var containerPackageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

type Mount = pebblestore.SwarmLocalContainerMount

type ContainerPackageSelection = pebblestore.ContainerPackageSelectionRecord

type ContainerPackageManifest = pebblestore.ContainerPackageManifestRecord

type RuntimeStatus struct {
	Recommended string            `json:"recommended"`
	Available   []string          `json:"available"`
	Installed   []string          `json:"installed,omitempty"`
	Issues      map[string]string `json:"issues,omitempty"`
	Warning     string            `json:"warning,omitempty"`
	PathID      string            `json:"path_id"`
}

type Container struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	ContainerName  string  `json:"container_name"`
	Runtime        string  `json:"runtime"`
	NetworkName    string  `json:"network_name,omitempty"`
	Status         string  `json:"status"`
	ContainerID    string  `json:"container_id,omitempty"`
	HostAPIBaseURL string  `json:"host_api_base_url,omitempty"`
	HostPort       int     `json:"host_port"`
	RuntimePort    int     `json:"runtime_port"`
	Image          string  `json:"image,omitempty"`
	Warning        string  `json:"warning,omitempty"`
	Mounts         []Mount `json:"mounts,omitempty"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

type CreateInput struct {
	Name              string
	Runtime           string
	NetworkName       string
	HostAPIBaseURL    string
	HostPort          int
	Image             string
	ContainerPackages ContainerPackageManifest
	Mounts            []Mount
	Env               []string
	ExtraRunArgs      []string
	RuntimeMount      *RuntimeMount
}

type ActionInput struct {
	ID     string
	Action string
}

type DeleteItemResult struct {
	ID                      string `json:"id"`
	Name                    string `json:"name,omitempty"`
	ContainerName           string `json:"container_name,omitempty"`
	Deleted                 bool   `json:"deleted"`
	ChildSwarmID            string `json:"child_swarm_id,omitempty"`
	ChildDisplayName        string `json:"child_display_name,omitempty"`
	ChildInfoDetected       bool   `json:"child_info_detected,omitempty"`
	RemovedDeployment       bool   `json:"removed_deployment,omitempty"`
	RemovedTrustedPeer      bool   `json:"removed_trusted_peer,omitempty"`
	RemovedGroupMemberships int    `json:"removed_group_memberships,omitempty"`
	Error                   string `json:"error,omitempty"`
}

type DeleteResult struct {
	Deleted          []string           `json:"deleted"`
	Count            int                `json:"count"`
	Failed           int                `json:"failed,omitempty"`
	ChildInfoRemoved int                `json:"child_info_removed,omitempty"`
	Items            []DeleteItemResult `json:"items,omitempty"`
}

type RuntimeNetwork struct {
	Runtime string `json:"runtime"`
	Name    string `json:"name"`
	Gateway string `json:"gateway,omitempty"`
}

type Service struct {
	store              *pebblestore.SwarmLocalContainerStore
	deployments        *pebblestore.DeployContainerStore
	swarmStore         *pebblestore.SwarmStore
	authStore          *pebblestore.AuthStore
	workspace          *workspaceruntime.Service
	startupPath        string
	inspectContainerFn func(string, string) (string, string, error)
	hostCallbackURLsMu sync.RWMutex
	hostCallbackURLs   map[string]string
}

func NewService(store *pebblestore.SwarmLocalContainerStore, deployments *pebblestore.DeployContainerStore, swarmStore *pebblestore.SwarmStore, authStore *pebblestore.AuthStore, workspaceSvc *workspaceruntime.Service, startupPath string) *Service {
	return &Service{
		store:              store,
		deployments:        deployments,
		swarmStore:         swarmStore,
		authStore:          authStore,
		workspace:          workspaceSvc,
		startupPath:        strings.TrimSpace(startupPath),
		inspectContainerFn: inspectContainer,
		hostCallbackURLs:   make(map[string]string),
	}
}

func (s *Service) RuntimeStatus(ctx context.Context) (RuntimeStatus, error) {
	installed := detectAvailableRuntimes()
	status := RuntimeStatus{PathID: PathRuntimeStatus}
	if len(installed) == 0 {
		status.Warning = "Install Podman or Docker to create local swarms on this machine."
		return status, nil
	}
	status.Installed = append([]string(nil), installed...)
	usable := make([]string, 0, len(installed))
	issues := make(map[string]string, len(installed))
	warnings := make([]string, 0, len(installed))
	for _, candidate := range installed {
		if err := runtimeAvailableInCurrentContext(ctx, candidate); err != nil {
			message := strings.TrimSpace(err.Error())
			issues[candidate] = message
			warnings = append(warnings, fmt.Sprintf("%s is installed but unavailable here: %s", runtimeDisplayName(candidate), message))
			continue
		}
		usable = append(usable, candidate)
		if status.Recommended == "" {
			status.Recommended = candidate
		}
	}
	status.Available = usable
	if len(issues) > 0 {
		status.Issues = issues
	}
	if status.Recommended == "" {
		status.Warning = strings.Join(warnings, " ")
		return status, nil
	}
	if len(warnings) > 0 {
		status.Warning = strings.Join(warnings, " ")
	}
	return status, nil
}

func (s *Service) List(context.Context) ([]Container, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("local container service is not configured")
	}
	records, err := s.store.List(500)
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(records))
	for _, record := range records {
		resolved, resolveErr := s.resolveRecord(record)
		if resolveErr != nil {
			resolved.Warning = resolveErr.Error()
		}
		out = append(out, mapRecord(resolved))
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Container, error) {
	if s == nil || s.store == nil {
		return Container{}, errors.New("local container service is not configured")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Container{}, errors.New("name is required")
	}
	status, err := s.RuntimeStatus(ctx)
	if err != nil {
		return Container{}, err
	}
	runtimeName := normalizeRuntimeSelection(input.Runtime)
	if runtimeName == "" {
		runtimeName = status.Recommended
	}
	if runtimeName == "" {
		return Container{}, errors.New(status.Warning)
	}
	if !contains(status.Available, runtimeName) {
		if _, lookErr := exec.LookPath(runtimeName); lookErr == nil {
			if runtimeErr := runtimeAvailableInCurrentContext(ctx, runtimeName); runtimeErr != nil {
				return Container{}, fmt.Errorf("%s is installed but unavailable here: %w", runtimeDisplayName(runtimeName), runtimeErr)
			}
		}
		return Container{}, fmt.Errorf("runtime %q is not available", runtimeName)
	}
	hostAPIBaseURL := strings.TrimSpace(input.HostAPIBaseURL)
	if hostAPIBaseURL == "" {
		hostAPIBaseURL, err = s.defaultHostAPIBaseURL(runtimeName)
		if err != nil {
			return Container{}, err
		}
	}
	hostPort, err := s.ResolveCreateHostPort(hostAPIBaseURL, input.HostPort)
	if err != nil {
		return Container{}, err
	}
	image, err := resolveLocalContainerImage(ctx, runtimeName, strings.TrimSpace(input.Image), input.ContainerPackages)
	if err != nil {
		return Container{}, err
	}
	runtimeMount := normalizeRuntimeMount(input.RuntimeMount)
	containerName := suggestedContainerName(name)
	networkName := sanitizeSlug(input.NetworkName)
	preparedNetwork := RuntimeNetwork{Runtime: runtimeName, Name: networkName}
	if networkName != "" {
		preparedNetwork, err = PrepareRuntimeNetwork(ctx, runtimeName, networkName)
		if err != nil {
			return Container{}, err
		}
	}
	mounts := normalizeMounts(input.Mounts)
	runtimeMounts, runtimeEnv, runtimeErr := containerRuntimeMountResources(runtimeMount)
	if runtimeErr != nil {
		return Container{}, runtimeErr
	}
	mounts = append(mounts, runtimeMounts...)
	extraRunArgs := normalizeRunArgs(input.ExtraRunArgs)
	if runtimeName == "podman" && preparedNetwork.Name != "" && preparedNetwork.Gateway != "" {
		extraRunArgs = append(extraRunArgs, "--add-host", fmt.Sprintf("host.containers.internal:%s", preparedNetwork.Gateway))
	}
	if runtimeName == "docker" {
		extraRunArgs = append(extraRunArgs, "--add-host", "host.docker.internal:host-gateway")
	}
	env := ensureLocalSwarmRuntimeEnv(normalizeEnv(input.Env))
	env = append(env, runtimeEnv...)
	log.Printf("local container create runtime=%q name=%q network=%q gateway=%q host_port=%d mounts=%d env=%d extra_args=%d runtime_mount=%t", runtimeName, name, preparedNetwork.Name, preparedNetwork.Gateway, hostPort, len(mounts), len(env), len(extraRunArgs), runtimeMount != nil)
	containerID, runErr := runContainer(ctx, runtimeName, runOptions{
		ContainerName: containerName,
		NetworkName:   preparedNetwork.Name,
		HostPort:      hostPort,
		Image:         image,
		Mounts:        mounts,
		Env:           env,
		ExtraRunArgs:  extraRunArgs,
	})
	record := pebblestore.SwarmLocalContainerRecord{
		ID:             containerName,
		Name:           name,
		ContainerName:  containerName,
		Runtime:        runtimeName,
		NetworkName:    preparedNetwork.Name,
		Status:         "running",
		ContainerID:    strings.TrimSpace(containerID),
		HostAPIBaseURL: hostAPIBaseURL,
		HostPort:       hostPort,
		RuntimePort:    hostPort,
		Image:          image,
		Mounts:         mounts,
	}
	if runErr != nil {
		record.Status = "created"
		record.Warning = runErr.Error()
	}
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return Container{}, saveErr
	}
	resolved, resolveErr := s.resolveRecord(saved)
	if resolveErr != nil && strings.TrimSpace(resolved.Warning) == "" {
		resolved.Warning = resolveErr.Error()
	}
	return mapRecord(resolved), runErr
}

func (s *Service) Act(ctx context.Context, input ActionInput) (Container, error) {
	if s == nil || s.store == nil {
		return Container{}, errors.New("local container service is not configured")
	}
	record, ok, err := s.store.Get(input.ID)
	if err != nil {
		return Container{}, err
	}
	if !ok {
		return Container{}, errors.New("local container not found")
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "start" && action != "stop" {
		return Container{}, errors.New("action must be start or stop")
	}
	if err := controlContainer(ctx, record.Runtime, action, record.ContainerName); err != nil {
		record.Warning = err.Error()
	} else {
		record.Warning = ""
		if action == "start" {
			record.Status = "running"
		} else {
			record.Status = "exited"
		}
	}
	saved, saveErr := s.store.Put(record)
	if saveErr != nil {
		return Container{}, saveErr
	}
	resolved, resolveErr := s.resolveRecord(saved)
	if resolveErr != nil && strings.TrimSpace(resolved.Warning) == "" {
		resolved.Warning = resolveErr.Error()
	}
	if strings.TrimSpace(saved.Warning) != "" {
		return mapRecord(resolved), errors.New(saved.Warning)
	}
	return mapRecord(resolved), nil
}

func (s *Service) BulkDelete(ctx context.Context, containerIDs []string) (DeleteResult, error) {
	if s == nil || s.store == nil {
		return DeleteResult{}, errors.New("local container service is not configured")
	}
	ids := normalizeDeleteIDs(containerIDs)
	if len(ids) == 0 {
		return DeleteResult{}, errors.New("at least one local container id is required")
	}

	items := make([]DeleteItemResult, len(ids))
	var wg sync.WaitGroup
	for i, containerID := range ids {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			items[index] = s.deleteContainer(ctx, id)
		}(i, containerID)
	}
	wg.Wait()

	result := DeleteResult{Deleted: make([]string, 0, len(items)), Items: make([]DeleteItemResult, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, item)
		if item.Deleted {
			result.Deleted = append(result.Deleted, item.ID)
			result.Count++
		}
		if item.Error != "" {
			result.Failed++
		}
		if item.RemovedDeployment || item.RemovedTrustedPeer || item.RemovedGroupMemberships > 0 {
			result.ChildInfoRemoved++
		}
	}
	if result.Failed > 0 {
		return result, fmt.Errorf("failed to delete %d local container(s)", result.Failed)
	}
	return result, nil
}

func (s *Service) deleteContainer(ctx context.Context, containerID string) DeleteItemResult {
	record, ok, err := s.store.Get(containerID)
	if err != nil {
		return DeleteItemResult{ID: strings.TrimSpace(containerID), Error: err.Error()}
	}
	if !ok {
		return DeleteItemResult{ID: strings.TrimSpace(containerID), Error: "local container not found"}
	}

	item := DeleteItemResult{
		ID:            record.ID,
		Name:          record.Name,
		ContainerName: record.ContainerName,
	}
	attachments := s.findChildAttachments(record)
	if len(attachments) > 0 {
		item.ChildInfoDetected = true
		for _, attachment := range attachments {
			if item.ChildSwarmID == "" && strings.TrimSpace(attachment.childSwarmID) != "" {
				item.ChildSwarmID = attachment.childSwarmID
			}
			if item.ChildDisplayName == "" && strings.TrimSpace(attachment.childDisplayName) != "" {
				item.ChildDisplayName = attachment.childDisplayName
			}
			if item.ChildSwarmID != "" && item.ChildDisplayName != "" {
				break
			}
		}
	}

	if err := removeContainer(ctx, record.Runtime, record.ContainerName); err != nil && !isMissingContainerRemoveError(err) {
		item.Error = err.Error()
		return item
	}
	if err := s.store.Delete(record.ID); err != nil {
		item.Error = err.Error()
		return item
	}
	item.Deleted = true

	removedDeployments := map[string]struct{}{}
	childSwarmIDs := map[string]struct{}{}
	for _, attachment := range attachments {
		if attachment.deploymentID != "" {
			if _, seen := removedDeployments[attachment.deploymentID]; !seen {
				if err := s.deployments.Delete(attachment.deploymentID); err != nil {
					item.Error = err.Error()
					return item
				}
				removedDeployments[attachment.deploymentID] = struct{}{}
				item.RemovedDeployment = true
			}
		}
		if attachment.childSwarmID != "" {
			childSwarmIDs[attachment.childSwarmID] = struct{}{}
		}
	}
	if s.swarmStore != nil {
		for childSwarmID := range childSwarmIDs {
			memberships, err := s.swarmStore.ListGroupMembershipsBySwarm(childSwarmID, 500)
			if err != nil {
				item.Error = err.Error()
				return item
			}
			for _, membership := range memberships {
				if err := s.swarmStore.DeleteGroupMembership(membership.GroupID, membership.SwarmID); err != nil {
					item.Error = err.Error()
					return item
				}
				item.RemovedGroupMemberships++
			}
			if err := s.swarmStore.DeleteTrustedPeer(childSwarmID); err != nil {
				item.Error = err.Error()
				return item
			}
			item.RemovedTrustedPeer = true
		}
	}
	if s.authStore != nil {
		for childSwarmID := range childSwarmIDs {
			if _, err := s.authStore.DeleteCredentialsByOwnerSwarmID(childSwarmID); err != nil {
				item.Error = err.Error()
				return item
			}
		}
	}
	if s.workspace != nil {
		for childSwarmID := range childSwarmIDs {
			if err := s.workspace.RemoveReplicationLinksByTargetSwarmID(childSwarmID); err != nil {
				item.Error = err.Error()
				return item
			}
		}
	}
	return item
}

type childAttachment struct {
	deploymentID     string
	childSwarmID     string
	childDisplayName string
}

func (s *Service) findChildAttachments(record pebblestore.SwarmLocalContainerRecord) []childAttachment {
	if s == nil || s.deployments == nil {
		return nil
	}
	deployments, err := s.deployments.List(500)
	if err != nil {
		return nil
	}
	attachments := make([]childAttachment, 0, 1)
	seenDeployments := map[string]struct{}{}
	for _, deployment := range deployments {
		if !deploymentMatchesRecord(deployment, record) {
			continue
		}
		if _, seen := seenDeployments[deployment.ID]; seen {
			continue
		}
		seenDeployments[deployment.ID] = struct{}{}
		attachments = append(attachments, childAttachment{
			deploymentID:     deployment.ID,
			childSwarmID:     deployment.ChildSwarmID,
			childDisplayName: firstNonEmpty(deployment.ChildDisplayName, deployment.ChildSwarmID),
		})
	}
	return attachments
}

func deploymentMatchesRecord(deployment pebblestore.DeployContainerRecord, record pebblestore.SwarmLocalContainerRecord) bool {
	deploymentID := strings.TrimSpace(deployment.ID)
	recordID := strings.TrimSpace(record.ID)
	deploymentContainerName := strings.TrimSpace(deployment.ContainerName)
	recordContainerName := strings.TrimSpace(record.ContainerName)
	deploymentContainerID := strings.TrimSpace(deployment.ContainerID)
	recordContainerID := strings.TrimSpace(record.ContainerID)
	return (deploymentID != "" && deploymentID == recordID) || (deploymentContainerName != "" && deploymentContainerName == recordContainerName) || (deploymentContainerID != "" && deploymentContainerID == recordContainerID)
}

func isMissingContainerRemoveError(err error) bool {
	return isMissingContainerLookupError(err)
}

func isMissingContainerLookupError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "no such container") || strings.Contains(message, "no container with name or id") || (strings.Contains(message, "container") && strings.Contains(message, "not found"))
}

func IsMissingRuntimeContainerError(err error) bool {
	return isMissingContainerLookupError(err)
}

func normalizeDeleteIDs(containerIDs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(containerIDs))
	for _, value := range containerIDs {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func removeContainer(ctx context.Context, runtimeName, containerName string) error {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	containerName = strings.TrimSpace(containerName)
	if runtimeName == "" {
		return errors.New("container runtime is required")
	}
	if containerName == "" {
		return errors.New("container name is required")
	}
	cmd := exec.CommandContext(ctx, runtimeName, "rm", "-f", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("remove %s container: %s", runtimeName, message)
	}
	return nil
}

func RemoveRuntimeContainer(ctx context.Context, runtimeName, containerName string) error {
	return removeContainer(ctx, runtimeName, containerName)
}

func (s *Service) PruneMissing(ctx context.Context) (DeleteResult, error) {
	if s == nil || s.store == nil {
		return DeleteResult{}, errors.New("local container service is not configured")
	}
	records, err := s.store.List(500)
	if err != nil {
		return DeleteResult{}, err
	}
	result := DeleteResult{Deleted: make([]string, 0)}
	for _, record := range records {
		resolved, _ := s.resolveRecord(record)
		if resolved.Status != "missing" {
			continue
		}
		if err := s.store.Delete(record.ID); err != nil {
			return result, err
		}
		result.Deleted = append(result.Deleted, record.ID)
	}
	result.Count = len(result.Deleted)
	return result, nil
}

func (s *Service) resolveRecord(record pebblestore.SwarmLocalContainerRecord) (pebblestore.SwarmLocalContainerRecord, error) {
	if record.Runtime == "" || record.ContainerName == "" {
		return record, nil
	}
	inspector := inspectContainer
	if s != nil && s.inspectContainerFn != nil {
		inspector = s.inspectContainerFn
	}
	status, containerID, err := inspector(record.Runtime, record.ContainerName)
	if err != nil {
		if isMissingContainerLookupError(err) {
			record.Status = "missing"
		} else if strings.TrimSpace(record.Status) == "" {
			record.Status = "created"
		}
		return record, err
	}
	record.Status = status
	if strings.TrimSpace(containerID) != "" {
		record.ContainerID = containerID
	}
	return record, nil
}

type RuntimeMount struct {
	BinDir     string
	ToolBinDir string
	WebDistDir string
	FFFLibPath string
}

type runOptions struct {
	ContainerName string
	NetworkName   string
	HostPort      int
	Image         string
	Mounts        []Mount
	Env           []string
	ExtraRunArgs  []string
}

func detectAvailableRuntimes() []string {
	available := make([]string, 0, 2)
	for _, candidate := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(candidate); err == nil {
			available = append(available, candidate)
		}
	}
	return available
}

func runtimeAvailableInCurrentContext(parent context.Context, runtimeName string) error {
	args := runtimeAvailabilityProbeArgs(runtimeName)
	if len(args) == 0 {
		return fmt.Errorf("unsupported runtime")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtimeName, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("probe timed out")
	}
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if idx := strings.IndexAny(message, "\r\n"); idx >= 0 {
		message = strings.TrimSpace(message[:idx])
	}
	return errors.New(message)
}

func runtimeAvailabilityProbeArgs(runtimeName string) []string {
	switch normalizeRuntimeSelection(runtimeName) {
	case "podman":
		return []string{"info", "--format", "{{.Version.Version}}"}
	case "docker":
		return []string{"info", "--format", "{{.ServerVersion}}"}
	default:
		return nil
	}
}

func runtimeImageExists(parent context.Context, runtimeName, image string) (bool, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image = strings.TrimSpace(image)
	if runtimeName == "" || image == "" {
		return false, fmt.Errorf("runtime and image are required")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	args := runtimeImageExistsArgs(runtimeName, image)
	if len(args) == 0 {
		return false, fmt.Errorf("unsupported runtime %q", runtimeName)
	}
	cmd := exec.CommandContext(ctx, runtimeName, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return false, fmt.Errorf("image probe timed out")
	}
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch runtimeName {
		case "podman":
			if exitErr.ExitCode() == 1 {
				return false, nil
			}
		case "docker":
			if exitErr.ExitCode() == 1 {
				return false, nil
			}
		}
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if idx := strings.IndexAny(message, "\r\n"); idx >= 0 {
		message = strings.TrimSpace(message[:idx])
	}
	return false, errors.New(message)
}

func runtimeImageExistsArgs(runtimeName, image string) []string {
	switch normalizeRuntimeSelection(runtimeName) {
	case "podman":
		return []string{"image", "exists", image}
	case "docker":
		return []string{"image", "inspect", image}
	default:
		return nil
	}
}

func runtimeDisplayName(runtimeName string) string {
	switch normalizeRuntimeSelection(runtimeName) {
	case "podman":
		return "Podman"
	case "docker":
		return "Docker"
	default:
		return strings.TrimSpace(runtimeName)
	}
}

func normalizeRuntimeSelection(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "podman":
		return "podman"
	case "docker":
		return "docker"
	default:
		return ""
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ChildReachableHostAlias(runtimeName string) string {
	switch normalizeRuntimeSelection(runtimeName) {
	case "docker":
		return "host.docker.internal"
	case "podman":
		return "host.containers.internal"
	default:
		return ""
	}
}

func ResolveChildReachableHostAliasIP(runtimeName string) (string, error) {
	host := ChildReachableHostAlias(runtimeName)
	if host == "" {
		return "", fmt.Errorf("runtime %q does not define a child-reachable host alias", strings.TrimSpace(runtimeName))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch normalizeRuntimeSelection(runtimeName) {
	case "podman":
		cmd = exec.CommandContext(ctx, "podman", "run", "--rm", "docker.io/library/busybox:latest", "sh", "-c", fmt.Sprintf("awk '/%s/ {print $1; exit}' %s", regexpQuoteForSingleQuotedAwk(host), systemHostsFilePath()))
	case "docker":
		cmd = exec.CommandContext(ctx, "docker", "run", "--rm", "--add-host", "host.docker.internal:host-gateway", "busybox:latest", "sh", "-c", fmt.Sprintf("awk '/%s/ {print $1; exit}' %s", regexpQuoteForSingleQuotedAwk(host), systemHostsFilePath()))
	default:
		return "", fmt.Errorf("runtime %q does not define a child-reachable host alias", strings.TrimSpace(runtimeName))
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("resolve %s alias IP from %s container: %s", host, runtimeName, message)
	}
	ipText := strings.TrimSpace(string(output))
	if net.ParseIP(ipText) == nil {
		return "", fmt.Errorf("resolve %s alias IP from %s container: invalid IP %q", host, runtimeName, ipText)
	}
	return ipText, nil
}

func regexpQuoteForSingleQuotedAwk(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `.`, `\.`)
	return value
}

func systemHostsFilePath() string {
	return string(filepath.Separator) + filepath.Join("etc", "hosts")
}

func (s *Service) defaultHostAPIBaseURL(runtimeName string) (string, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return "", err
	}
	callbackPort := cfg.Port
	if cfg.AdvertisePort >= 1 && cfg.AdvertisePort <= 65535 {
		callbackPort = cfg.AdvertisePort
	}
	if isLoopbackHostLiteral(cfg.Host) {
		if s != nil {
			if baseURL, ok := s.HostCallbackURL(runtimeName); ok {
				return baseURL, nil
			}
		}
		return "", fmt.Errorf("local child transport is required while swarmd is bound to %q, but no runtime callback URL is active", strings.TrimSpace(cfg.Host))
	}
	return ResolveChildReachableHostAPIBaseURL(runtimeName, callbackPort)
}

func (s *Service) loadStartupConfig() (startupconfig.FileConfig, error) {
	path := strings.TrimSpace(s.startupPath)
	if path == "" {
		var err error
		path, err = startupconfig.ResolvePath()
		if err != nil {
			return startupconfig.FileConfig{}, err
		}
	}
	return startupconfig.Load(path)
}

func HostnameFromBaseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Hostname())
}

func (s *Service) HostCallbackURL(runtimeName string) (string, bool) {
	if s == nil {
		return "", false
	}
	runtimeName = normalizeRuntimeSelection(runtimeName)
	if runtimeName == "" {
		return "", false
	}
	s.hostCallbackURLsMu.RLock()
	defer s.hostCallbackURLsMu.RUnlock()
	value := strings.TrimSpace(s.hostCallbackURLs[runtimeName])
	return value, value != ""
}

func (s *Service) SetHostCallbackURL(runtimeName, baseURL string) {
	if s == nil {
		return
	}
	runtimeName = normalizeRuntimeSelection(runtimeName)
	if runtimeName == "" {
		return
	}
	baseURL = strings.TrimSpace(baseURL)
	s.hostCallbackURLsMu.Lock()
	defer s.hostCallbackURLsMu.Unlock()
	if baseURL == "" {
		delete(s.hostCallbackURLs, runtimeName)
		return
	}
	s.hostCallbackURLs[runtimeName] = baseURL
}

func ResolveChildReachableHostAPIBaseURL(runtimeName string, port int) (string, error) {
	if port < 1 || port > 65535 {
		port = startupconfig.DefaultPort
	}
	host := ChildReachableHostAlias(runtimeName)
	if host == "" {
		return "", fmt.Errorf("runtime %q does not define a child-reachable host api base url; set host_api_base_url explicitly", strings.TrimSpace(runtimeName))
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port))), nil
}

func ResolveChildReachableHostDesktopURL(runtimeName string, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", nil
	}
	host := ChildReachableHostAlias(runtimeName)
	if host == "" {
		return "", fmt.Errorf("runtime %q does not define a child-reachable host desktop url; set host_api_base_url explicitly", strings.TrimSpace(runtimeName))
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port))), nil
}

func isLoopbackHostLiteral(host string) bool {
	host = strings.TrimSpace(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return parsedIP.IsLoopback()
	}
	return false
}

func resolveLocalContainerImage(ctx context.Context, runtimeName, requestedImage string, manifest ContainerPackageManifest) (string, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image := strings.TrimSpace(requestedImage)
	if image == "" {
		image = defaultImageName
	}
	normalizedManifest, err := normalizeContainerPackageManifest(manifest)
	if err != nil {
		return "", err
	}
	if len(normalizedManifest.Packages) == 0 {
		if err := ensureCanonicalImageCurrent(ctx, runtimeName, image); err != nil {
			return "", err
		}
		return image, nil
	}
	if image != defaultImageName {
		return "", fmt.Errorf("local Add Swarm only supports package installation with the default image %q; remove the custom image or clear package selections", defaultImageName)
	}
	derivedImage, err := ensurePackageAwareImageCurrent(ctx, runtimeName, normalizedManifest)
	if err != nil {
		return "", err
	}
	return derivedImage, nil
}

func normalizeContainerPackageManifest(manifest ContainerPackageManifest) (ContainerPackageManifest, error) {
	manifest.BaseImage = strings.TrimSpace(manifest.BaseImage)
	manifest.PackageManager = strings.ToLower(strings.TrimSpace(manifest.PackageManager))
	if manifest.BaseImage == "" {
		manifest.BaseImage = supportedPackageBaseImage
	}
	if manifest.PackageManager == "" {
		manifest.PackageManager = defaultPackageManager
	}
	if manifest.BaseImage != supportedPackageBaseImage {
		return ContainerPackageManifest{}, fmt.Errorf("local Add Swarm package installation currently requires base image %q", supportedPackageBaseImage)
	}
	if manifest.PackageManager != defaultPackageManager {
		return ContainerPackageManifest{}, fmt.Errorf("local Add Swarm package installation currently requires package manager %q", defaultPackageManager)
	}
	if len(manifest.Packages) == 0 {
		manifest.Packages = nil
		return manifest, nil
	}
	out := make([]ContainerPackageSelection, 0, len(manifest.Packages))
	seen := make(map[string]struct{}, len(manifest.Packages))
	for _, pkg := range manifest.Packages {
		name := strings.ToLower(strings.TrimSpace(pkg.Name))
		if name == "" {
			continue
		}
		if !containerPackageNamePattern.MatchString(name) {
			return ContainerPackageManifest{}, fmt.Errorf("local Add Swarm package %q is not a valid apt package identifier", pkg.Name)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, ContainerPackageSelection{
			Name:   name,
			Source: strings.TrimSpace(pkg.Source),
			Reason: strings.TrimSpace(pkg.Reason),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	manifest.Packages = out
	return manifest, nil
}

func ensurePackageAwareImageCurrent(ctx context.Context, runtimeName string, manifest ContainerPackageManifest) (string, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	if runtimeName == "" {
		return "", fmt.Errorf("runtime name is required")
	}
	manifest, err := normalizeContainerPackageManifest(manifest)
	if err != nil {
		return "", err
	}
	if len(manifest.Packages) == 0 {
		if err := ensureCanonicalImageCurrent(ctx, runtimeName, defaultImageName); err != nil {
			return "", err
		}
		return defaultImageName, nil
	}
	repoRoot, _, err := resolveCanonicalRebuildScript()
	if err != nil {
		return "", fmt.Errorf("local Add Swarm packages require a source checkout with deploy/container-mvp assets available: %w", err)
	}
	signature := packageAwareImageSignature(manifest)
	image := fmt.Sprintf("localhost/swarm-container-mvp:pkg-%s", signature)
	exists, err := runtimeImageExists(ctx, runtimeName, image)
	if err != nil {
		return "", fmt.Errorf("check local package image %q: %w", image, err)
	}
	if exists {
		return image, nil
	}
	if err := ensureCanonicalImageCurrent(ctx, runtimeName, defaultImageName); err != nil {
		return "", err
	}
	log.Printf("local container create building package-aware image runtime=%q image=%q packages=%d", runtimeName, image, len(manifest.Packages))
	if err := buildPackageAwareImage(ctx, runtimeName, repoRoot, image, manifest); err != nil {
		return "", err
	}
	return image, nil
}

func packageAwareImageSignature(manifest ContainerPackageManifest) string {
	hash := sha256.Sum256([]byte(packageAwareImageSignaturePayload(manifest)))
	return hex.EncodeToString(hash[:])[:16]
}

func packageAwareImageSignaturePayload(manifest ContainerPackageManifest) string {
	parts := make([]string, 0, len(manifest.Packages)+2)
	parts = append(parts, manifest.BaseImage, manifest.PackageManager)
	for _, pkg := range manifest.Packages {
		parts = append(parts, pkg.Name)
	}
	return strings.Join(parts, "\n")
}

func buildPackageAwareImage(ctx context.Context, runtimeName, repoRoot, image string, manifest ContainerPackageManifest) error {
	packageNames := make([]string, 0, len(manifest.Packages))
	for _, pkg := range manifest.Packages {
		packageNames = append(packageNames, pkg.Name)
	}
	installCommand := fmt.Sprintf("apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*", strings.Join(packageNames, " "))
	containerfile := fmt.Sprintf("FROM %s\nRUN %s\n", defaultImageName, installCommand)
	cmd := exec.CommandContext(ctx, runtimeName, "build", "-t", image, "-")
	cmd.Dir = repoRoot
	cmd.Stdin = strings.NewReader(containerfile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("build local package image %q: %s", image, message)
	}
	return nil
}

func ensureCanonicalImageCurrent(ctx context.Context, runtimeName, image string) error {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image = strings.TrimSpace(image)
	if runtimeName == "" || image == "" || image != defaultImageName {
		return nil
	}
	exists, err := runtimeImageExists(ctx, runtimeName, image)
	if err != nil {
		return fmt.Errorf("check local container image %q: %w", image, err)
	}
	if exists {
		return nil
	}
	repoRoot, scriptPath, err := resolveCanonicalRebuildScript()
	if err != nil {
		return fmt.Errorf("default local container image %q is not installed and no source checkout is available to build it; run scripts/rebuild-container.sh from a source checkout or set a custom image", image)
	}
	log.Printf("local container create rebuilding canonical image runtime=%q image=%q script=%q", runtimeName, image, scriptPath)
	cmd := exec.CommandContext(ctx, "bash", scriptPath, "--image-only")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"BUILD_RUNTIME="+runtimeName,
		"IMAGE_NAME="+image,
		"SWARM_REBUILD_REASON=local-container-create",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("rebuild canonical local swarm image %q: %s", image, message)
	}
	log.Printf("local container create rebuilt canonical image runtime=%q image=%q", runtimeName, image)
	return nil
}

func resolveCanonicalRebuildScript() (string, string, error) {
	candidates := make([]string, 0, 8)
	appendCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		candidates = append(candidates, value)
	}
	appendCandidate(os.Getenv("SWARM_ROOT"))
	appendCandidate(os.Getenv("SWARM_GO_ROOT"))
	if webDir := strings.TrimSpace(os.Getenv("SWARM_WEB_DIR")); webDir != "" {
		appendCandidate(filepath.Dir(webDir))
	}
	appendCandidate(os.Getenv("STARTUP_CWD"))
	if wd, err := os.Getwd(); err == nil {
		appendCandidate(wd)
	}
	if exePath, err := os.Executable(); err == nil {
		appendCandidate(filepath.Dir(exePath))
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		root, ok := searchUpForProjectRoot(candidate)
		if !ok {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		scriptPath := filepath.Join(root, "scripts", "rebuild-container.sh")
		if isReadableFile(scriptPath) {
			return root, scriptPath, nil
		}
	}
	return "", "", errors.New("could not locate scripts/rebuild-container.sh from the current runtime environment")
}

func searchUpForProjectRoot(start string) (string, bool) {
	start = strings.TrimSpace(start)
	if start == "" {
		return "", false
	}
	resolved, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	for {
		if isReadableFile(filepath.Join(resolved, "scripts", "rebuild-container.sh")) &&
			isReadableFile(filepath.Join(resolved, "deploy", "container-mvp", "Containerfile")) {
			return resolved, true
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return "", false
		}
		resolved = parent
	}
}

func isReadableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func portFromBaseURL(raw string) (int, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid host api base url: %w", err)
	}
	portText := parsed.Port()
	if portText == "" {
		if parsed.Scheme == "https" {
			return 443, nil
		}
		return 80, nil
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("host api base url must include a valid port")
	}
	return port, nil
}

func ResolveHostPort(hostAPIBaseURL string, requested int) (int, error) {
	if requested > 0 {
		return requirePortPairAvailable(requested)
	}
	return resolveDefaultHostPort(hostAPIBaseURL, nil)
}

func (s *Service) ResolveCreateHostPort(hostAPIBaseURL string, requested int) (int, error) {
	if requested > 0 {
		return ResolveHostPort(hostAPIBaseURL, requested)
	}
	reserved, err := s.runningPublishedHostPorts()
	if err != nil {
		return 0, err
	}
	return resolveDefaultHostPort(hostAPIBaseURL, func(port int) bool {
		_, ok := reserved[port]
		return ok
	})
}

func (s *Service) runningPublishedHostPorts() (map[int]struct{}, error) {
	reserved := map[int]struct{}{}
	if s == nil || s.store == nil {
		return reserved, nil
	}
	records, err := s.store.List(500)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.HostPort < 1 || record.HostPort > 65534 {
			continue
		}
		if strings.TrimSpace(record.Runtime) == "" || strings.TrimSpace(record.ContainerName) == "" {
			continue
		}
		resolved, _ := s.resolveRecord(record)
		if resolved.Status != "running" {
			continue
		}
		reserved[resolved.HostPort] = struct{}{}
	}
	return reserved, nil
}

func resolveDefaultHostPort(hostAPIBaseURL string, managedPairReserved func(int) bool) (int, error) {
	hostPort, err := portFromBaseURL(hostAPIBaseURL)
	if err != nil {
		return 0, err
	}
	for port := hostPort + 1; port <= 65534; port += 2 {
		_, err := requirePortPairAvailable(port)
		if err == nil {
			return port, nil
		}
		if managedPairReserved == nil || !managedPairReserved(port) {
			return 0, err
		}
	}
	return 0, errors.New("no available local port pair remains")
}

func requirePortPairAvailable(port int) (int, error) {
	if port < 1 || port > 65534 {
		return 0, errors.New("requested local port pair is invalid")
	}
	if !isPortAvailable(port) {
		return 0, fmt.Errorf("host port %d is not available", port)
	}
	if !isPortAvailable(port + 1) {
		return 0, fmt.Errorf("desktop host port %d is not available", port+1)
	}
	return port, nil
}

func isPortAvailable(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func suggestedContainerName(name string) string {
	slug := sanitizeSlug(name)
	if slug == "" {
		slug = "swarm"
	}
	return slug
}

func normalizeMounts(mounts []Mount) []Mount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]Mount, 0, len(mounts))
	seen := map[string]struct{}{}
	for _, mount := range mounts {
		mount.SourcePath = strings.TrimSpace(mount.SourcePath)
		mount.TargetPath = strings.TrimSpace(mount.TargetPath)
		mount.WorkspacePath = strings.TrimSpace(mount.WorkspacePath)
		mount.WorkspaceName = strings.TrimSpace(mount.WorkspaceName)
		if mount.SourcePath == "" {
			continue
		}
		if mount.TargetPath == "" {
			mount.TargetPath = filepath.ToSlash(filepath.Join(defaultContainerPath, filepath.Base(mount.SourcePath)))
		}
		if mount.Mode != pebblestore.ContainerMountModeReadOnly {
			mount.Mode = pebblestore.ContainerMountModeReadWrite
		}
		key := strings.ToLower(mount.SourcePath) + "|" + strings.ToLower(mount.TargetPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, mount)
	}
	return out
}

func normalizeEnv(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !strings.Contains(value, "=") {
			continue
		}
		key, _, _ := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		lowerKey := strings.ToLower(key)
		if _, ok := seen[lowerKey]; ok {
			continue
		}
		seen[lowerKey] = struct{}{}
		out = append(out, key+"="+strings.TrimSpace(strings.TrimPrefix(value, key+"=")))
	}
	return out
}

func ensureLocalSwarmRuntimeEnv(values []string) []string {
	if len(values) == 0 {
		return []string{fmt.Sprintf("SWARMD_LISTEN=0.0.0.0:%d", containerBackendPort)}
	}
	out := append([]string(nil), values...)
	for _, value := range out {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "SWARMD_LISTEN") {
			return out
		}
	}
	out = append(out, fmt.Sprintf("SWARMD_LISTEN=0.0.0.0:%d", containerBackendPort))
	return out
}

func normalizeRuntimeMount(input *RuntimeMount) *RuntimeMount {
	if input == nil {
		return nil
	}
	normalized := &RuntimeMount{
		BinDir:     strings.TrimSpace(input.BinDir),
		ToolBinDir: strings.TrimSpace(input.ToolBinDir),
		WebDistDir: strings.TrimSpace(input.WebDistDir),
		FFFLibPath: strings.TrimSpace(input.FFFLibPath),
	}
	if normalized.BinDir == "" && normalized.ToolBinDir == "" && normalized.WebDistDir == "" && normalized.FFFLibPath == "" {
		return nil
	}
	return normalized
}

func containerRuntimeMountResources(runtimeMount *RuntimeMount) ([]Mount, []string, error) {
	if runtimeMount == nil {
		return nil, nil, nil
	}
	mounts := make([]Mount, 0, 4)
	env := make([]string, 0, 5)
	appendMount := func(source, target string, readOnly bool) error {
		source = strings.TrimSpace(source)
		target = strings.TrimSpace(target)
		if source == "" || target == "" {
			return nil
		}
		info, err := os.Stat(source)
		if err != nil {
			return fmt.Errorf("local runtime mount source %q: %w", source, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("local runtime mount source %q must be a directory", source)
		}
		mode := pebblestore.ContainerMountModeReadWrite
		if readOnly {
			mode = pebblestore.ContainerMountModeReadOnly
		}
		mounts = append(mounts, Mount{SourcePath: source, TargetPath: target, Mode: mode})
		return nil
	}
	appendFileParentMount := func(sourceFile, targetDir string) error {
		sourceFile = strings.TrimSpace(sourceFile)
		targetDir = strings.TrimSpace(targetDir)
		if sourceFile == "" || targetDir == "" {
			return nil
		}
		info, err := os.Stat(sourceFile)
		if err != nil {
			return fmt.Errorf("local runtime file mount source %q: %w", sourceFile, err)
		}
		if info.IsDir() {
			return fmt.Errorf("local runtime file mount source %q must be a file", sourceFile)
		}
		mounts = append(mounts, Mount{SourcePath: filepath.Dir(sourceFile), TargetPath: targetDir, Mode: pebblestore.ContainerMountModeReadOnly})
		return nil
	}
	if err := appendMount(runtimeMount.BinDir, "/mnt/swarm/bin", true); err != nil {
		return nil, nil, err
	}
	if runtimeMount.ToolBinDir != "" {
		if _, err := os.Stat(runtimeMount.ToolBinDir); err == nil {
			if err := appendMount(runtimeMount.ToolBinDir, "/mnt/swarm/libexec", true); err != nil {
				return nil, nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("local runtime mount source %q: %w", runtimeMount.ToolBinDir, err)
		}
	}
	if runtimeMount.WebDistDir != "" {
		if _, err := os.Stat(runtimeMount.WebDistDir); err == nil {
			if err := appendMount(runtimeMount.WebDistDir, "/mnt/swarm/share", true); err != nil {
				return nil, nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("local runtime mount source %q: %w", runtimeMount.WebDistDir, err)
		}
	}
	if err := appendFileParentMount(runtimeMount.FFFLibPath, "/mnt/swarm/lib"); err != nil {
		return nil, nil, err
	}
	env = append(env,
		"SWARM_BIN_DIR=/mnt/swarm/bin",
		"SWARM_RUNTIME_BIN=/mnt/swarm/bin/swarmd",
	)
	if runtimeMount.WebDistDir != "" {
		env = append(env, "SWARM_WEB_DIST_DIR=/mnt/swarm/share")
	}
	if runtimeMount.ToolBinDir != "" {
		env = append(env, "SWARM_TOOL_BIN_DIR=/mnt/swarm/libexec")
	}
	if runtimeMount.FFFLibPath != "" {
		env = append(env, "LD_LIBRARY_PATH=/mnt/swarm/lib")
	}
	return mounts, env, nil
}

func CurrentRuntimeMount() *RuntimeMount {
	fffLibPath := filepath.Join("swarmd", "internal", "fff", "lib", fffLibraryPlatformDir(), "libfff_c.so")
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidate := filepath.Join(wd, fffLibPath)
		if _, statErr := os.Stat(candidate); statErr == nil {
			fffLibPath = candidate
		}
	}
	if !filepath.IsAbs(fffLibPath) {
		if repoRoot, _, err := resolveCanonicalRebuildScript(); err == nil {
			fffLibPath = filepath.Join(repoRoot, fffLibPath)
		}
	}
	roots := make([]string, 0, 3)
	appendRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		for _, existing := range roots {
			if existing == root {
				return
			}
		}
		roots = append(roots, root)
	}
	if override := strings.TrimSpace(os.Getenv("SWARM_SHARED_RUNTIME_ROOT")); override != "" {
		appendRoot(override)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		appendRoot(filepath.Join(home, ".local", "share", "swarm"))
	}
	if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
		appendRoot(filepath.Join(dataHome, "swarm"))
	}
	var best *RuntimeMount
	bestScore := -1
	for _, root := range roots {
		candidate := normalizeRuntimeMount(&RuntimeMount{
			BinDir:     filepath.Join(root, "bin"),
			ToolBinDir: filepath.Join(root, "libexec"),
			WebDistDir: filepath.Join(root, "share"),
			FFFLibPath: fffLibPath,
		})
		if candidate == nil {
			continue
		}
		score := 0
		if isReadableFile(filepath.Join(candidate.BinDir, "swarmd")) {
			score += 100
		}
		if isReadableFile(filepath.Join(candidate.WebDistDir, "index.html")) {
			score += 10
		}
		if isReadableFile(filepath.Join(candidate.ToolBinDir, "rebuild")) {
			score += 1
		}
		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}
	if best != nil {
		return best
	}
	return normalizeRuntimeMount(&RuntimeMount{FFFLibPath: fffLibPath})
}

func fffLibraryPlatformDir() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "linux-amd64-gnu"
	default:
		return "linux-amd64-gnu"
	}
}

func normalizeRunArgs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func PrepareRuntimeNetwork(ctx context.Context, runtimeName, networkName string) (RuntimeNetwork, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	if runtimeName == "" {
		return RuntimeNetwork{}, errors.New("runtime name is required")
	}
	networkName = sanitizeSlug(networkName)
	if networkName == "" {
		return RuntimeNetwork{}, errors.New("network name is required")
	}
	if runtimeName == "podman" {
		details, err := ensurePodmanRuntimeNetwork(ctx, networkName)
		if err != nil {
			return RuntimeNetwork{}, err
		}
		return RuntimeNetwork{Runtime: runtimeName, Name: networkName, Gateway: details.Gateway}, nil
	}
	if err := ensureRuntimeNetwork(ctx, runtimeName, networkName); err != nil {
		return RuntimeNetwork{}, err
	}
	return RuntimeNetwork{Runtime: runtimeName, Name: networkName}, nil
}

type podmanNetworkDetails struct {
	Gateway    string
	DNSEnabled bool
}

func ensurePodmanRuntimeNetwork(ctx context.Context, networkName string) (podmanNetworkDetails, error) {
	details, err := inspectPodmanNetwork(ctx, networkName)
	if err == nil {
		if !details.DNSEnabled {
			return details, nil
		}
		attached, listErr := listRuntimeNetworkContainers(ctx, "podman", networkName)
		if listErr != nil {
			return podmanNetworkDetails{}, listErr
		}
		if len(attached) > 0 {
			return podmanNetworkDetails{}, fmt.Errorf("podman network %q still uses embedded DNS, which is unstable on this host; remove attached containers or choose a fresh group network", networkName)
		}
		if err := removeRuntimeNetwork(ctx, "podman", networkName); err != nil {
			return podmanNetworkDetails{}, err
		}
	} else if !isMissingRuntimeNetworkError(err) {
		return podmanNetworkDetails{}, err
	}
	if err := createRuntimeNetwork(ctx, "podman", networkName, true); err != nil {
		return podmanNetworkDetails{}, err
	}
	return inspectPodmanNetwork(ctx, networkName)
}

func inspectPodmanNetwork(ctx context.Context, networkName string) (podmanNetworkDetails, error) {
	inspectCmd := exec.CommandContext(ctx, "podman", "network", "inspect", networkName)
	output, err := inspectCmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return podmanNetworkDetails{}, fmt.Errorf("inspect podman network %q: %s", networkName, message)
	}
	type subnetRecord struct {
		Gateway    string `json:"gateway"`
		GatewayAlt string `json:"Gateway"`
	}
	type networkRecord struct {
		Subnets     []subnetRecord `json:"subnets"`
		SubnetsAlt  []subnetRecord `json:"Subnets"`
		DNSEnabled  bool           `json:"dns_enabled"`
		DNSEnabled2 bool           `json:"DNSEnabled"`
	}
	findDetails := func(records []networkRecord) podmanNetworkDetails {
		for _, record := range records {
			for _, subnet := range append(record.Subnets, record.SubnetsAlt...) {
				gateway := strings.TrimSpace(firstNonEmpty(subnet.Gateway, subnet.GatewayAlt))
				if gateway != "" {
					return podmanNetworkDetails{
						Gateway:    gateway,
						DNSEnabled: record.DNSEnabled || record.DNSEnabled2,
					}
				}
			}
		}
		return podmanNetworkDetails{}
	}
	var records []networkRecord
	if err := json.Unmarshal(output, &records); err == nil {
		if details := findDetails(records); details.Gateway != "" {
			return details, nil
		}
	}
	var single networkRecord
	if err := json.Unmarshal(output, &single); err == nil {
		if details := findDetails([]networkRecord{single}); details.Gateway != "" {
			return details, nil
		}
	}
	return podmanNetworkDetails{}, fmt.Errorf("inspect podman network %q: gateway not found", networkName)
}

func runContainer(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
	args := []string{
		"run",
		"-d",
		"--name", opts.ContainerName,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", opts.HostPort, containerBackendPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", opts.HostPort+1, containerDesktopPort),
	}
	if strings.TrimSpace(opts.NetworkName) != "" {
		args = append(args, "--network", strings.TrimSpace(opts.NetworkName))
	}
	for _, arg := range opts.ExtraRunArgs {
		args = append(args, arg)
	}
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}
	for _, mount := range opts.Mounts {
		args = append(args, "-v", fmt.Sprintf("%s:%s", mount.SourcePath, mount.TargetPath))
	}
	args = append(args, opts.Image)
	cmd := exec.CommandContext(ctx, runtimeName, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("start %s container: %s", runtimeName, message)
	}
	return strings.TrimSpace(string(output)), nil
}

func ensureRuntimeNetwork(ctx context.Context, runtimeName, networkName string) error {
	networkName = sanitizeSlug(networkName)
	if networkName == "" {
		return errors.New("network name is required")
	}
	inspectCmd := exec.CommandContext(ctx, runtimeName, "network", "inspect", networkName)
	if _, err := inspectCmd.CombinedOutput(); err == nil {
		return nil
	}
	return createRuntimeNetwork(ctx, runtimeName, networkName, false)
}

func createRuntimeNetwork(ctx context.Context, runtimeName, networkName string, disableDNS bool) error {
	args := []string{"network", "create"}
	if runtimeName == "podman" && disableDNS {
		args = append(args, "--disable-dns")
	}
	args = append(args, networkName)
	createCmd := exec.CommandContext(ctx, runtimeName, args...)
	output, err := createCmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("create %s network %q: %s", runtimeName, networkName, message)
	}
	return nil
}

func removeRuntimeNetwork(ctx context.Context, runtimeName, networkName string) error {
	cmd := exec.CommandContext(ctx, runtimeName, "network", "rm", networkName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("remove %s network %q: %s", runtimeName, networkName, message)
	}
	return nil
}

func listRuntimeNetworkContainers(ctx context.Context, runtimeName, networkName string) ([]string, error) {
	cmd := exec.CommandContext(ctx, runtimeName, "ps", "-a", "--filter", "network="+networkName, "--format", "{{.Names}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("list %s network %q containers: %s", runtimeName, networkName, message)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	containers := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		containers = append(containers, line)
	}
	return containers, nil
}

func isMissingRuntimeNetworkError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "network not found") || strings.Contains(message, "unable to find network")
}

func controlContainer(ctx context.Context, runtimeName, action, containerName string) error {
	cmd := exec.CommandContext(ctx, runtimeName, action, containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s %s container: %s", action, runtimeName, message)
	}
	return nil
}

func inspectContainer(runtimeName, containerName string) (string, string, error) {
	cmd := exec.Command(runtimeName, "inspect", "--format", "{{.State.Status}}|{{.Id}}", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "missing", "", fmt.Errorf("inspect %s container: %s", runtimeName, message)
	}
	parts := strings.SplitN(strings.TrimSpace(string(output)), "|", 2)
	status := strings.TrimSpace(parts[0])
	containerID := ""
	if len(parts) == 2 {
		containerID = strings.TrimSpace(parts[1])
	}
	switch status {
	case "running":
		return "running", containerID, nil
	case "exited", "stopped":
		return "exited", containerID, nil
	default:
		return "created", containerID, nil
	}
}

func sanitizeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func mapRecord(record pebblestore.SwarmLocalContainerRecord) Container {
	return Container{
		ID:             record.ID,
		Name:           record.Name,
		ContainerName:  record.ContainerName,
		Runtime:        record.Runtime,
		NetworkName:    record.NetworkName,
		Status:         record.Status,
		ContainerID:    record.ContainerID,
		HostAPIBaseURL: record.HostAPIBaseURL,
		HostPort:       record.HostPort,
		RuntimePort:    record.RuntimePort,
		Image:          record.Image,
		Warning:        record.Warning,
		Mounts:         append([]Mount(nil), record.Mounts...),
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}
}
