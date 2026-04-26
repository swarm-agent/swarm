package localcontainers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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

	"swarm-refactor/swarmtui/pkg/buildinfo"
	"swarm-refactor/swarmtui/pkg/devmode"
	"swarm-refactor/swarmtui/pkg/localupdate"
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
	PathContainerUpdatePlan   = "swarm.containers.local.update-plan.v1"
	defaultImageName          = devmode.DefaultContainerImageRef
	ProductionImagePrefix     = "ghcr.io/swarm-agent/swarm"
	OfficialSourceRepository  = "https://github.com/swarm-agent/swarm"
	OfficialImageContract     = "swarm.container.v1"
	productionImagePrefix     = ProductionImagePrefix
	officialSourceRepository  = OfficialSourceRepository
	officialImageContract     = OfficialImageContract
	defaultContainerPath      = "/workspaces"
	containerBackendPort      = startupconfig.DefaultPort
	containerDesktopPort      = startupconfig.DefaultDesktopPort
	defaultPackageManager     = "apt"
	supportedPackageBaseImage = "ubuntu:24.04"
)

var containerPackageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

var (
	productionImageMetadataClient = http.DefaultClient
	productionMetadataURLTmpl     = "https://github.com/swarm-agent/swarm/releases/download/%s/container-image-info.txt"
)

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

// UpdatePlan is a local-only inventory for future Swarm update checkpoints.
// It is intentionally read-only: Checkpoint 1 reports which local containers
// would be affected by a dev/prod Swarm update without replacing containers.
// Swarm binary/apply updates remain independent; future container update
// failures should be reported as resumable follow-up work instead of hiding or
// rolling back the Swarm update itself.
type UpdatePlan struct {
	PathID        string           `json:"path_id"`
	Mode          string           `json:"mode"`
	DevMode       bool             `json:"dev_mode"`
	Target        UpdatePlanTarget `json:"target"`
	Summary       UpdateSummary    `json:"summary"`
	Containers    []UpdateItem     `json:"containers"`
	Contract      UpdateContract   `json:"contract"`
	Error         string           `json:"error,omitempty"`
	CheckedAtUnix int64            `json:"checked_at_unix_ms,omitempty"`
}

type UpdatePlanTarget struct {
	ImageRef               string `json:"image_ref,omitempty"`
	DigestRef              string `json:"digest_ref,omitempty"`
	Version                string `json:"version,omitempty"`
	Fingerprint            string `json:"fingerprint,omitempty"`
	PostRebuildImageRef    string `json:"post_rebuild_image_ref,omitempty"`
	PostRebuildFingerprint string `json:"post_rebuild_fingerprint,omitempty"`
	Commit                 string `json:"commit,omitempty"`
}

type UpdateSummary struct {
	Total          int `json:"total"`
	Affected       int `json:"affected"`
	AlreadyCurrent int `json:"already_current"`
	NeedsUpdate    int `json:"needs_update"`
	Unknown        int `json:"unknown"`
	Errors         int `json:"errors"`
}

type UpdateContract struct {
	WarningCopy      string `json:"warning_copy"`
	DismissalScope   string `json:"dismissal_scope"`
	FailureSemantics string `json:"failure_semantics"`
	Replacement      string `json:"replacement"`
}

type UpdateItem struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	ContainerName      string            `json:"container_name"`
	Runtime            string            `json:"runtime"`
	Status             string            `json:"status,omitempty"`
	ContainerID        string            `json:"container_id,omitempty"`
	StoredImageRef     string            `json:"stored_image_ref,omitempty"`
	CurrentImageRef    string            `json:"current_image_ref,omitempty"`
	CurrentDigestRef   string            `json:"current_digest_ref,omitempty"`
	CurrentFingerprint string            `json:"current_fingerprint,omitempty"`
	TargetImageRef     string            `json:"target_image_ref,omitempty"`
	TargetDigestRef    string            `json:"target_digest_ref,omitempty"`
	TargetVersion      string            `json:"target_version,omitempty"`
	TargetFingerprint  string            `json:"target_fingerprint,omitempty"`
	State              string            `json:"state"`
	Reason             string            `json:"reason,omitempty"`
	Error              string            `json:"error,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type UpdatePlanInput struct {
	DevMode          *bool
	TargetVersion    string
	PostRebuildCheck bool
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
	dataDir            string
	inspectContainerFn func(string, string) (string, string, error)
	inspectImageFn     func(context.Context, string, string) (runtimeImageInfo, error)
	hostCallbackURLsMu sync.RWMutex
	hostCallbackURLs   map[string]string
}

func NewService(store *pebblestore.SwarmLocalContainerStore, deployments *pebblestore.DeployContainerStore, swarmStore *pebblestore.SwarmStore, authStore *pebblestore.AuthStore, workspaceSvc *workspaceruntime.Service, startupPath string) *Service {
	return NewServiceWithDataDir(store, deployments, swarmStore, authStore, workspaceSvc, startupPath, "")
}

func NewServiceWithDataDir(store *pebblestore.SwarmLocalContainerStore, deployments *pebblestore.DeployContainerStore, swarmStore *pebblestore.SwarmStore, authStore *pebblestore.AuthStore, workspaceSvc *workspaceruntime.Service, startupPath, dataDir string) *Service {
	return &Service{
		store:              store,
		deployments:        deployments,
		swarmStore:         swarmStore,
		authStore:          authStore,
		workspace:          workspaceSvc,
		startupPath:        strings.TrimSpace(startupPath),
		dataDir:            strings.TrimSpace(dataDir),
		inspectContainerFn: inspectContainer,
		inspectImageFn:     inspectRuntimeImage,
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

func (s *Service) UpdatePlan(ctx context.Context, input UpdatePlanInput) (UpdatePlan, error) {
	plan := UpdatePlan{
		PathID:        PathContainerUpdatePlan,
		Contract:      localUpdateContract(),
		Containers:    []UpdateItem{},
		CheckedAtUnix: time.Now().UnixMilli(),
	}
	if s == nil || s.store == nil {
		return plan, errors.New("local container service is not configured")
	}
	startupCfg, cfgErr := s.loadStartupConfig()
	devMode := false
	if cfgErr == nil {
		devMode = startupCfg.DevMode
	}
	if input.DevMode != nil {
		devMode = *input.DevMode
		startupCfg.DevMode = devMode
	}
	plan.DevMode = devMode
	if devMode {
		plan.Mode = "dev"
	} else {
		plan.Mode = "release"
	}
	records, err := s.store.List(500)
	if err != nil {
		return plan, err
	}
	if len(records) == 0 {
		if cfgErr != nil {
			plan.Error = cfgErr.Error()
		}
		return plan, nil
	}
	if cfgErr != nil {
		plan.Error = cfgErr.Error()
		for _, record := range records {
			item := baseUpdateItem(record)
			item.State = "unknown"
			item.Reason = "startup_config_error"
			item.Error = cfgErr.Error()
			plan.addUpdateItem(item)
		}
		return plan, nil
	}
	target, targetErr := localUpdateTarget(ctx, startupCfg, strings.TrimSpace(input.TargetVersion))
	if targetErr != nil {
		plan.Error = targetErr.Error()
	}
	if input.PostRebuildCheck && startupCfg.DevMode && targetErr == nil {
		postTarget, postErr := s.localDevPostRebuildTarget(ctx, target, records)
		if postErr != nil {
			if plan.Error == "" {
				plan.Error = postErr.Error()
			}
		} else {
			if strings.TrimSpace(postTarget.PostRebuildImageRef) != "" {
				target.PostRebuildImageRef = postTarget.PostRebuildImageRef
			}
			if strings.TrimSpace(postTarget.PostRebuildFingerprint) != "" {
				target.PostRebuildFingerprint = postTarget.PostRebuildFingerprint
			}
		}
	}
	plan.Target = target
	for _, record := range records {
		item := s.planLocalContainerUpdate(ctx, record, startupCfg, target, targetErr)
		plan.addUpdateItem(item)
	}
	return plan, nil
}

func localUpdateContract() UpdateContract {
	return UpdateContract{
		WarningCopy:      "This will also update your local containers.",
		DismissalScope:   "local-container-update-warning",
		FailureSemantics: "Swarm update succeeds independently; local container update failures are reported as resumable follow-up work.",
		Replacement:      "not-performed-in-this-checkpoint",
	}
}

func localUpdateTarget(ctx context.Context, startupCfg startupconfig.FileConfig, targetVersion string) (UpdatePlanTarget, error) {
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		targetVersion = buildinfo.DisplayVersion()
	}
	if startupCfg.DevMode {
		repoRoot, err := resolveConfiguredDevRoot(startupCfg)
		if err != nil {
			return UpdatePlanTarget{}, err
		}
		fingerprint, err := devmode.ContainerImageFingerprint(repoRoot)
		if err != nil {
			return UpdatePlanTarget{}, fmt.Errorf("compute dev local container image fingerprint: %w", err)
		}
		return UpdatePlanTarget{ImageRef: defaultImageName, Fingerprint: fingerprint}, nil
	}
	metadata, err := fetchProductionImageMetadataForVersion(ctx, targetVersion)
	if err != nil {
		return UpdatePlanTarget{}, fmt.Errorf("fetch production swarm image metadata: %w", err)
	}
	return UpdatePlanTarget{
		ImageRef:  metadata.ImageRef,
		DigestRef: metadata.ImageDigestRef,
		Version:   metadata.Version,
		Commit:    metadata.Commit,
	}, nil
}

func (s *Service) localDevPostRebuildTarget(ctx context.Context, target UpdatePlanTarget, records []pebblestore.SwarmLocalContainerRecord) (UpdatePlanTarget, error) {
	if s != nil && strings.TrimSpace(s.dataDir) != "" {
		status, ok, err := localupdate.ReadRebuildStatusPath(localupdate.RebuildStatusPath(s.dataDir))
		if err != nil {
			return UpdatePlanTarget{PostRebuildImageRef: defaultImageName}, fmt.Errorf("read local container rebuild status: %w", err)
		}
		if ok && strings.EqualFold(status.Mode, "dev") && strings.TrimSpace(status.Fingerprint) == strings.TrimSpace(target.Fingerprint) {
			return UpdatePlanTarget{
				PostRebuildImageRef:    firstNonEmpty(strings.TrimSpace(status.ImageRef), defaultImageName),
				PostRebuildFingerprint: strings.TrimSpace(status.Fingerprint),
			}, nil
		}
	}
	inspector := inspectRuntimeImage
	if s != nil && s.inspectImageFn != nil {
		inspector = s.inspectImageFn
	}
	for _, record := range records {
		runtimeName := normalizeRuntimeSelection(record.Runtime)
		if runtimeName == "" {
			continue
		}
		imageInfo, err := inspector(ctx, runtimeName, defaultImageName)
		if err != nil {
			return UpdatePlanTarget{PostRebuildImageRef: defaultImageName}, err
		}
		fingerprint := strings.TrimSpace(imageInfo.Labels[devmode.ContainerImageFingerprintLabel])
		if fingerprint != "" && fingerprint == strings.TrimSpace(target.Fingerprint) {
			return UpdatePlanTarget{
				PostRebuildImageRef:    defaultImageName,
				PostRebuildFingerprint: fingerprint,
			}, nil
		}
		return UpdatePlanTarget{}, nil
	}
	return UpdatePlanTarget{}, nil
}

func (s *Service) planLocalContainerUpdate(ctx context.Context, record pebblestore.SwarmLocalContainerRecord, startupCfg startupconfig.FileConfig, target UpdatePlanTarget, targetErr error) UpdateItem {
	item := baseUpdateItem(record)
	resolved, resolveErr := s.resolveRecord(record)
	item.Status = resolved.Status
	item.ContainerID = strings.TrimSpace(resolved.ContainerID)
	item.StoredImageRef = strings.TrimSpace(resolved.Image)
	if item.StoredImageRef == "" {
		item.StoredImageRef = strings.TrimSpace(record.Image)
	}
	if resolveErr != nil {
		item.State = "unknown"
		item.Reason = "container_inspect_error"
		item.Error = resolveErr.Error()
		return item
	}
	if targetErr != nil {
		item.State = "unknown"
		item.Reason = "target_error"
		item.Error = targetErr.Error()
		return item
	}
	item.TargetImageRef = firstNonEmpty(target.PostRebuildImageRef, target.ImageRef)
	item.TargetDigestRef = target.DigestRef
	item.TargetVersion = target.Version
	item.TargetFingerprint = firstNonEmpty(target.PostRebuildFingerprint, target.Fingerprint)
	imageRef := strings.TrimSpace(firstNonEmpty(resolved.Image, record.Image))
	if imageRef == "" {
		item.State = "unknown"
		item.Reason = "missing_stored_image"
		return item
	}
	inspector := inspectRuntimeImage
	if s != nil && s.inspectImageFn != nil {
		inspector = s.inspectImageFn
	}
	imageInfo, imageErr := inspector(ctx, resolved.Runtime, imageRef)
	if imageErr != nil {
		item.State = "unknown"
		item.Reason = "image_inspect_error"
		item.Error = imageErr.Error()
		return item
	}
	item.CurrentImageRef = imageInfo.ID
	item.CurrentDigestRef = imageInfo.digestRefFor(imageRef)
	item.Labels = imageInfo.Labels
	if startupCfg.DevMode {
		item.CurrentFingerprint = strings.TrimSpace(imageInfo.Labels[devmode.ContainerImageFingerprintLabel])
		if isPackageAwareLocalImage(imageRef, imageInfo.Labels) {
			item.State = "unknown"
			item.Reason = "package_aware_deferred"
			return item
		}
		if item.CurrentFingerprint == "" {
			item.State = "unknown"
			item.Reason = "missing_dev_fingerprint"
			return item
		}
		targetFingerprint := strings.TrimSpace(firstNonEmpty(target.PostRebuildFingerprint, target.Fingerprint))
		if targetFingerprint == "" {
			item.State = "unknown"
			item.Reason = "missing_target_dev_fingerprint"
			return item
		}
		if item.CurrentFingerprint == targetFingerprint {
			item.State = "already-current"
		} else {
			item.State = "needs-update"
		}
		return item
	}
	if item.CurrentDigestRef == "" {
		item.State = "unknown"
		item.Reason = "missing_digest"
		return item
	}
	if item.CurrentDigestRef == target.DigestRef || imageRef == target.DigestRef {
		item.State = "already-current"
	} else {
		item.State = "needs-update"
	}
	return item
}

func isPackageAwareLocalImage(imageRef string, labels map[string]string) bool {
	if strings.Contains(strings.TrimSpace(imageRef), ":pkg-") {
		return true
	}
	if strings.TrimSpace(labels[devmode.ContainerImageBaseFingerprintLabel]) != "" {
		return true
	}
	return false
}

func baseUpdateItem(record pebblestore.SwarmLocalContainerRecord) UpdateItem {
	return UpdateItem{
		ID:             strings.TrimSpace(record.ID),
		Name:           strings.TrimSpace(record.Name),
		ContainerName:  strings.TrimSpace(record.ContainerName),
		Runtime:        strings.TrimSpace(record.Runtime),
		Status:         strings.TrimSpace(record.Status),
		ContainerID:    strings.TrimSpace(record.ContainerID),
		StoredImageRef: strings.TrimSpace(record.Image),
		State:          "unknown",
	}
}

func (p *UpdatePlan) addUpdateItem(item UpdateItem) {
	if p == nil {
		return
	}
	p.Containers = append(p.Containers, item)
	p.Summary.Total++
	switch item.State {
	case "already-current":
		p.Summary.AlreadyCurrent++
	case "needs-update":
		p.Summary.NeedsUpdate++
		p.Summary.Affected++
	default:
		p.Summary.Unknown++
	}
	if strings.TrimSpace(item.Error) != "" {
		p.Summary.Errors++
	}
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
	startupCfg, err := s.loadStartupConfig()
	if err != nil {
		return Container{}, err
	}
	hostAPIBaseURL := strings.TrimSpace(input.HostAPIBaseURL)
	if hostAPIBaseURL == "" {
		hostAPIBaseURL, err = s.defaultHostAPIBaseURLFromConfig(runtimeName, startupCfg)
		if err != nil {
			return Container{}, err
		}
	}
	hostPort, err := s.ResolveCreateHostPort(hostAPIBaseURL, input.HostPort)
	if err != nil {
		return Container{}, err
	}
	image, err := resolveLocalContainerImage(ctx, runtimeName, strings.TrimSpace(input.Image), input.ContainerPackages, startupCfg)
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

func runtimePullImage(parent context.Context, runtimeName, image string) error {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image = strings.TrimSpace(image)
	if runtimeName == "" || image == "" {
		return fmt.Errorf("runtime and image are required")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtimeName, "pull", image)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("image pull timed out")
	}
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return errors.New(message)
}

type runtimeImageInfo struct {
	ID          string
	RepoDigests []string
	Labels      map[string]string
}

func (info runtimeImageInfo) digestRefFor(imageRef string) string {
	imageRef = strings.TrimSpace(imageRef)
	if strings.Contains(imageRef, "@sha256:") {
		return imageRef
	}
	imageName := strings.TrimSpace(imageRef)
	if idx := strings.LastIndex(imageName, ":"); idx > strings.LastIndex(imageName, "/") {
		imageName = imageName[:idx]
	}
	for _, digest := range info.RepoDigests {
		digest = strings.TrimSpace(digest)
		if digest == "" || digest == "<none>" {
			continue
		}
		if imageName == "" || strings.HasPrefix(digest, imageName+"@") {
			return digest
		}
	}
	for _, digest := range info.RepoDigests {
		digest = strings.TrimSpace(digest)
		if digest != "" && digest != "<none>" {
			return digest
		}
	}
	return ""
}

func inspectRuntimeImage(parent context.Context, runtimeName, image string) (runtimeImageInfo, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image = strings.TrimSpace(image)
	if runtimeName == "" || image == "" {
		return runtimeImageInfo{}, fmt.Errorf("runtime and image are required")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtimeName, "image", "inspect", image)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return runtimeImageInfo{}, fmt.Errorf("image inspect timed out")
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		if idx := strings.IndexAny(message, "\r\n"); idx >= 0 {
			message = strings.TrimSpace(message[:idx])
		}
		return runtimeImageInfo{}, errors.New(message)
	}
	return parseRuntimeImageInspect(output)
}

func parseRuntimeImageInspect(output []byte) (runtimeImageInfo, error) {
	var entries []struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
		Config      struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(output, &entries); err != nil {
		return runtimeImageInfo{}, fmt.Errorf("decode image inspect: %w", err)
	}
	if len(entries) == 0 {
		return runtimeImageInfo{}, errors.New("image inspect returned no records")
	}
	labels := map[string]string{}
	for key, value := range entries[0].Config.Labels {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			labels[key] = value
		}
	}
	return runtimeImageInfo{ID: strings.TrimSpace(entries[0].ID), RepoDigests: append([]string(nil), entries[0].RepoDigests...), Labels: labels}, nil
}

func runtimeImageLabel(parent context.Context, runtimeName, image, label string) (string, bool, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image = strings.TrimSpace(image)
	label = strings.TrimSpace(label)
	if runtimeName == "" || image == "" || label == "" {
		return "", false, fmt.Errorf("runtime, image, and label are required")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	args := []string{"image", "inspect", image, "--format", fmt.Sprintf("{{ index .Config.Labels %q }}", label)}
	cmd := exec.CommandContext(ctx, runtimeName, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", false, fmt.Errorf("image inspect timed out")
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		if idx := strings.IndexAny(message, "\r\n"); idx >= 0 {
			message = strings.TrimSpace(message[:idx])
		}
		return "", false, errors.New(message)
	}
	value := strings.TrimSpace(string(output))
	if value == "" || value == "<no value>" {
		return "", false, nil
	}
	return value, true, nil
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
	return s.defaultHostAPIBaseURLFromConfig(runtimeName, cfg)
}

func (s *Service) defaultHostAPIBaseURLFromConfig(runtimeName string, cfg startupconfig.FileConfig) (string, error) {
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

func resolveLocalContainerImage(ctx context.Context, runtimeName, requestedImage string, manifest ContainerPackageManifest, startupCfg startupconfig.FileConfig) (string, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image := strings.TrimSpace(requestedImage)
	if startupCfg.DevMode {
		if image == "" {
			image = defaultImageName
		}
		normalizedManifest, err := normalizeContainerPackageManifest(manifest)
		if err != nil {
			return "", err
		}
		if len(normalizedManifest.Packages) == 0 {
			if _, err := ensureCanonicalImageCurrent(ctx, runtimeName, image, startupCfg); err != nil {
				return "", err
			}
			return image, nil
		}
		if image != defaultImageName {
			return "", fmt.Errorf("local Add Swarm only supports package installation with the default image %q; remove the custom image or clear package selections", defaultImageName)
		}
		derivedImage, err := ensurePackageAwareImageCurrent(ctx, runtimeName, normalizedManifest, startupCfg)
		if err != nil {
			return "", err
		}
		return derivedImage, nil
	}

	if image != "" && image != defaultImageName {
		if _, err := ensureCanonicalImageCurrent(ctx, runtimeName, image, startupCfg); err != nil {
			return "", err
		}
		return image, nil
	}
	prodImage, err := ensureProductionImageCurrent(ctx, runtimeName)
	if err != nil {
		return "", err
	}
	return prodImage, nil
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

type ProductionImageMetadata struct {
	ImageRef       string
	ImageDigestRef string
	Version        string
	Commit         string
	SourceRevision string
	ImageSizeBytes int64
}

type productionImageMetadata = ProductionImageMetadata

func productionImageRef() (string, error) {
	version := strings.TrimSpace(buildinfo.DisplayVersion())
	if buildinfo.IsDevVersionString(version) {
		return "", fmt.Errorf("production local Add Swarm requires an installed release version, got %q", version)
	}
	return productionImagePrefix + ":" + version, nil
}

func productionImageMetadataURL(version string) string {
	return fmt.Sprintf(productionMetadataURLTmpl, url.PathEscape(strings.TrimSpace(version)))
}

func FetchProductionImageMetadata(ctx context.Context) (ProductionImageMetadata, error) {
	version := strings.TrimSpace(buildinfo.DisplayVersion())
	return fetchProductionImageMetadataForVersion(ctx, version)
}

func fetchProductionImageMetadataForVersion(ctx context.Context, version string) (ProductionImageMetadata, error) {
	version = strings.TrimSpace(version)
	if buildinfo.IsDevVersionString(version) {
		return ProductionImageMetadata{}, fmt.Errorf("production container image requires an installed release version, got %q", version)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, productionImageMetadataURL(version), nil)
	if err != nil {
		return ProductionImageMetadata{}, err
	}
	client := productionImageMetadataClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return ProductionImageMetadata{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return ProductionImageMetadata{}, fmt.Errorf("release image metadata returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return ProductionImageMetadata{}, err
	}
	fields := parseProductionImageMetadata(string(body))
	metadata := ProductionImageMetadata{
		ImageRef:       strings.TrimSpace(fields["image_ref"]),
		ImageDigestRef: strings.TrimSpace(fields["image_digest_ref"]),
		Version:        strings.TrimSpace(fields["version"]),
		Commit:         strings.TrimSpace(fields["commit"]),
		SourceRevision: strings.TrimSpace(fields["source_revision"]),
	}
	if rawSize := strings.TrimSpace(fields["image_size_bytes"]); rawSize != "" {
		imageSize, err := strconv.ParseInt(rawSize, 10, 64)
		if err != nil || imageSize < 0 {
			return ProductionImageMetadata{}, fmt.Errorf("release image metadata image_size_bytes is invalid: %q", rawSize)
		}
		metadata.ImageSizeBytes = imageSize
	}
	if metadata.Version != version {
		return ProductionImageMetadata{}, fmt.Errorf("release image metadata version mismatch: %q, expected %q", metadata.Version, version)
	}
	expectedRef := ProductionImagePrefix + ":" + version
	if metadata.ImageRef != expectedRef {
		return ProductionImageMetadata{}, fmt.Errorf("release image metadata image_ref mismatch: %q, expected %q", metadata.ImageRef, expectedRef)
	}
	if !strings.HasPrefix(metadata.ImageDigestRef, ProductionImagePrefix+"@sha256:") {
		return ProductionImageMetadata{}, fmt.Errorf("release image metadata missing official image digest for %q", ProductionImagePrefix)
	}
	return metadata, nil
}

func fetchProductionImageMetadata(ctx context.Context) (productionImageMetadata, error) {
	return FetchProductionImageMetadata(ctx)
}

func parseProductionImageMetadata(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func ensureProductionImageCurrent(ctx context.Context, runtimeName string) (string, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	if runtimeName == "" {
		return "", fmt.Errorf("runtime name is required")
	}
	metadata, err := fetchProductionImageMetadata(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch production swarm image metadata: %w", err)
	}
	image := strings.TrimSpace(metadata.ImageDigestRef)
	exists, err := runtimeImageExists(ctx, runtimeName, image)
	if err != nil {
		return "", fmt.Errorf("check production swarm image %q: %w", image, err)
	}
	if !exists {
		log.Printf("local container create pulling production image runtime=%q image=%q", runtimeName, image)
		if err := runtimePullImage(ctx, runtimeName, image); err != nil {
			return "", fmt.Errorf("pull production swarm image %q: %w", image, err)
		}
	}
	if err := verifyProductionImageLabels(ctx, runtimeName, image); err != nil {
		return "", err
	}
	return image, nil
}

func verifyProductionImageLabels(ctx context.Context, runtimeName, image string) error {
	expectedVersion := strings.TrimSpace(buildinfo.DisplayVersion())
	expectedCommit := strings.TrimSpace(buildinfo.DisplayCommit())
	checks := []struct {
		label    string
		expected string
	}{
		{label: "org.opencontainers.image.source", expected: officialSourceRepository},
		{label: "org.opencontainers.image.version", expected: expectedVersion},
		{label: "swarmagent.image.contract", expected: officialImageContract},
		{label: "swarmagent.image.role", expected: "app"},
		{label: "swarmagent.version", expected: expectedVersion},
	}
	if expectedCommit != "" && expectedCommit != "unknown" {
		checks = append(checks,
			struct {
				label    string
				expected string
			}{label: "org.opencontainers.image.revision", expected: expectedCommit},
			struct {
				label    string
				expected string
			}{label: "swarmagent.commit", expected: expectedCommit},
		)
	}
	for _, check := range checks {
		value, found, err := runtimeImageLabel(ctx, runtimeName, image, check.label)
		if err != nil {
			return fmt.Errorf("inspect production swarm image %q label %s: %w", image, check.label, err)
		}
		if !found || strings.TrimSpace(value) != check.expected {
			return fmt.Errorf("production swarm image %q failed verification: label %s=%q, expected %q", image, check.label, strings.TrimSpace(value), check.expected)
		}
	}
	return nil
}

func ensurePackageAwareImageCurrent(ctx context.Context, runtimeName string, manifest ContainerPackageManifest, startupCfg startupconfig.FileConfig) (string, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	if runtimeName == "" {
		return "", fmt.Errorf("runtime name is required")
	}
	manifest, err := normalizeContainerPackageManifest(manifest)
	if err != nil {
		return "", err
	}
	if len(manifest.Packages) == 0 {
		if _, err := ensureCanonicalImageCurrent(ctx, runtimeName, defaultImageName, startupCfg); err != nil {
			return "", err
		}
		return defaultImageName, nil
	}
	if !startupCfg.DevMode {
		return "", fmt.Errorf("local Add Swarm package installation requires dev_mode = true in swarm.conf; production mode only supports the prepared canonical image %q", defaultImageName)
	}
	repoRoot, err := resolveConfiguredDevRoot(startupCfg)
	if err != nil {
		return "", err
	}
	baseSignature, err := ensureCanonicalImageCurrent(ctx, runtimeName, defaultImageName, startupCfg)
	if err != nil {
		return "", err
	}
	signature := packageAwareImageSignature(manifest, baseSignature)
	image := fmt.Sprintf("localhost/swarm-container-mvp:pkg-%s", signature)
	exists, err := runtimeImageExists(ctx, runtimeName, image)
	if err != nil {
		return "", fmt.Errorf("check local package image %q: %w", image, err)
	}
	if exists {
		return image, nil
	}
	log.Printf("local container create building package-aware image runtime=%q image=%q packages=%d", runtimeName, image, len(manifest.Packages))
	if err := buildPackageAwareImage(ctx, runtimeName, repoRoot, image, manifest, baseSignature); err != nil {
		return "", err
	}
	return image, nil
}

func packageAwareImageSignature(manifest ContainerPackageManifest, baseSignature string) string {
	hash := sha256.Sum256([]byte(packageAwareImageSignaturePayload(manifest, baseSignature)))
	return hex.EncodeToString(hash[:])[:16]
}

func packageAwareImageSignaturePayload(manifest ContainerPackageManifest, baseSignature string) string {
	parts := make([]string, 0, len(manifest.Packages)+3)
	parts = append(parts, strings.TrimSpace(baseSignature), manifest.BaseImage, manifest.PackageManager)
	for _, pkg := range manifest.Packages {
		parts = append(parts, pkg.Name)
	}
	return strings.Join(parts, "\n")
}

func buildPackageAwareImage(ctx context.Context, runtimeName, repoRoot, image string, manifest ContainerPackageManifest, baseSignature string) error {
	packageNames := make([]string, 0, len(manifest.Packages))
	for _, pkg := range manifest.Packages {
		packageNames = append(packageNames, pkg.Name)
	}
	installCommand := fmt.Sprintf("apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*", strings.Join(packageNames, " "))
	containerfile := fmt.Sprintf("FROM %s\nRUN %s\n", defaultImageName, installCommand)
	args := []string{"build", "-t", image}
	if strings.TrimSpace(baseSignature) != "" {
		args = append(args,
			"--label", devmode.ContainerImageDevModeLabel+"=true",
			"--label", devmode.ContainerImageBaseFingerprintLabel+"="+strings.TrimSpace(baseSignature),
		)
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, runtimeName, args...)
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

func ensureCanonicalImageCurrent(ctx context.Context, runtimeName, image string, startupCfg startupconfig.FileConfig) (string, error) {
	runtimeName = normalizeRuntimeSelection(runtimeName)
	image = strings.TrimSpace(image)
	if runtimeName == "" || image == "" || image != defaultImageName {
		return "", nil
	}
	exists, err := runtimeImageExists(ctx, runtimeName, image)
	if err != nil {
		return "", fmt.Errorf("check local container image %q: %w", image, err)
	}
	if !startupCfg.DevMode {
		if exists {
			return "", nil
		}
		return "", fmt.Errorf("default local container image %q is not installed; production mode will not rebuild from source, so prebuild or install the canonical image first", image)
	}
	repoRoot, err := resolveConfiguredDevRoot(startupCfg)
	if err != nil {
		return "", err
	}
	expectedFingerprint, err := devmode.ContainerImageFingerprint(repoRoot)
	if err != nil {
		return "", fmt.Errorf("compute dev local container image fingerprint: %w", err)
	}
	if exists {
		currentFingerprint, found, err := runtimeImageLabel(ctx, runtimeName, image, devmode.ContainerImageFingerprintLabel)
		if err != nil {
			return "", fmt.Errorf("inspect local container image %q: %w", image, err)
		}
		if found && strings.TrimSpace(currentFingerprint) == expectedFingerprint {
			return expectedFingerprint, nil
		}
		log.Printf("local container create rebuilding stale dev image runtime=%q image=%q expected_fingerprint=%q current_fingerprint=%q found_label=%t", runtimeName, image, expectedFingerprint, currentFingerprint, found)
	}
	scriptPath, err := devmode.RebuildScriptPath(repoRoot)
	if err != nil {
		return "", err
	}
	log.Printf("local container create rebuilding canonical image runtime=%q image=%q script=%q", runtimeName, image, scriptPath)
	cmd := exec.CommandContext(ctx, "bash", scriptPath, "--image-only")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"BUILD_RUNTIME="+runtimeName,
		"IMAGE_NAME="+image,
		"SWARM_REBUILD_REASON=local-container-create",
		"SWARM_CONTAINER_DEV_FINGERPRINT="+expectedFingerprint,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("rebuild canonical local swarm image %q: %s", image, message)
	}
	log.Printf("local container create rebuilt canonical image runtime=%q image=%q", runtimeName, image)
	return expectedFingerprint, nil
}

func resolveConfiguredDevRoot(startupCfg startupconfig.FileConfig) (string, error) {
	if !startupCfg.DevMode {
		return "", fmt.Errorf("dev_mode is disabled")
	}
	devRoot := strings.TrimSpace(startupCfg.DevRoot)
	if devRoot == "" {
		return "", fmt.Errorf("dev_mode is enabled but dev_root is empty; run a rebuild from the source checkout once so the dev container path can use that checkout explicitly")
	}
	resolvedRoot, err := devmode.ResolveRoot(devRoot)
	if err != nil {
		return "", fmt.Errorf("resolve dev_root %q: %w", devRoot, err)
	}
	return resolvedRoot, nil
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
		"SWARM_RUNTIME_HOME=/var/lib/swarmd/home",
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
	repoFFFLibPath := filepath.Join("swarmd", "internal", "fff", "lib", fffLibraryPlatformDir(), "libfff_c.so")
	installedFFFLibRelPath := filepath.Join("lib", "libfff_c.so")
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidate := filepath.Join(wd, repoFFFLibPath)
		if _, statErr := os.Stat(candidate); statErr == nil {
			repoFFFLibPath = candidate
		}
	}
	if !filepath.IsAbs(repoFFFLibPath) {
		if repoRoot, _, err := resolveCanonicalRebuildScript(); err == nil {
			repoFFFLibPath = filepath.Join(repoRoot, repoFFFLibPath)
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
	if repoRoot, _, err := resolveCanonicalRebuildScript(); err == nil {
		repoMount := normalizeRuntimeMount(&RuntimeMount{
			BinDir:     filepath.Join(repoRoot, ".bin", "main"),
			WebDistDir: filepath.Join(repoRoot, "web", "dist"),
			FFFLibPath: repoFFFLibPath,
		})
		if repoMount != nil && isReadableFile(filepath.Join(repoMount.BinDir, "swarmd")) {
			if !isReadableFile(filepath.Join(repoMount.WebDistDir, "index.html")) {
				repoMount.WebDistDir = ""
			}
			if !isReadableFile(repoMount.FFFLibPath) {
				repoMount.FFFLibPath = ""
			}
			return repoMount
		}
	}
	var best *RuntimeMount
	bestScore := -1
	for _, root := range roots {
		candidate := normalizeRuntimeMount(&RuntimeMount{
			BinDir:     filepath.Join(root, "bin"),
			ToolBinDir: filepath.Join(root, "libexec"),
			WebDistDir: filepath.Join(root, "share"),
			FFFLibPath: filepath.Join(root, installedFFFLibRelPath),
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
	for _, root := range roots {
		installedLib := filepath.Join(root, installedFFFLibRelPath)
		if isReadableFile(installedLib) {
			return normalizeRuntimeMount(&RuntimeMount{FFFLibPath: installedLib})
		}
	}
	return normalizeRuntimeMount(&RuntimeMount{FFFLibPath: repoFFFLibPath})
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
