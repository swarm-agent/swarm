package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	directDesignerSwarmRouterParallelism     = 8
	directDesignerSwarmGenerationParallelism = 8
	directDesignerSwarmMaxRepairAttempts     = 2
	directDesignerSwarmMaxOutputRunes        = 512000
)

type directDesignerSwarmHydration struct {
	Index      int
	Prompt     string
	Title      string
	Theme      string
	GroupTitle string
}

func directDesignerSwarmProgress(phase, currentStage string) (string, string, []string) {
	phase = strings.ToLower(strings.TrimSpace(phase))
	stage := strings.ToLower(strings.TrimSpace(currentStage))
	switch phase {
	case "completed":
		return "completed", "Artifact ready", []string{"Routing", "Artifact generation"}
	case "failed", "error":
		if stage == "router" {
			return "failed", "Routing", []string{"Routing"}
		}
		return "failed", "Artifact generation", []string{"Routing", "Artifact generation"}
	case "generating":
		return "running", "Artifact generation", []string{"Routing", "Artifact generation"}
	default:
		return "running", "Routing", []string{"Routing"}
	}
}

func buildDirectDesignerSwarmStreamPayload(callID, action, description string, designerCount, index int, phase, title, theme, currentStage, preview string, reference *taskArtifactReference) map[string]any {
	status, stageLabel, stageHistory := directDesignerSwarmProgress(phase, currentStage)
	designerKey := fmt.Sprintf("designer:%d", index)
	designer := map[string]any{
		"designer_key":          designerKey,
		"index":                 index,
		"status":                status,
		"phase":                 strings.TrimSpace(phase),
		"title":                 strings.TrimSpace(title),
		"theme":                 strings.TrimSpace(theme),
		"current_stage":         strings.TrimSpace(currentStage),
		"current_stage_label":   stageLabel,
		"stage_history":         stageHistory,
		"preview":               strings.TrimSpace(preview),
		"swarm_mode":            true,
		"execution_format":      taskExecutionFormatDesignerDirect,
		"child_session_created": false,
	}
	if reference != nil {
		designer["artifact_reference"] = reference
	}
	return map[string]any{
		"path_id":          "tool.task.designer_swarm.stream.v1",
		"tool":             "task",
		"task_call_id":     strings.TrimSpace(callID),
		"action":           strings.TrimSpace(action),
		"description":      strings.TrimSpace(description),
		"status":           status,
		"task_mode":        taskModeSwarm,
		"swarm_strategy":   taskSwarmStrategyExplore,
		"execution_format": taskExecutionFormatDesignerDirect,
		"designer_count":   designerCount,
		"designer_key":     designerKey,
		"designer":         designer,
		"phase":            strings.TrimSpace(phase),
	}
}

func emitDirectDesignerSwarmDelta(emit StreamHandler, step int, callID, action, description string, designerCount, index int, phase, title, theme, currentStage, preview string, reference *taskArtifactReference) {
	emitTaskStreamPayload(emit, step, "task", callID, buildDirectDesignerSwarmStreamPayload(callID, action, description, designerCount, index, phase, title, theme, currentStage, preview, reference))
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asUint64(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case int64:
		if n >= 0 {
			return uint64(n)
		}
	case int:
		if n >= 0 {
			return uint64(n)
		}
	case float64:
		if n >= 0 {
			return uint64(n)
		}
	}
	return 0
}

func validateApprovedDirectDesignerSwarm(approved string, parsed taskCallArguments) error {
	if strings.TrimSpace(approved) == "" {
		return nil
	}
	var envelope struct {
		ManifestHash string             `json:"manifest_hash"`
		Manifest     taskLaunchManifest `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(approved), &envelope); err != nil {
		return fmt.Errorf("approved direct designer swarm manifest invalid: %w", err)
	}
	digest, err := taskLaunchManifestDigest(envelope.Manifest)
	if err != nil || digest != strings.TrimSpace(envelope.ManifestHash) || digest != strings.TrimSpace(envelope.Manifest.ManifestHash) {
		return errors.New("approved direct designer swarm manifest snapshot hash mismatch")
	}
	if envelope.Manifest.ExecutionFormat != taskExecutionFormatDesignerDirect || envelope.Manifest.SwarmAgentType != "designer" || parsed.Swarm == nil || len(envelope.Manifest.Designers) != parsed.Swarm.Count {
		return errors.New("approved direct designer swarm manifest format mismatch")
	}
	for i, row := range envelope.Manifest.Designers {
		baseTheme := ""
		if i < len(parsed.Swarm.Themes) {
			baseTheme = strings.TrimSpace(parsed.Swarm.Themes[i])
		}
		if row.Index != i+1 || strings.TrimSpace(row.Theme) != baseTheme || strings.TrimSpace(row.StreamKey) != strings.TrimSpace(parsed.Launches[i].StreamKey) {
			return errors.New("approved direct designer swarm manifest row mismatch")
		}
	}
	return nil
}

func extractHTMLFromModelOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	re := regexp.MustCompile("(?is)```(?:html)?\\s*(<!doctype html.*?>.*?</html>|<html.*?>.*?</html>)\\s*```")
	if m := re.FindStringSubmatch(trimmed); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	reBare := regexp.MustCompile("(?is)(<!doctype html.*?>.*?</html>|<html.*?>.*?</html>)")
	if m := reBare.FindStringSubmatch(trimmed); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return trimmed
}

func validateGeneratedDesignerHTML(html string, isAnimation bool) error {
	trimmed := strings.TrimSpace(html)
	if trimmed == "" {
		return errors.New("model returned empty output")
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "<html") || !strings.Contains(lower, "</html>") {
		return errors.New("output does not contain a valid <html>...</html> document")
	}
	if isAnimation {
		if !strings.Contains(trimmed, "swarm-animation-manifest") {
			return errors.New("missing <script id=\"swarm-animation-manifest\" type=\"application/json\"> manifest")
		}
		if !strings.Contains(trimmed, "__SWARM_ANIMATION_V1__") {
			return errors.New("missing window.__SWARM_ANIMATION_V1__ animation bridge")
		}
		if !strings.Contains(trimmed, "swarm.animation/v1") {
			return errors.New("__SWARM_ANIMATION_V1__ must declare version: \"swarm.animation/v1\"")
		}
		if !strings.Contains(trimmed, "ready") || !strings.Contains(trimmed, "seek") {
			return errors.New("__SWARM_ANIMATION_V1__ must implement both ready() and seek(time_ms)")
		}
	}
	hasID := false
	for _, tag := range []string{"<main", "<div", "<section", "<article", "<canvas", "<svg"} {
		if strings.Contains(lower, tag) && strings.Contains(lower, "id=") {
			hasID = true
			break
		}
	}
	if !hasID {
		return errors.New("missing stage container element with an id attribute (e.g. <main id=\"swarm-animation-stage\">)")
	}
	return nil
}

func extractDurationFromPrompt(prompt string) int64 {
	reMS := regexp.MustCompile(`(?i)(?:duration|length)[^\d]*(\d+)\s*ms`)
	if m := reMS.FindStringSubmatch(prompt); len(m) > 1 {
		if val, err := strconv.ParseInt(m[1], 10, 64); err == nil && val > 0 {
			return val
		}
	}
	reManifest := regexp.MustCompile(`(?i)duration_ms["':\s]+(\d+)`)
	if m := reManifest.FindStringSubmatch(prompt); len(m) > 1 {
		if val, err := strconv.ParseInt(m[1], 10, 64); err == nil && val > 0 {
			return val
		}
	}
	reSec := regexp.MustCompile(`(?i)(\d+)\s*(?:second|sec)s?`)
	if m := reSec.FindStringSubmatch(prompt); len(m) > 1 {
		if val, err := strconv.ParseInt(m[1], 10, 64); err == nil && val > 0 {
			return val * 1000
		}
	}
	return 0
}

func composeDirectDesignerSwarmPrompt(parentPrompt, baseTheme string, controls *taskSwarmIterationControls, delta taskSwarmHydratedDelta, isIteration bool, baseHTML string, profile *pebblestore.SessionArtifactAnimationProfile, durationMS int64) (string, string) {
	var sys strings.Builder
	sys.WriteString(`You are Designer, Swarm's compiled UI and animation generation engine.
Your assignment is to generate a complete, production-ready, standalone single-file HTML5 document.

CRITICAL REQUIREMENTS:
1. Complete Standalone HTML5:
   - Must be a complete HTML document starting with <!DOCTYPE html> and closing with </html>.
   - All styling in <style>, all scripts in <script>.
   - Zero external CDNs, remote scripts, or remote stylesheet links. The document must work fully offline.
   - Stage element: include a main container element with an id attribute (e.g. <main id="swarm-animation-stage"> or <div id="stage">) filling the 1920x1080 viewport with a dark background (#020205).

2. Animation Contract & Controller Implementation:
   - In <head>, declare:
     <script id="swarm-animation-manifest" type="application/json">
     {"version":"swarm.animation/v1","duration_ms":` + fmt.Sprintf("%d", durationMS) + `,"fps":60}
     </script>
   - In <script>, implement deterministic frame rendering and loop cancellation:
     let animId = null;
     let isPlaying = false;

     function stopLoop() {
       isPlaying = false;
       if (animId) {
         cancelAnimationFrame(animId);
         animId = null;
       }
     }

     function renderState(time_ms) {
       // Synchronously draw canvas, SVG, or DOM visual state strictly from time_ms.
     }

     window.__SWARM_ANIMATION_V1__ = {
       version: "swarm.animation/v1",
       ready: async () => ({ duration_ms: ` + fmt.Sprintf("%d", durationMS) + `, fps: 60 }),
       pause: async () => {
         stopLoop();
       },
       seek: async (time_ms) => {
         stopLoop(); // MUST stop continuous loop immediately
         renderState(time_ms); // Synchronously draw static frame
         return { time_ms: time_ms };
       }
     };

   - STABILITY REQUIREMENT:
     When seek(time_ms) or pause() is called, stop all continuous animation loops immediately.
     The canvas and DOM must remain 100% static and motionless at time_ms until playback resumes.

3. Output Format:
   - Return ONLY the complete single-file HTML document wrapped in a single ` + "```html ... ```" + ` block.
   - Do NOT include any conversational preamble or commentary outside the code block.`)

	var user strings.Builder
	if isIteration {
		user.WriteString("ANIMATION REVISION:\n")
		user.WriteString("You are updating and refining an existing HTML animation.\n\n")
		user.WriteString("User Revision Instructions:\n")
		user.WriteString(strings.TrimSpace(parentPrompt))
		if baseTheme != "" {
			user.WriteString("\nSpecialized Theme: " + baseTheme)
		}
		if delta.Role != "" {
			user.WriteString("\nCreative Direction: " + delta.Role)
		}
		user.WriteString("\n\nExisting HTML Document:\n```html\n")
		user.WriteString(baseHTML)
		user.WriteString("\n```\n\n")
		user.WriteString("Instructions:\n")
		user.WriteString("1. Apply the user's requested changes directly to the existing HTML animation.\n")
		user.WriteString("2. Preserve the #swarm-animation-manifest and window.__SWARM_ANIMATION_V1__ controller contract.\n")
		user.WriteString("3. Return ONLY the complete updated standalone HTML in a ```html ... ``` block.\n")
	} else {
		user.WriteString("Authoritative Design Brief:\n")
		user.WriteString(strings.TrimSpace(parentPrompt))
		if baseTheme != "" {
			user.WriteString("\n\nSelected Theme: " + baseTheme)
		}
		if delta.Role != "" {
			user.WriteString("\nCreative Direction: " + delta.Role)
		}
		if controls != nil {
			user.WriteString("\n\nFocused Iteration Boundary:\n")
			writeTaskSwarmControlList(&user, "preserve", controls.Preserve)
			writeTaskSwarmControlList(&user, "change only", controls.Change)
			writeTaskSwarmControlList(&user, "exclude", controls.Exclude)
		}
	}
	return sys.String(), user.String()
}

func (s *Service) hydrateDirectDesignerSwarm(ctx context.Context, parent pebblestore.SessionSnapshot, parsed taskCallArguments, callID string, principal identity.Principal, step int, emit StreamHandler) ([]directDesignerSwarmHydration, error) {
	if parsed.Swarm == nil || parsed.Swarm.AgentType != "designer" || len(parsed.Launches) != parsed.Swarm.Count {
		return nil, errors.New("direct designer swarm requires a complete designer specification")
	}
	results := make([]directDesignerSwarmHydration, len(parsed.Launches))
	errs := make([]error, len(parsed.Launches))
	sem := make(chan struct{}, directDesignerSwarmRouterParallelism)
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
			emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(parsed.Launches), i+1, "hydrating", "", baseTheme, "router", "Hydrating the design brief and theme specialization.", nil)
			request := taskSwarmHydrationRequest{
				Description: parsed.Description, Prompt: parsed.Prompt, AgentType: "designer", SwarmStrategy: parsed.Swarm.Strategy,
				OutputContract: parsed.Swarm.OutputContract, OutputMode: taskOutputModeManaged, OutputRequirements: cloneTaskOutputRequirements(parsed.Swarm.OutputRequirements),
				AnimationProfile:  cloneTaskAnimationProfile(parsed.Swarm.AnimationProfile),
				IterationControls: cloneTaskSwarmIterationControls(parsed.Swarm.IterationControls),
				Items:             []taskSwarmHydrationItem{{Index: 1, Theme: baseTheme, OutputMode: taskOutputModeManaged, WorkerExecution: "direct_designer_model_generation"}},
			}
			slotRouter, slotErr := s.newTaskSwarmRouter(parent, principal, fmt.Sprintf("%s:slot:%d", callID, i+1))
			if slotErr != nil {
				errs[i] = slotErr
				return
			}
			hydrated, hydrateErr := slotRouter.Hydrate(ctx, request)
			if hydrateErr != nil {
				errs[i] = fmt.Errorf("designer %d Router hydration failed: %w", i+1, hydrateErr)
				emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(parsed.Launches), i+1, "failed", "", baseTheme, "router", boundedTaskLaunchReason(hydrateErr.Error()), nil)
				return
			}
			if len(hydrated.Deltas) != 1 {
				errs[i] = fmt.Errorf("designer %d Router hydration returned an invalid result", i+1)
				emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(parsed.Launches), i+1, "failed", "", baseTheme, "router", "Router returned an invalid design direction", nil)
				return
			}
			delta := hydrated.Deltas[0]
			title := strings.TrimSpace(delta.Title)
			if title == "" {
				title = fmt.Sprintf("Design Variant %d", i+1)
			}
			results[i] = directDesignerSwarmHydration{
				Index:      i + 1,
				Title:      title,
				Theme:      baseTheme,
				GroupTitle: strings.TrimSpace(hydrated.GroupTitle),
			}
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

func (s *Service) executeDirectDesignerSwarm(ctx context.Context, sessionID, sessionMode string, step int, call tool.Call, emit StreamHandler, req taskExecutionRequest, parsed taskCallArguments, description, prompt string) (string, error) {
	if s == nil || s.sessions == nil || s.tools == nil {
		return "", errors.New("direct designer swarm services are not configured")
	}
	if parsed.Swarm == nil || parsed.Swarm.AgentType != "designer" || parsed.Swarm.Count < 1 || parsed.Swarm.Count > taskSwarmMaxAgents {
		return "", fmt.Errorf("direct designer swarm count must be between 1 and %d", taskSwarmMaxAgents)
	}
	if err := validateApprovedDirectDesignerSwarm(req.ApprovedArguments, parsed); err != nil {
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
	if parent.AccountScopeID == "" {
		parent.AccountScopeID = strings.TrimSpace(req.Principal.AccountScopeID)
	}
	if parent.UserID == "" {
		parent.UserID = strings.TrimSpace(req.Principal.UserID)
	}

	callID := strings.TrimSpace(call.CallID)
	if callID == "" {
		callID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
	}

	hydrated, err := s.hydrateDirectDesignerSwarm(ctx, parent, parsed, callID, req.Principal, step, emit)
	if err != nil {
		return "", err
	}
	for i := range hydrated {
		emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, parsed.Description, len(hydrated), i+1, "hydrated", hydrated[i].Title, hydrated[i].Theme, "router", "Design prompt hydrated", nil)
	}

	if parsed.Swarm.AnimationProfile != nil {
		if resolved, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: parsed.Swarm.AnimationProfile.ProfileID}); err == nil {
			parsed.Swarm.AnimationProfile = resolved
		}
	}
	specs := append([]taskLaunchSpec(nil), parsed.Launches...)
	for i := range specs {
		if parsed.Swarm.AnimationProfile != nil {
			specs[i].AnimationProfile = cloneTaskAnimationProfile(parsed.Swarm.AnimationProfile)
		}
		specs[i].AssignmentLabel = hydrated[i].Title
		if specs[i].SourceArguments == nil {
			specs[i].SourceArguments = map[string]any{}
		}
		specs[i].SourceArguments["swarm_theme"] = hydrated[i].Theme
		specs[i].SourceArguments["swarm_collection_title"] = hydrated[0].GroupTitle
		specs[i].SourceArguments["swarm_description"] = strings.TrimSpace(parsed.Description)
	}
	prepared := make([]taskLaunchPrepared, len(specs))
	for i, spec := range specs {
		run := managedDesignerArtifactContext(parent, callID, spec, i+1)
		if run == nil {
			return "", fmt.Errorf("direct designer swarm item %d cannot allocate a trusted artifact destination", i+1)
		}
		if parsed.Swarm.AnimationProfile != nil {
			run.AnimationProfile = cloneTaskAnimationProfile(parsed.Swarm.AnimationProfile)
		}
		run.ChildSessionID = parent.ID
		prepared[i] = taskLaunchPrepared{
			LaunchIndex:        i + 1,
			RequestedSubagent:  "designer_model",
			AssignmentLabel:    hydrated[i].Title,
			StreamKey:          spec.StreamKey,
			SwarmMode:          true,
			SwarmStrategy:      spec.SwarmStrategy,
			OutputMode:         taskOutputModeManaged,
			OutputRequirements: cloneTaskOutputRequirements(spec.OutputRequirements),
			ArtifactRunContext: run,
			SourceArtifact:     cloneTaskImageSourceArtifact(parsed.Swarm.SourceArtifact),
			ChildSession:       parent,
		}
	}

	// Resolve the account Designer model and runner
	accountScopeID := strings.TrimSpace(firstNonEmptyString(req.Principal.AccountScopeID, parent.AccountScopeID))
	resolvedAgent, _, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, accountScopeID, agentruntime.DesignerAgentID, "")
	if err != nil {
		return "", fmt.Errorf("resolve direct designer swarm agent: %w", err)
	}
	modelRuntime, err := resolveCompactModelRuntime(s.model, resolvedAgent)
	if err != nil {
		return "", fmt.Errorf("resolve direct designer model runtime: %w", err)
	}
	runner, ok := s.providers.GetRunner(modelRuntime.ProviderID)
	if !ok {
		return "", fmt.Errorf("configured Designer provider %q is not runnable", modelRuntime.ProviderID)
	}

	// Check if this is a targeted part revision of an existing Artifact V3 document
	if parsed.Swarm.ArtifactV3Source == nil && parsed.Swarm.SourceArtifact != nil {
		artID := strings.TrimSpace(parsed.Swarm.SourceArtifact.VariantID)
		if strings.HasPrefix(artID, "artifact-") {
			if repoProj, found, _ := s.sessions.Store().GetArtifactV3Repository(parent.AccountScopeID, parent.UserID, artID); found {
				targetIDs := []string{}
				if parsed.Swarm.SectionTarget != nil {
					targetIDs = []string{parsed.Swarm.SectionTarget.ID}
				}
				parsed.Swarm.ArtifactV3Source = &taskArtifactV3Source{
					SessionID:      parsed.Swarm.SourceArtifact.SessionID,
					ArtifactID:     artID,
					CommitOID:      repoProj.HeadCommitOID,
					TargetPartIDs:  targetIDs,
					RevisionIntent: "focused_parts",
				}
			}
		}
	}

	isIteration := parsed.Swarm.ArtifactV3Source != nil
	baseHTML := ""
	var iterationRefMap map[string]any
	scope := tool.WorkspaceScope{
		PrimaryPath: parent.WorkspacePath,
		Roots:       append([]string(nil), parent.TemporaryWorkspaceRoots...),
		SessionID:   parent.ID,
		Principal:   req.Principal,
	}
	if !scope.Principal.Valid() {
		scope.Principal = identity.Principal{
			Type:               identity.PrincipalTypeUser,
			UserID:             parent.UserID,
			AccountScopeID:     parent.AccountScopeID,
			SessionID:          parent.ID,
			AccountScopeSource: identity.AccountScopeSourceSession,
		}
	}

	if isIteration {
		src := parsed.Swarm.ArtifactV3Source
		commitOID := strings.TrimSpace(src.CommitOID)
		cleanCommit := strings.TrimPrefix(commitOID, "revision-")
		if commitOID == "" || strings.EqualFold(commitOID, "HEAD") || len(cleanCommit) != 40 {
			if repoProj, found, _ := s.sessions.Store().GetArtifactV3Repository(parent.AccountScopeID, parent.UserID, src.ArtifactID); found && repoProj.HeadCommitOID != "" {
				commitOID = repoProj.HeadCommitOID
			}
		}
		if !strings.HasPrefix(commitOID, "revision-") {
			commitOID = "revision-" + commitOID
		}
		iterationRefMap = map[string]any{
			"session_id":   src.SessionID,
			"artifact_id":  src.ArtifactID,
			"revision_ref": commitOID,
		}
		readHTML, _, readErr := s.tools.ReadManagedArtifactV3HTML(ctx, scope, iterationRefMap)
		if readErr != nil {
			return "", fmt.Errorf("read base Artifact V3 for revision: %w", readErr)
		}
		baseHTML = readHTML
	}

	durationMS := int64(6000)
	if parsed.Swarm.AnimationProfile != nil {
		durationMS = 9000
		if parsedDuration := extractDurationFromPrompt(parsed.Prompt); parsedDuration > 0 {
			durationMS = parsedDuration
		}
	}

	type designerResult struct {
		Reference *taskArtifactReference
		Parts     []pebblestore.SessionArtifactPart
		Err       error
		Attempts  int
	}
	results := make([]designerResult, len(prepared))
	sem := make(chan struct{}, directDesignerSwarmGenerationParallelism)
	var wg sync.WaitGroup

	for i := range prepared {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].Err = ctx.Err()
				return
			}

			run := *prepared[i].ArtifactRunContext
			run.RunID = fmt.Sprintf("%s:designer:%d", callID, i+1)

			emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "generating", hydrated[i].Title, hydrated[i].Theme, "designer_model", "Generating HTML design", nil)

			sysPrompt, userPrompt := composeDirectDesignerSwarmPrompt(
				parsed.Prompt,
				hydrated[i].Theme,
				parsed.Swarm.IterationControls,
				taskSwarmHydratedDelta{Index: i + 1, Title: hydrated[i].Title, Theme: hydrated[i].Theme},
				isIteration,
				baseHTML,
				parsed.Swarm.AnimationProfile,
				durationMS,
			)

			lineage := provideriface.ShortProviderLineageKey("task_swarm_designer", parent.ID, fmt.Sprintf("%s:%d", callID, i+1), modelRuntime.Preference.Model, modelRuntime.Preference.Thinking, hydrated[i].Theme)
			req := provideriface.Request{
				SessionID:                 parent.ID,
				ProviderLineageID:         lineage,
				ProviderCacheKey:          providerScopedKey("cache", lineage),
				SessionAffinityKey:        providerScopedKey("affinity", lineage),
				BoundaryReason:            "task_swarm_designer_one_shot",
				NativeContinuationAllowed: false,
				ForceFreshProviderContext: true,
				Instructions:              sysPrompt,
				Input: []map[string]any{
					{
						"role": "user",
						"content": []map[string]any{
							{"type": "input_text", "text": userPrompt},
						},
					},
				},
				ToolChoice:        "none",
				Tools:             nil,
				ParallelToolCalls: false,
			}
			req = modelRuntime.apply(req)

			callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			if reqPrincipal := scope.Principal; reqPrincipal.Valid() {
				callCtx = identity.ContextWithPrincipal(callCtx, reqPrincipal)
			}

			// Generate HTML with bounded repair retry
			var generatedHTML string
			var lastErr error
			isAnimation := parsed.Swarm.AnimationProfile != nil

			for attempt := 0; attempt <= directDesignerSwarmMaxRepairAttempts; attempt++ {
				if err := callCtx.Err(); err != nil {
					lastErr = err
					break
				}
				var outBuf strings.Builder
				outRunes := 0
				overLimit := false

				attemptReq := req
				if attempt > 0 {
					attemptReq.ProviderLineageID = provideriface.ShortProviderLineageKey("task_swarm_designer_repair", lineage, fmt.Sprintf("att_%d", attempt))
					attemptReq.BoundaryReason = "task_swarm_designer_repair"
				}

				_, runErr := runner.CreateResponseStreaming(callCtx, attemptReq, func(event provideriface.StreamEvent) {
					if overLimit {
						return
					}
					if event.Type == provideriface.StreamEventOutputTextDelta {
						outRunes += utf8.RuneCountInString(event.Delta)
						if outRunes > directDesignerSwarmMaxOutputRunes {
							overLimit = true
							cancel()
							return
						}
						outBuf.WriteString(event.Delta)
					}
				})
				if runErr != nil && !overLimit {
					lastErr = runErr
					break
				}

				rawOutput := outBuf.String()
				extracted := extractHTMLFromModelOutput(rawOutput)
				valErr := validateGeneratedDesignerHTML(extracted, isAnimation)
				if valErr == nil {
					generatedHTML = extracted
					lastErr = nil
					results[i].Attempts = attempt + 1
					break
				}

				lastErr = valErr
				if attempt < directDesignerSwarmMaxRepairAttempts {
					// In-memory repair prompt
					req.Input = append(req.Input,
						map[string]any{
							"role": "assistant",
							"content": []map[string]any{
								{"type": "output_text", "text": rawOutput},
							},
						},
						map[string]any{
							"role": "user",
							"content": []map[string]any{
								{"type": "input_text", "text": fmt.Sprintf("Your output failed validation: %s\nPlease fix this issue and return the complete corrected standalone HTML inside a ```html ... ``` block.", valErr.Error())},
							},
						},
					)
				}
			}

			if lastErr != nil || generatedHTML == "" {
				if lastErr == nil {
					lastErr = errors.New("empty HTML returned from Designer model")
				}
				results[i].Err = lastErr
				emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "failed", hydrated[i].Title, hydrated[i].Theme, "designer_model", boundedTaskLaunchReason(lastErr.Error()), nil)
				return
			}

			// Persist to Artifact V3
			var rawResult string
			var persistErr error

			if isIteration {
				rawResult, persistErr = s.tools.ReviseManagedHTMLArtifactV3(ctx, scope, fmt.Sprintf("%s:designer:%d", callID, i+1), iterationRefMap, nil, generatedHTML, run)
			} else {
				rawResult, persistErr = s.tools.CreateManagedHTMLArtifactV3(ctx, scope, fmt.Sprintf("%s:designer:%d", callID, i+1), hydrated[i].Title, generatedHTML, nil, parsed.Swarm.AnimationProfile, run)
			}
			if persistErr == nil {
				var checkObj map[string]any
				if jsonErr := json.Unmarshal([]byte(rawResult), &checkObj); jsonErr == nil {
					artV3, _ := checkObj["artifact_v3"].(map[string]any)
					st := asString(checkObj["status"])
					if artV3 != nil {
						if v3Status := asString(artV3["status"]); v3Status != "" {
							st = v3Status
						}
					}
					if st == "fixing" || st == "failed" || st == "error" {
						msg := ""
						if artV3 != nil {
							msg = asString(artV3["message"])
						}
						if msg == "" {
							msg = asString(checkObj["message"])
						}
						if msg == "" {
							msg = "artifact creation failed browser preview gate"
						}
						persistErr = errors.New(msg)
					}
				}
			}

			if persistErr != nil {
				results[i].Err = persistErr
				emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "failed", hydrated[i].Title, hydrated[i].Theme, "designer_model", boundedTaskLaunchReason(persistErr.Error()), nil)
				return
			}

			// Parse result to construct exact taskArtifactReference
			var respObj map[string]any
			_ = json.Unmarshal([]byte(rawResult), &respObj)

			artifactRefMap, _ := respObj["reference"].(map[string]any)
			artifactV3Map, _ := respObj["artifact_v3"].(map[string]any)
			if artifactRefMap == nil && artifactV3Map != nil {
				if innerRef, ok := artifactV3Map["reference"].(map[string]any); ok {
					artifactRefMap = innerRef
				}
			}

			artifactID := asString(artifactRefMap["artifact_id"])
			if artifactID == "" && artifactV3Map != nil {
				artifactID = asString(artifactV3Map["artifact_id"])
			}
			commitOID := asString(artifactRefMap["commit_oid"])
			if commitOID == "" && artifactV3Map != nil {
				commitOID = asString(artifactV3Map["commit_oid"])
			}
			if commitOID == "" && artifactV3Map != nil {
				if innerRef, ok := artifactV3Map["reference"].(map[string]any); ok {
					commitOID = asString(innerRef["commit_oid"])
					if commitOID == "" {
						commitOID = strings.TrimPrefix(asString(innerRef["revision_ref"]), "revision-")
					}
				}
			}
			if commitOID == "" || strings.HasPrefix(commitOID, "revision-") {
				commitOID = strings.TrimPrefix(commitOID, "revision-")
			}
			if commitOID == "" && artifactRefMap != nil {
				if revRef := asString(artifactRefMap["revision_ref"]); revRef != "" {
					commitOID = strings.TrimPrefix(revRef, "revision-")
				}
			}
			if commitOID == "" && artifactID != "" {
				authAccID := strings.TrimSpace(firstNonEmptyString(scope.Principal.AccountScopeID, parent.AccountScopeID))
				authUserID := strings.TrimSpace(firstNonEmptyString(scope.Principal.UserID, parent.UserID))
				if repoProj, found, _ := s.sessions.Store().GetArtifactV3Repository(authAccID, authUserID, artifactID); found {
					commitOID = repoProj.HeadCommitOID
				}
			}

			if commitOID == "" {
				results[i].Err = fmt.Errorf("artifact %s has no committed revision (browser preview gate failed)", artifactID)
				emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "failed", hydrated[i].Title, hydrated[i].Theme, "designer_model", results[i].Err.Error(), nil)
				return
			}

			var sessionParts []pebblestore.SessionArtifactPart
			if artifactV3Map != nil {
				if rawParts, ok := artifactV3Map["parts"].([]any); ok {
					for _, rp := range rawParts {
						if pm, ok := rp.(map[string]any); ok {
							sp := pebblestore.SessionArtifactPart{
								ID:    asString(pm["id"]),
								Label: asString(pm["label"]),
							}
							if loc, ok := pm["locator"].(map[string]any); ok {
								sp.Selector = asString(loc["value"])
								sp.Kind = asString(loc["kind"])
							}
							if temp, ok := pm["temporal"].(map[string]any); ok {
								sp.Kind = "temporal"
								sp.StartMs = int64(asUint64(temp["start_ms"]))
								sp.EndMs = int64(asUint64(temp["end_ms"]))
							}
							sessionParts = append(sessionParts, sp)
						}
					}
				}
			}
			if len(sessionParts) == 0 && artifactID != "" {
				revRef := "revision-" + commitOID
				if _, repoParts, readErr := s.tools.ReadManagedArtifactV3HTML(ctx, scope, map[string]any{"session_id": parent.ID, "artifact_id": artifactID, "revision_ref": revRef}); readErr == nil && len(repoParts) > 0 {
					for _, p := range repoParts {
						sp := pebblestore.SessionArtifactPart{
							ID:       p.ID,
							Label:    p.Label,
							Selector: p.Locator.Value,
						}
						if p.Temporal != nil {
							sp.Kind = "temporal"
							sp.StartMs = p.Temporal.StartMS
							sp.EndMs = p.Temporal.EndMS
						} else {
							sp.Kind = p.Locator.Kind
						}
						sessionParts = append(sessionParts, sp)
					}
				}
			}

			ref := &taskArtifactReference{
				SessionID:        parent.ID,
				ArtifactID:       artifactID,
				CommitOID:        commitOID,
				RevisionRef:      "revision-" + commitOID,
				Status:           "ready",
				Parts:            sessionParts,
				AnimationProfile: cloneTaskAnimationProfile(parsed.Swarm.AnimationProfile),
			}
			results[i].Reference = ref
			results[i].Parts = sessionParts

			emitDirectDesignerSwarmDelta(emit, step, callID, parsed.Action, description, len(prepared), i+1, "completed", hydrated[i].Title, hydrated[i].Theme, "designer_model", "Design artifact ready", ref)
		}()
	}
	wg.Wait()

	items := make([]map[string]any, len(results))
	references := make([]*taskArtifactReference, 0, len(results))
	failed := 0
	var firstErr error

	for i, res := range results {
		status := "ok"
		item := map[string]any{
			"index":                 i + 1,
			"title":                 hydrated[i].Title,
			"theme":                 hydrated[i].Theme,
			"stream_key":            prepared[i].StreamKey,
			"execution":             "router_to_designer_model",
			"child_session_created": false,
			"attempts":              res.Attempts,
			"one_shot":              res.Attempts == 1,
		}
		if res.Err != nil {
			status = "error"
			failed++
			item["error"] = boundedTaskLaunchReason(res.Err.Error())
			if firstErr == nil {
				firstErr = res.Err
			}
		} else {
			item["artifact_reference"] = res.Reference
			item["parts"] = res.Parts
			references = append(references, res.Reference)
		}
		item["status"] = status
		items[i] = item
	}

	overallStatus := "ok"
	if failed > 0 {
		overallStatus = "error"
	}

	payload := map[string]any{
		"tool":                  "task",
		"path_id":               "tool.task.designer_swarm.v1",
		"task_call_id":          callID,
		"action":                parsed.Action,
		"status":                overallStatus,
		"description":           description,
		"goal":                  description,
		"prompt":                prompt,
		"task_mode":             taskModeSwarm,
		"swarm_strategy":        parsed.Swarm.Strategy,
		"execution_format":      taskExecutionFormatDesignerDirect,
		"designer_count":        len(items),
		"artifacts":             items,
		"success_count":         len(items) - failed,
		"failed_count":          failed,
		"artifact_references":   references,
		"artifact_count":        len(references),
		"child_session_count":   0,
		"subagent_launch_count": 0,
		"details_truncated":     false,
	}
	if parsed.Swarm.ArtifactV3Source != nil {
		payload["artifact_v3_source"] = cloneTaskArtifactV3Source(parsed.Swarm.ArtifactV3Source)
	}

	encoded, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		return "", fmt.Errorf("marshal direct designer swarm result: %w", encodeErr)
	}
	if failed == len(items) && firstErr != nil {
		return string(encoded), firstErr
	}
	return string(encoded), nil
}
