package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/imagegen"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const manageImageMaxCodexCount = 3

var newManageImageThreadID = sessionruntime.NewSessionID

func (r *Runtime) manageImageInspect(ctx context.Context, scope WorkspaceScope) (string, error) {
	if r == nil || r.imageGen == nil {
		return "", errors.New("manage-image image generation service is not configured; background image jobs must run through the workspace-owning daemon")
	}
	caps, err := r.imageGen.Capabilities(ctx)
	if err != nil {
		return "", fmt.Errorf("manage-image inspect failed: %w", err)
	}
	provider, model := defaultManageImageProviderModel(caps)
	response := map[string]any{
		"status":             "ok",
		"action":             "inspect",
		"tool":               "manage-image",
		"ready":              provider != "",
		"mode":               "background_workspace_image_generation",
		"storage_semantics":  "daemon_system_managed_data_root_workspace_image_session_storage",
		"providers":          caps.Providers,
		"default_provider":   provider,
		"default_model":      model,
		"count_limits":       manageImageCountLimits(caps),
		"supported_settings": manageImageSupportedSettings(),
		"workspace_path":     strings.TrimSpace(scope.PrimaryPath),
		"open_url_template":  manageImageOpenURLTemplate(),
		"details_truncated":  false,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) manageImageGenerate(ctx context.Context, scope WorkspaceScope, args map[string]any, onProgress func(Progress)) (string, error) {
	if r == nil || r.imageGen == nil || r.imageThreads == nil {
		return "", errors.New("manage-image image service/storage is not configured; background image jobs must run through the workspace-owning daemon")
	}
	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	if workspacePath == "" {
		return "", errors.New("manage-image requires an active workspace path")
	}
	prompt := strings.TrimSpace(asString(args["prompt"]))
	if prompt == "" {
		return "", errors.New("manage-image generate requires prompt")
	}
	caps, err := r.imageGen.Capabilities(ctx)
	if err != nil {
		return "", fmt.Errorf("manage-image inspect providers failed: %w", err)
	}
	provider := normalizeManageImageProvider(asString(args["provider"]))
	model := strings.TrimSpace(asString(args["model"]))
	if provider == "" {
		provider, model = defaultManageImageProviderModel(caps)
	}
	if provider == "" {
		return "", errors.New("manage-image no ready image provider is configured")
	}
	if model == "" {
		model = defaultManageImageModel(caps, provider)
	}
	threadID := strings.TrimSpace(asString(args["thread_id"]))
	createdThread := false
	if threadID == "" {
		thread, createErr := r.createManageImageThread(scope, args)
		if createErr != nil {
			return "", createErr
		}
		threadID = thread.ID
		createdThread = true
	} else if _, ok, getErr := r.imageThreads.Get(threadID); getErr != nil {
		return "", fmt.Errorf("manage-image get image thread failed: %w", getErr)
	} else if !ok {
		return "", errors.New("manage-image image thread not found")
	}
	count := asInt(args["count"], 1)
	if count == 0 {
		count = 1
	}
	settings, err := manageImageSettings(args)
	if err != nil {
		return "", err
	}
	emitManageImageProgress(onProgress, "started", map[string]any{
		"thread_id":       threadID,
		"open_url":        manageImageOpenURL(threadID),
		"provider":        provider,
		"model":           model,
		"requested_count": count,
	})
	result, err := r.imageGen.Generate(ctx, imagegen.GenerateRequest{
		Provider: provider,
		Model:    model,
		Prompt:   prompt,
		Count:    count,
		Size:     strings.TrimSpace(asString(args["size"])),
		Settings: settings,
		Target: imagegen.GenerationTarget{
			Kind:     imagegen.TargetWorkspaceImage,
			ThreadID: threadID,
		},
		OnEvent: manageImageGenerateStreamCallback(onProgress, threadID, provider, model, count),
	})
	if err != nil {
		if createdThread {
			return "", fmt.Errorf("manage-image generate failed after creating thread %s: %w", threadID, err)
		}
		return "", fmt.Errorf("manage-image generate failed: %w", err)
	}
	if result.Target != nil && strings.TrimSpace(result.Target.Thread.ID) != "" {
		threadID = strings.TrimSpace(result.Target.Thread.ID)
	}
	assets := compactManageImageAssets(result.Assets)
	emitManageImageProgress(onProgress, "completed", map[string]any{
		"thread_id":       threadID,
		"open_url":        manageImageOpenURL(threadID),
		"provider":        provider,
		"model":           model,
		"requested_count": count,
		"saved_count":     len(assets),
	})
	response := map[string]any{
		"status":          "completed",
		"tool":            "manage-image",
		"thread_id":       threadID,
		"open_url":        manageImageOpenURL(threadID),
		"provider":        provider,
		"model":           model,
		"requested_count": count,
		"saved_count":     len(assets),
		"assets":          assets,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) createManageImageThread(scope WorkspaceScope, args map[string]any) (pebblestore.ImageThreadSnapshot, error) {
	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	threadID := newManageImageThreadID()
	storagePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", "image", "sessions", threadID)
	if err != nil {
		return pebblestore.ImageThreadSnapshot{}, fmt.Errorf("resolve image session storage: %w", err)
	}
	storagePath = filepath.Clean(storagePath)
	if storagePath == "." || storagePath == "" {
		return pebblestore.ImageThreadSnapshot{}, errors.New("image session storage path is invalid")
	}
	metadata := map[string]any{
		"tool_storage_path":      storagePath,
		"tool_kind":              "image",
		"session_schema_version": 1,
		"storage_area":           "daemon_system_managed_data_root/workspaces/tools/image/sessions",
		"created_by_tool":        "manage-image",
	}
	if purpose := strings.TrimSpace(asString(args["purpose"])); purpose != "" {
		metadata["purpose"] = purpose
	}
	title := strings.TrimSpace(asString(args["title"]))
	if title == "" {
		title = "Image Generation"
	}
	workspaceName := filepath.Base(workspacePath)
	if r != nil && r.workspace != nil {
		if info, scopeErr := r.workspace.ScopeForPath(workspacePath); scopeErr == nil && strings.TrimSpace(info.WorkspaceName) != "" {
			workspaceName = strings.TrimSpace(info.WorkspaceName)
		}
	}
	thread, err := r.imageThreads.Create(pebblestore.ImageThreadSnapshot{
		ID:            threadID,
		WorkspacePath: workspacePath,
		WorkspaceName: workspaceName,
		Title:         title,
		ImageFolders:  []string{storagePath},
		Metadata:      metadata,
	})
	if err != nil {
		return pebblestore.ImageThreadSnapshot{}, fmt.Errorf("create image thread: %w", err)
	}
	return thread, nil
}

func defaultManageImageProviderModel(caps imagegen.Capabilities) (string, string) {
	for _, provider := range caps.Providers {
		if provider.Ready {
			return provider.ID, provider.DefaultModel
		}
	}
	return "", ""
}

func defaultManageImageModel(caps imagegen.Capabilities, providerID string) string {
	providerID = normalizeManageImageProvider(providerID)
	for _, provider := range caps.Providers {
		if normalizeManageImageProvider(provider.ID) == providerID {
			return strings.TrimSpace(provider.DefaultModel)
		}
	}
	return ""
}

func manageImageCountLimits(caps imagegen.Capabilities) map[string]any {
	limits := map[string]any{}
	for _, provider := range caps.Providers {
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			continue
		}
		maxCount := manageImageMaxCodexCount
		if normalizeManageImageProvider(providerID) == imagegen.ProviderGoogleGemini {
			maxCount = 10
		}
		limits[providerID] = map[string]any{"min": 1, "max": maxCount}
	}
	return limits
}

func manageImageSupportedSettings() []string {
	return []string{"size", "aspect_ratio", "image_size", "quality", "background", "output_format"}
}

func normalizeManageImageProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "default", "auto":
		return ""
	case "openai", "codex":
		return imagegen.ProviderCodexOpenAI
	case "google", "gemini", "nano_banana", "nano-banana":
		return imagegen.ProviderGoogleGemini
	default:
		return strings.TrimSpace(provider)
	}
}

func manageImageSettings(args map[string]any) (map[string]any, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	if rawSettings, ok := args["settings"]; ok && rawSettings != nil {
		settings, ok := rawSettings.(map[string]any)
		if !ok {
			return nil, errors.New("manage-image settings must be an object")
		}
		for key, value := range settings {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			out[key] = value
		}
	}
	for _, key := range []string{"aspect_ratio", "image_size", "quality", "background", "output_format"} {
		if value, ok := args[key]; ok && value != nil {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func manageImageGenerateStreamCallback(onProgress func(Progress), threadID, provider, model string, requestedCount int) func(imagegen.GenerateStreamEvent) {
	if onProgress == nil {
		return nil
	}
	return func(event imagegen.GenerateStreamEvent) {
		metadata := map[string]any{
			"thread_id":       strings.TrimSpace(threadID),
			"open_url":        manageImageOpenURL(threadID),
			"provider":        strings.TrimSpace(provider),
			"model":           strings.TrimSpace(model),
			"requested_count": requestedCount,
			"event_type":      strings.TrimSpace(event.Type),
		}
		if event.OutputIndex >= 0 {
			metadata["output_index"] = event.OutputIndex
		}
		if event.SequenceNumber > 0 {
			metadata["sequence_number"] = event.SequenceNumber
		}
		if event.PartialImageIndex > 0 {
			metadata["partial_image_index"] = event.PartialImageIndex
		}
		if strings.TrimSpace(event.ItemID) != "" {
			metadata["item_id"] = strings.TrimSpace(event.ItemID)
		}
		if strings.TrimSpace(event.Text) != "" {
			metadata["text"] = strings.TrimSpace(event.Text)
		}
		if strings.TrimSpace(event.Thinking) != "" {
			metadata["thinking"] = strings.TrimSpace(event.Thinking)
		}
		stage := "generating"
		switch strings.ToLower(strings.TrimSpace(event.Type)) {
		case "started":
			stage = "started"
		case "completed":
			stage = "completed"
		case "partial_image", "response.image_generation_call.partial_image", "image_generation.partial_image":
			stage = "partial"
		}
		emitManageImageProgress(onProgress, stage, metadata)
	}
}

func emitManageImageProgress(onProgress func(Progress), stage string, metadata map[string]any) {
	if onProgress == nil {
		return
	}
	metadata = compactManageImageProgressMetadata(metadata)
	encoded, err := json.Marshal(map[string]any{
		"tool":      "manage-image",
		"status":    strings.TrimSpace(stage),
		"thread_id": strings.TrimSpace(asString(metadata["thread_id"])),
		"open_url":  strings.TrimSpace(asString(metadata["open_url"])),
		"provider":  strings.TrimSpace(asString(metadata["provider"])),
		"model":     strings.TrimSpace(asString(metadata["model"])),
		"metadata":  metadata,
	})
	if err != nil {
		return
	}
	onProgress(Progress{Stage: "image", Output: string(encoded), Metadata: metadata})
}

func compactManageImageProgressMetadata(metadata map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		if key == "" || key == "partial_image_b64" || key == "base64_image" || key == "data_url" || key == "b64_json" {
			continue
		}
		out[key] = value
	}
	return out
}

func compactManageImageAssets(assets []imagegen.GeneratedAsset) []map[string]any {
	out := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		entry := map[string]any{
			"asset_id":   strings.TrimSpace(asset.ID),
			"name":       strings.TrimSpace(asset.Name),
			"url":        strings.TrimSpace(asset.URL),
			"provider":   strings.TrimSpace(asset.Provider),
			"model":      strings.TrimSpace(asset.Model),
			"size_bytes": asset.SizeBytes,
		}
		if strings.TrimSpace(asset.RevisedPrompt) != "" {
			entry["revised_prompt"] = strings.TrimSpace(asset.RevisedPrompt)
		}
		out = append(out, entry)
	}
	return out
}

func manageImageOpenURL(threadID string) string {
	return "swarm://tools/image/sessions/" + url.PathEscape(strings.TrimSpace(threadID))
}

func manageImageOpenURLTemplate() string {
	return "swarm://tools/image/sessions/{thread_id}"
}
