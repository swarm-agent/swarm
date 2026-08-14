package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const directImageSwarmRouterParallelism = 8

type directImageSwarmHydration struct {
	Index  int
	Prompt string
	Title  string
	Theme  string
}

func directImageSwarmProgress(phase, currentStage string) (string, string, []string) {
	phase = strings.ToLower(strings.TrimSpace(phase))
	stage := strings.ToLower(strings.TrimSpace(currentStage))
	switch phase {
	case "completed":
		return "completed", "Image creation", []string{"Routing", "Image creation"}
	case "failed", "error":
		if stage == "router" {
			return "failed", "Routing", []string{"Routing"}
		}
		return "failed", "Image creation", []string{"Routing", "Image creation"}
	case "generating":
		return "running", "Image creation", []string{"Routing", "Image creation"}
	default:
		return "running", "Routing", []string{"Routing"}
	}
}

func buildDirectImageSwarmStreamPayload(callID, action, description string, imageCount, index int, phase, title, theme, currentStage, preview string, reference *taskArtifactReference) map[string]any {
	status, stageLabel, stageHistory := directImageSwarmProgress(phase, currentStage)
	imageKey := fmt.Sprintf("image:%d", index)
	image := map[string]any{
		"image_key": imageKey, "index": index, "status": status, "phase": strings.TrimSpace(phase), "title": strings.TrimSpace(title), "theme": strings.TrimSpace(theme),
		"current_stage": strings.TrimSpace(currentStage), "current_stage_label": stageLabel, "stage_history": stageHistory, "preview": strings.TrimSpace(preview),
		"swarm_mode": true, "execution_format": taskExecutionFormatImageDirect, "child_session_created": false,
	}
	if reference != nil {
		image["artifact_reference"] = reference
	}
	return map[string]any{
		"path_id": "tool.task.image_swarm.stream.v1", "tool": "task", "task_call_id": strings.TrimSpace(callID), "action": strings.TrimSpace(action),
		"description": strings.TrimSpace(description), "status": status, "task_mode": taskModeSwarm, "swarm_strategy": taskSwarmStrategyExplore,
		"execution_format": taskExecutionFormatImageDirect, "image_count": imageCount, "image_key": imageKey, "image": image, "phase": strings.TrimSpace(phase),
	}
}

func emitDirectImageSwarmDelta(emit StreamHandler, step int, callID, action, description string, imageCount, index int, phase, title, theme, currentStage, preview string, reference *taskArtifactReference) {
	emitTaskStreamPayload(emit, step, "task", callID, buildDirectImageSwarmStreamPayload(callID, action, description, imageCount, index, phase, title, theme, currentStage, preview, reference))
}

func composeDirectImageSwarmPrompt(parentPrompt, baseTheme string, delta taskSwarmHydratedDelta) string {
	var b strings.Builder
	b.WriteString("Create one image from this overall brief:\n")
	b.WriteString(strings.TrimSpace(parentPrompt))
	if baseTheme = strings.TrimSpace(baseTheme); baseTheme != "" {
		b.WriteString("\n\nBase theme for this image:\n")
		b.WriteString(baseTheme)
	}
	b.WriteString("\n\nRouter-hydrated image direction:\n")
	b.WriteString("Title: ")
	b.WriteString(strings.TrimSpace(delta.Title))
	b.WriteString("\nSpecialized theme: ")
	b.WriteString(strings.TrimSpace(delta.Theme))
	b.WriteString("\nComposition and visual direction: ")
	b.WriteString(strings.TrimSpace(delta.Role))
	if len(delta.Constraints) > 0 {
		b.WriteString("\nConstraints:")
		for _, constraint := range delta.Constraints {
			b.WriteString("\n- ")
			b.WriteString(strings.TrimSpace(constraint))
		}
	}
	b.WriteString("\nRequired result: ")
	b.WriteString(strings.TrimSpace(delta.Deliverable))
	return strings.TrimSpace(b.String())
}

func (s *Service) hydrateDirectImageSwarm(ctx context.Context, parent pebblestore.SessionSnapshot, parsed taskCallArguments, callID string, principal identity.Principal, step int, emit StreamHandler) ([]directImageSwarmHydration, error) {
	if parsed.Swarm == nil || parsed.Swarm.AgentType != "image" || len(parsed.Launches) != parsed.Swarm.Count {
		return nil, errors.New("direct image swarm requires a complete image specification")
	}
	router, err := s.newTaskSwarmRouter(parent, principal, callID)
	if err != nil {
		return nil, err
	}
	results := make([]directImageSwarmHydration, len(parsed.Launches))
	errs := make([]error, len(parsed.Launches))
	sem := make(chan struct{}, directImageSwarmRouterParallelism)
	var wg sync.WaitGroup
	for i := range parsed.Launches {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			baseTheme := ""
			if i < len(parsed.Swarm.Themes) {
				baseTheme = strings.TrimSpace(parsed.Swarm.Themes[i])
			}
			emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(parsed.Launches), i+1, "hydrating", "", baseTheme, "router", "Hydrating the overall brief and this image's base theme.", nil)
			request := taskSwarmHydrationRequest{
				Prompt: parsed.Prompt, AgentType: "image", SwarmStrategy: parsed.Swarm.Strategy,
				OutputContract: parsed.Swarm.OutputContract, OutputMode: taskOutputModeManaged, OutputRequirements: cloneTaskOutputRequirements(parsed.Swarm.OutputRequirements),
				Items: []taskSwarmHydrationItem{{Index: 1, Theme: baseTheme, OutputMode: taskOutputModeManaged, WorkerExecution: "direct_image_model_generation"}},
			}
			hydrated, hydrateErr := router.Hydrate(ctx, request)
			if hydrateErr != nil {
				errs[i] = fmt.Errorf("image %d Router hydration failed: %w", i+1, hydrateErr)
				emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(parsed.Launches), i+1, "failed", "", baseTheme, "router", boundedTaskLaunchReason(hydrateErr.Error()), nil)
				return
			}
			if len(hydrated.Deltas) != 1 {
				errs[i] = fmt.Errorf("image %d Router hydration returned an invalid result", i+1)
				emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(parsed.Launches), i+1, "failed", "", baseTheme, "router", "Router returned an invalid image direction", nil)
				return
			}
			delta := hydrated.Deltas[0]
			results[i] = directImageSwarmHydration{Index: i + 1, Prompt: composeDirectImageSwarmPrompt(parsed.Prompt, baseTheme, delta), Title: strings.TrimSpace(delta.Title), Theme: baseTheme}
			if results[i].Theme == "" {
				results[i].Theme = strings.TrimSpace(delta.Theme)
			}
		}()
	}
	wg.Wait()
	for _, hydrateErr := range errs {
		if hydrateErr != nil {
			return nil, hydrateErr
		}
	}
	return results, nil
}

func validateApprovedDirectImageSwarm(approved string, parsed taskCallArguments) error {
	if strings.TrimSpace(approved) == "" {
		return nil
	}
	var envelope struct {
		ManifestHash string             `json:"manifest_hash"`
		Manifest     taskLaunchManifest `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(approved), &envelope); err != nil {
		return fmt.Errorf("approved direct image swarm manifest invalid: %w", err)
	}
	digest, err := taskLaunchManifestDigest(envelope.Manifest)
	if err != nil || digest != strings.TrimSpace(envelope.ManifestHash) || digest != strings.TrimSpace(envelope.Manifest.ManifestHash) {
		return errors.New("approved direct image swarm manifest snapshot hash mismatch")
	}
	if envelope.Manifest.ExecutionFormat != taskExecutionFormatImageDirect || envelope.Manifest.SwarmAgentType != "image" || parsed.Swarm == nil || len(envelope.Manifest.Images) != parsed.Swarm.Count {
		return errors.New("approved direct image swarm manifest format mismatch")
	}
	for i, row := range envelope.Manifest.Images {
		baseTheme := ""
		if i < len(parsed.Swarm.Themes) {
			baseTheme = strings.TrimSpace(parsed.Swarm.Themes[i])
		}
		if row.Index != i+1 || row.StreamKey != parsed.Launches[i].StreamKey || strings.TrimSpace(row.Theme) != baseTheme || !equalTaskOutputRequirements(row.OutputRequirements, parsed.Launches[i].OutputRequirements) {
			return fmt.Errorf("approved direct image swarm item %d mismatch", i+1)
		}
	}
	return nil
}

func (s *Service) executeDirectImageSwarm(ctx context.Context, sessionID, sessionMode string, step int, call tool.Call, emit StreamHandler, req taskExecutionRequest, parsed taskCallArguments, description, prompt string) (string, error) {
	if s == nil || s.sessions == nil || s.tools == nil || s.tools.ArtifactAuthority() == nil {
		return "", errors.New("direct image swarm services are not fully configured")
	}
	if err := validateApprovedDirectImageSwarm(req.ApprovedArguments, parsed); err != nil {
		return "", err
	}
	parent := pebblestore.SessionSnapshot{}
	if req.ParentSession != nil {
		parent = *req.ParentSession
	} else if snapshot, ok, getErr := s.sessions.GetSession(sessionID); getErr != nil {
		return "", getErr
	} else if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	} else {
		parent = snapshot
	}
	callID := strings.TrimSpace(call.CallID)
	if callID == "" {
		callID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
	}
	hydrated, err := s.hydrateDirectImageSwarm(ctx, parent, parsed, callID, req.Principal, step, emit)
	if err != nil {
		return "", err
	}
	for i := range hydrated {
		emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(hydrated), i+1, "hydrated", hydrated[i].Title, hydrated[i].Theme, "router", "Prompt hydrated", nil)
	}

	specs := append([]taskLaunchSpec(nil), parsed.Launches...)
	for i := range specs {
		specs[i].AssignmentLabel = hydrated[i].Title
		if specs[i].SourceArguments == nil {
			specs[i].SourceArguments = map[string]any{}
		}
		specs[i].SourceArguments["swarm_theme"] = hydrated[i].Theme
	}
	collectionID, err := s.ensureManagedDesignerArtifactCollection(parent, callID, specs, req.ApplySessionMutation)
	if err != nil {
		return "", err
	}
	prepared := make([]taskLaunchPrepared, len(specs))
	for i, spec := range specs {
		run := managedDesignerArtifactContext(parent, callID, spec, i+1)
		if run == nil || run.CollectionID != collectionID {
			return "", fmt.Errorf("direct image swarm item %d cannot allocate a trusted artifact destination", i+1)
		}
		run.ChildSessionID = parent.ID
		prepared[i] = taskLaunchPrepared{LaunchIndex: i + 1, RequestedSubagent: "image_model", AssignmentLabel: hydrated[i].Title, StreamKey: spec.StreamKey, SwarmMode: true, SwarmStrategy: spec.SwarmStrategy, OutputMode: taskOutputModeManaged, OutputRequirements: cloneTaskOutputRequirements(spec.OutputRequirements), ArtifactRunContext: run, ChildSession: parent}
	}
	if err := s.ensureManagedDesignerArtifactPlaceholders(parent, prepared, req.ApplySessionMutation); err != nil {
		return "", err
	}

	type imageResult struct {
		Reference *taskArtifactReference
		Err       error
	}
	results := make([]imageResult, len(prepared))
	var wg sync.WaitGroup
	for i := range prepared {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			run := *prepared[i].ArtifactRunContext
			scope := tool.WorkspaceScope{PrimaryPath: parent.WorkspacePath, Roots: append([]string(nil), parent.TemporaryWorkspaceRoots...), SessionID: parent.ID, Principal: req.Principal}
			if !scope.Principal.Valid() {
				scope.Principal = identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: parent.ID, AccountScopeSource: identity.AccountScopeSourceSession}
			}
			emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "generating", hydrated[i].Title, hydrated[i].Theme, "image_model", "Generating image", nil)
			_, generateErr := s.tools.GenerateManagedImageArtifact(ctx, scope, fmt.Sprintf("%s:image:%d", callID, i+1), hydrated[i].Prompt, run)
			if generateErr != nil {
				s.markManagedDesignerArtifactFailed(parent, &run, parent.ID, "direct_image_generation_failed")
				results[i].Err = generateErr
				emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "failed", hydrated[i].Title, hydrated[i].Theme, "image_model", boundedTaskLaunchReason(generateErr.Error()), nil)
				return
			}
			principal := artifact.Principal{SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, TaskCallID: callID, ChildSessionID: parent.ID, IterationGroupID: run.IterationGroupID, IterationGroup: run.IterationGroup, IterationID: run.IterationID, IterationIndex: run.IterationIndex, IterationLabel: run.IterationLabel, IterationTheme: run.IterationTheme}
			variant, getErr := s.tools.ArtifactAuthority().Get(principal, run.VariantID)
			if getErr != nil || variant.Status != pebblestore.SessionArtifactStatusReady || variant.CollectionID != run.CollectionID {
				if getErr == nil {
					getErr = errors.New("generated image artifact is not ready at its trusted destination")
				}
				results[i].Err = getErr
				emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "failed", hydrated[i].Title, hydrated[i].Theme, "image_model", boundedTaskLaunchReason(getErr.Error()), nil)
				return
			}
			results[i].Reference = &taskArtifactReference{SessionID: parent.ID, CollectionID: variant.CollectionID, VariantID: variant.ID, Status: variant.Status, OutputRequirements: cloneTaskOutputRequirements(variant.OutputRequirements)}
			emitDirectImageSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "completed", hydrated[i].Title, hydrated[i].Theme, "image_model", "Image ready", results[i].Reference)
		}()
	}
	wg.Wait()

	items := make([]map[string]any, len(results))
	references := make([]*taskArtifactReference, 0, len(results))
	failed := 0
	var firstErr error
	for i, result := range results {
		status := "ok"
		item := map[string]any{"index": i + 1, "title": hydrated[i].Title, "theme": hydrated[i].Theme, "stream_key": prepared[i].StreamKey, "execution": "router_to_image_model", "child_session_created": false}
		if result.Err != nil {
			status, failed = "error", failed+1
			item["error"] = boundedTaskLaunchReason(result.Err.Error())
			if firstErr == nil {
				firstErr = result.Err
			}
		} else {
			item["artifact_reference"] = result.Reference
			references = append(references, result.Reference)
		}
		item["status"] = status
		items[i] = item
	}
	status := "ok"
	if failed > 0 {
		status = "error"
	}
	payload := map[string]any{"tool": "task", "path_id": "tool.task.image_swarm.v1", "task_call_id": callID, "action": parsed.Action, "status": status, "description": description, "goal": description, "prompt": prompt, "task_mode": taskModeSwarm, "swarm_strategy": parsed.Swarm.Strategy, "execution_format": taskExecutionFormatImageDirect, "image_count": len(items), "images": items, "success_count": len(items) - failed, "failed_count": failed, "artifact_references": references, "artifact_count": len(references), "child_session_count": 0, "subagent_launch_count": 0, "details_truncated": false}
	encoded, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		return "", encodeErr
	}
	if firstErr != nil {
		return string(encoded), firstErr
	}
	return string(encoded), nil
}
