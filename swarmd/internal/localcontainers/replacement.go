package localcontainers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/pkg/devmode"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type replacementRuntimeResult struct {
	Mounts []Mount
}

func (s *Service) Replace(ctx context.Context, input ReplaceInput) (ReplaceResult, error) {
	result := ReplaceResult{PathID: PathContainerReplace, State: "failed"}
	if s == nil || s.store == nil {
		return result, errors.New("local container service is not configured")
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return result, errors.New("local container id is required")
	}
	record, ok, err := s.store.Get(id)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, errors.New("local container not found")
	}
	result.ID = record.ID
	result.Name = record.Name
	result.ContainerName = record.ContainerName
	result.Runtime = record.Runtime
	result.PreviousContainerID = strings.TrimSpace(record.ContainerID)
	result.PreviousImageRef = strings.TrimSpace(record.Image)
	resolved, resolveErr := s.resolveRecord(record)
	if resolveErr != nil {
		return result, fmt.Errorf("inspect local container before replace: %w", resolveErr)
	}
	wasRunning := resolved.Status == "running"
	startMode := strings.ToLower(strings.TrimSpace(input.StartMode))
	switch startMode {
	case "", "preserve":
	case "start":
		wasRunning = true
	case "stop", "stopped":
		wasRunning = false
	default:
		return result, errors.New("start mode must be preserve, start, or stop")
	}
	startupCfg, cfgErr := s.loadStartupConfig()
	if cfgErr != nil {
		return result, cfgErr
	}
	devMode := startupCfg.DevMode
	if input.DevMode != nil {
		devMode = *input.DevMode
		startupCfg.DevMode = devMode
	}
	targetImage, targetFingerprint := replacementTarget(input.Target)
	if targetImage == "" {
		target, targetErr := localUpdateTarget(ctx, startupCfg, "")
		if targetErr != nil {
			return result, targetErr
		}
		targetImage, targetFingerprint = replacementTarget(target)
	}
	if targetImage == "" {
		return result, errors.New("target image is required")
	}
	if input.Target.DigestRef != "" {
		input.Target.DigestRef = targetImage
	}
	if input.Target.ImageRef == "" && input.Target.PostRebuildImageRef == "" {
		input.Target.ImageRef = targetImage
	}
	if input.Target.Fingerprint == "" && input.Target.PostRebuildFingerprint == "" {
		input.Target.Fingerprint = targetFingerprint
	}
	result.TargetImageRef = targetImage
	result.TargetFingerprint = targetFingerprint
	planItem := s.planLocalContainerUpdate(ctx, record, startupCfg, UpdatePlanTarget{
		ImageRef:               targetImage,
		DigestRef:              input.Target.DigestRef,
		Fingerprint:            targetFingerprint,
		PostRebuildImageRef:    input.Target.PostRebuildImageRef,
		PostRebuildFingerprint: input.Target.PostRebuildFingerprint,
		Version:                input.Target.Version,
		Commit:                 input.Target.Commit,
	}, nil)
	if planItem.State == "already-current" {
		result.State = "skipped"
		result.Reason = "already-current"
		result.Status = resolved.Status
		result.ContainerID = strings.TrimSpace(resolved.ContainerID)
		result.Container = mapRecord(resolved)
		return result, nil
	}
	if planItem.State != "needs-update" {
		if planItem.Error != "" {
			return result, fmt.Errorf("local container replacement is not ready: %s", planItem.Error)
		}
		return result, fmt.Errorf("local container replacement is not ready: %s", firstNonEmpty(planItem.Reason, planItem.State))
	}
	if devMode && strings.TrimSpace(targetFingerprint) == "" {
		return result, errors.New("target dev image fingerprint is required")
	}
	if err := s.verifyReplacementTarget(ctx, resolved.Runtime, targetImage, targetFingerprint, devMode); err != nil {
		return result, err
	}
	if err := s.verifyReplacementRecordSupported(record); err != nil {
		return result, err
	}
	runtimeResult, replaceErr := s.replaceRuntimeContainer(ctx, resolved, targetImage, wasRunning)
	if replaceErr != nil {
		return result, replaceErr
	}
	updated := record
	updated.ContainerID = ""
	updated.Image = targetImage
	updated.Warning = ""
	if wasRunning {
		updated.Status = "running"
	} else {
		updated.Status = "exited"
	}
	refreshed, refreshErr := s.resolveRecord(updated)
	if refreshErr != nil {
		updated.Warning = refreshErr.Error()
	}
	if strings.TrimSpace(refreshed.ContainerID) != "" {
		updated.ContainerID = refreshed.ContainerID
	}
	if refreshed.Status != "" {
		updated.Status = refreshed.Status
	}
	if len(runtimeResult.Mounts) > 0 {
		updated.Mounts = normalizeMounts(runtimeResult.Mounts)
	}
	saved, saveErr := s.store.Put(updated)
	if saveErr != nil {
		return result, saveErr
	}
	if attachUpdateErr := s.updateAttachedDeploymentAfterReplace(record, saved, wasRunning); attachUpdateErr != nil {
		saved.Warning = attachUpdateErr.Error()
		if rewritten, rewriteErr := s.store.Put(saved); rewriteErr == nil {
			saved = rewritten
		}
		result.Warning = attachUpdateErr.Error()
	}
	resolvedSaved, resolveSavedErr := s.resolveRecord(saved)
	if resolveSavedErr == nil {
		if refreshedSaved, rewriteErr := s.store.Put(resolvedSaved); rewriteErr == nil {
			resolvedSaved = refreshedSaved
		}
	}
	if resolveSavedErr != nil && strings.TrimSpace(resolvedSaved.Warning) == "" {
		resolvedSaved.Warning = resolveSavedErr.Error()
	}
	result.State = "replaced"
	result.Status = resolvedSaved.Status
	result.ContainerID = strings.TrimSpace(resolvedSaved.ContainerID)
	result.Container = mapRecord(resolvedSaved)
	return result, nil
}

func replacementTarget(target UpdatePlanTarget) (string, string) {
	image := firstNonEmpty(target.PostRebuildImageRef, target.DigestRef, target.ImageRef)
	fingerprint := firstNonEmpty(target.PostRebuildFingerprint, target.Fingerprint)
	return image, fingerprint
}

func (s *Service) verifyReplacementRecordSupported(record pebblestore.SwarmLocalContainerRecord) error {
	if s == nil || s.deployments == nil {
		return nil
	}
	deployments, err := s.deployments.List(500)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if !deploymentMatchesRecord(deployment, record) {
			continue
		}
		if len(deployment.ContainerPackages.Packages) > 0 {
			return errors.New("package-aware local container replacement is deferred until package metadata can be rebuilt safely")
		}
		if strings.TrimSpace(deployment.BootstrapSecret) != "" && strings.TrimSpace(deployment.AttachStatus) != "attached" {
			return errors.New("local container replacement requires an attached deployment record")
		}
	}
	return nil
}

func (s *Service) verifyReplacementTarget(ctx context.Context, runtimeName, imageRef, targetFingerprint string, devMode bool) error {
	inspector := inspectRuntimeImage
	if s != nil && s.inspectImageFn != nil {
		inspector = s.inspectImageFn
	}
	imageInfo, err := inspector(ctx, runtimeName, imageRef)
	if err != nil {
		return fmt.Errorf("inspect replacement image %q: %w", imageRef, err)
	}
	if devMode {
		fingerprint := strings.TrimSpace(imageInfo.Labels[devmode.ContainerImageFingerprintLabel])
		if fingerprint == "" {
			return fmt.Errorf("replacement image %q is missing dev fingerprint label", imageRef)
		}
		if fingerprint != strings.TrimSpace(targetFingerprint) {
			return fmt.Errorf("replacement image %q fingerprint %q does not match target %q", imageRef, fingerprint, strings.TrimSpace(targetFingerprint))
		}
	}
	return nil
}

func (s *Service) replaceRuntimeContainer(ctx context.Context, record pebblestore.SwarmLocalContainerRecord, targetImage string, start bool) (replacementRuntimeResult, error) {
	runtimeName := normalizeRuntimeSelection(record.Runtime)
	containerName := strings.TrimSpace(record.ContainerName)
	if runtimeName == "" {
		return replacementRuntimeResult{}, errors.New("container runtime is required")
	}
	if containerName == "" {
		return replacementRuntimeResult{}, errors.New("container name is required")
	}
	runner := runContainer
	if s != nil && s.runContainerFn != nil {
		runner = s.runContainerFn
	}
	renamer := renameContainer
	if s != nil && s.renameContainerFn != nil {
		renamer = s.renameContainerFn
	}
	remover := removeContainer
	if s != nil && s.removeContainerFn != nil {
		remover = s.removeContainerFn
	}
	controller := controlContainer
	if s != nil && s.controlContainerFn != nil {
		controller = s.controlContainerFn
	}
	env := []string{fmt.Sprintf("SWARMD_LISTEN=0.0.0.0:%d", containerBackendPort)}
	if s != nil && s.inspectContainerEnvFn != nil {
		inspectedEnv, err := s.inspectContainerEnvFn(ctx, runtimeName, containerName)
		if err != nil {
			return replacementRuntimeResult{}, fmt.Errorf("inspect existing container environment: %w", err)
		}
		if len(inspectedEnv) > 0 {
			env = AppendInheritedChildDebugEnv(ensureLocalSwarmRuntimeEnv(normalizeEnv(inspectedEnv)))
		}
	}
	mounts := append([]Mount(nil), record.Mounts...)
	if s != nil && s.inspectContainerMountsFn != nil {
		inspectedMounts, err := s.inspectContainerMountsFn(ctx, runtimeName, containerName)
		if err != nil {
			return replacementRuntimeResult{}, fmt.Errorf("inspect existing container mounts: %w", err)
		}
		if len(inspectedMounts) > 0 {
			mounts = normalizeMounts(append(mounts, inspectedMounts...))
		}
	}
	extraRunArgs := []string(nil)
	if s != nil && s.inspectContainerRunArgsFn != nil {
		inspectedRunArgs, err := s.inspectContainerRunArgsFn(ctx, runtimeName, containerName)
		if err != nil {
			return replacementRuntimeResult{}, fmt.Errorf("inspect existing container run args: %w", err)
		}
		if len(inspectedRunArgs) > 0 {
			extraRunArgs = normalizeRunArgs(inspectedRunArgs)
		}
	}
	backupName := replacementBackupContainerName(containerName)
	if backupName != containerName {
		_ = remover(ctx, runtimeName, backupName)
	}
	oldRenamed := false
	if backupName == containerName {
		if err := remover(ctx, runtimeName, containerName); err != nil && !isMissingContainerRemoveError(err) {
			return replacementRuntimeResult{}, err
		}
	} else if start {
		if err := controller(ctx, runtimeName, "stop", containerName); err != nil && !isMissingContainerRemoveError(err) && !isAlreadyStoppedContainerError(err) {
			return replacementRuntimeResult{}, err
		}
		if err := renamer(ctx, runtimeName, containerName, backupName); err != nil {
			if !isMissingContainerRemoveError(err) {
				return replacementRuntimeResult{}, err
			}
		} else {
			oldRenamed = true
		}
	} else if err := renamer(ctx, runtimeName, containerName, backupName); err != nil {
		if !isMissingContainerRemoveError(err) {
			return replacementRuntimeResult{}, err
		}
	} else {
		oldRenamed = true
	}
	newContainerID, runErr := runner(ctx, runtimeName, runOptions{
		ContainerName: containerName,
		NetworkName:   record.NetworkName,
		HostPort:      record.HostPort,
		Image:         targetImage,
		Mounts:        mounts,
		Env:           env,
		ExtraRunArgs:  extraRunArgs,
	})
	if runErr != nil {
		_ = remover(ctx, runtimeName, containerName)
		if oldRenamed {
			_ = renamer(ctx, runtimeName, backupName, containerName)
		}
		return replacementRuntimeResult{}, runErr
	}
	if !start {
		if err := controller(ctx, runtimeName, "stop", containerName); err != nil {
			_ = remover(ctx, runtimeName, containerName)
			if oldRenamed {
				_ = renamer(ctx, runtimeName, backupName, containerName)
			}
			return replacementRuntimeResult{}, err
		}
	}
	if oldRenamed && strings.TrimSpace(backupName) != strings.TrimSpace(containerName) {
		_ = remover(ctx, runtimeName, backupName)
	}
	_ = strings.TrimSpace(newContainerID)
	return replacementRuntimeResult{Mounts: mounts}, nil
}

func replacementBackupContainerName(containerName string) string {
	containerName = strings.TrimSpace(containerName)
	if containerName == "" {
		return ""
	}
	return containerName + "-swarm-update-old"
}

func (s *Service) updateAttachedDeploymentAfterReplace(previous, updated pebblestore.SwarmLocalContainerRecord, running bool) error {
	if s == nil || s.deployments == nil {
		return nil
	}
	deployments, err := s.deployments.List(500)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if !deploymentMatchesRecord(deployment, previous) {
			continue
		}
		deployment.Runtime = updated.Runtime
		deployment.ContainerName = updated.ContainerName
		deployment.ContainerID = updated.ContainerID
		deployment.HostAPIBaseURL = updated.HostAPIBaseURL
		deployment.BackendHostPort = updated.HostPort
		deployment.DesktopHostPort = updated.HostPort + 1
		deployment.Image = updated.Image
		if running {
			deployment.Status = "running"
		} else {
			deployment.Status = "stopped"
		}
		if _, err := s.deployments.Put(deployment); err != nil {
			return err
		}
	}
	return nil
}
