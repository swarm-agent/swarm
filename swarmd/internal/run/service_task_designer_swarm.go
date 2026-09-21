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

func validateGeneratedDesignerHTML(html string, isAnimation bool, requiredParts []string) error {
	trimmed := strings.TrimSpace(html)
	if trimmed == "" {
		return errors.New("model returned empty output")
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "<html") || !strings.Contains(lower, "</html>") {
		return errors.New("output does not contain a valid <html>...</html> document")
	}
	if isAnimation {
		if !strings.Contains(trimmed, `id="swarm-animation-manifest"`) && !strings.Contains(trimmed, `id='swarm-animation-manifest'`) {
			return errors.New("missing <script id=\"swarm-animation-manifest\" type=\"application/json\"> manifest")
		}
		if !strings.Contains(trimmed, "__SWARM_ANIMATION_V1__") {
			return errors.New("missing window.__SWARM_ANIMATION_V1__ animation bridge")
		}
		if !strings.Contains(trimmed, `"swarm.animation/v1"`) && !strings.Contains(trimmed, `'swarm.animation/v1'`) {
			return errors.New("__SWARM_ANIMATION_V1__ must declare version: \"swarm.animation/v1\"")
		}
		if !strings.Contains(trimmed, "ready") || !strings.Contains(trimmed, "seek") {
			return errors.New("__SWARM_ANIMATION_V1__ must implement both ready() and seek(time_ms)")
		}
	}
	for _, partID := range requiredParts {
		if strings.TrimSpace(partID) == "" {
			continue
		}
		pattern := `(?i)\bid\s*=\s*["']?` + regexp.QuoteMeta(strings.TrimSpace(partID)) + `(?:\s|["'>]|$)`
		if matched, _ := regexp.MatchString(pattern, trimmed); !matched {
			return fmt.Errorf("missing required HTML element with id=%q for part %q", partID, partID)
		}
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

func extractPartsFromPrompt(prompt string, durationMS int64) []pebblestore.SessionArtifactPart {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	// 1. Explicit timings: e.g. Part 1 (0 - 3000ms) or Scene 1 (0 - 3000ms)
	reTimed := regexp.MustCompile(`(?i)(?:^|\n|\b)(?:[0-9]+\.\s*)?(?:part|scene)\s*([0-9]+|[a-zA-Z0-9_-]+)[^\n\(]*?\(\s*([0-9]+)\s*[-–]\s*([0-9]+)\s*ms\s*\)`)
	matchesTimed := reTimed.FindAllStringSubmatch(prompt, -1)
	if len(matchesTimed) >= 2 {
		var parts []pebblestore.SessionArtifactPart
		for _, m := range matchesTimed {
			numStr := m[1]
			s, _ := strconv.ParseInt(m[2], 10, 64)
			e, _ := strconv.ParseInt(m[3], 10, 64)
			id := fmt.Sprintf("part-%s", numStr)
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(numStr) {
				id = strings.ToLower(numStr)
			}
			parts = append(parts, pebblestore.SessionArtifactPart{
				ID:       id,
				Label:    fmt.Sprintf("Part %s", numStr),
				Kind:     "temporal",
				StartMs:  s,
				EndMs:    e,
				Selector: "#" + id,
			})
		}
		return parts
	}

	// 2. Named parts: e.g. Part 1: boot, Part 2: vortex, Part 3: online or Scene 1: Intro
	reNamed := regexp.MustCompile(`(?i)(?:part|scene)\s*([0-9]+)\s*[:=]\s*([a-zA-Z0-9_-]+)`)
	matchesNamed := reNamed.FindAllStringSubmatch(prompt, -1)
	if len(matchesNamed) >= 2 {
		step := durationMS / int64(len(matchesNamed))
		var parts []pebblestore.SessionArtifactPart
		for i, m := range matchesNamed {
			id := strings.ToLower(m[2])
			start := int64(i) * step
			end := int64(i+1) * step
			if i == len(matchesNamed)-1 {
				end = durationMS
			}
			parts = append(parts, pebblestore.SessionArtifactPart{
				ID:       id,
				Label:    fmt.Sprintf("Part %s - %s", m[1], m[2]),
				Kind:     "temporal",
				StartMs:  start,
				EndMs:    end,
				Selector: "#" + id,
			})
		}
		return parts
	}

	// 3. General count: e.g. "3-part", "3 part", "3 parts"
	reCount := regexp.MustCompile(`(?i)(\d+)\s*[- ]\s*parts?`)
	if match := reCount.FindStringSubmatch(prompt); len(match) > 1 {
		if n, err := strconv.Atoi(match[1]); err == nil && n >= 2 && n <= 16 {
			step := durationMS / int64(n)
			var parts []pebblestore.SessionArtifactPart
			for i := 0; i < n; i++ {
				start := int64(i) * step
				end := int64(i+1) * step
				if i == n-1 {
					end = durationMS
				}
				id := fmt.Sprintf("part-%d", i+1)
				parts = append(parts, pebblestore.SessionArtifactPart{
					ID:       id,
					Label:    fmt.Sprintf("Part %d", i+1),
					Kind:     "temporal",
					StartMs:  start,
					EndMs:    end,
					Selector: "#" + id,
				})
			}
			return parts
		}
	}

	return nil
}

func extractPartsFromHTML(html string, durationMS int64) []pebblestore.SessionArtifactPart {
	if strings.TrimSpace(html) == "" {
		return nil
	}
	re := regexp.MustCompile(`(?i)\bid=["']((?:part|scene)[-_]?[0-9]+)["']`)
	matches := re.FindAllStringSubmatch(html, -1)
	if len(matches) < 2 {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	for _, m := range matches {
		id := strings.ToLower(m[1])
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 {
		return nil
	}
	step := durationMS / int64(len(ids))
	var parts []pebblestore.SessionArtifactPart
	for i, id := range ids {
		start := int64(i) * step
		end := int64(i+1) * step
		if i == len(ids)-1 {
			end = durationMS
		}
		parts = append(parts, pebblestore.SessionArtifactPart{
			ID:       id,
			Label:    fmt.Sprintf("Part %d", i+1),
			Kind:     "temporal",
			StartMs:  start,
			EndMs:    end,
			Selector: "#" + id,
		})
	}
	return parts
}

func ensureAnimationSeekSceneID(html string, parts []pebblestore.SessionArtifactPart, durationMS int64) string {
	if strings.TrimSpace(html) == "" {
		return html
	}
	type partScene struct {
		ID      string `json:"id"`
		StartMs int64  `json:"start_ms"`
		EndMs   int64  `json:"end_ms"`
	}
	var scenes []partScene
	for _, p := range parts {
		if p.EndMs > p.StartMs {
			scenes = append(scenes, partScene{ID: p.ID, StartMs: p.StartMs, EndMs: p.EndMs})
		}
	}
	scenesJSON, _ := json.Marshal(scenes)

	// Guarantee that each declared part element exists in the HTML body so that
	// selectors like #part-1 always resolve and are visible during Chromedp preview audit.
	for _, p := range parts {
		pattern := `(?i)\bid\s*=\s*["']?` + regexp.QuoteMeta(p.ID) + `(?:\s|["'>]|$)`
		if matched, _ := regexp.MatchString(pattern, html); !matched {
			injectElem := fmt.Sprintf(`<section id="%s" data-swarm-part="%s" style="position:absolute;left:0;top:0;width:100%%;height:100%%;pointer-events:none"></section>`, p.ID, p.ID)
			if idx := strings.Index(strings.ToLower(html), "<body"); idx >= 0 {
				if endBodyTag := strings.Index(html[idx:], ">"); endBodyTag >= 0 {
					insertPos := idx + endBodyTag + 1
					html = html[:insertPos] + "\n" + injectElem + html[insertPos:]
				}
			}
		}
	}

	helperScript := fmt.Sprintf(`<script data-swarm-capture-ui>(()=>{
const scenes = %s;
function getSceneId(t) {
  for (const s of scenes) {
    if (t >= s.start_ms && t <= s.end_ms) return s.id;
  }
  return scenes.length > 0 ? scenes[0].id : "";
}
globalThis.__swarmGetSceneId = getSceneId;

let isPaused = false;
const activeRAFs = new Set();
const origRAF = typeof window !== "undefined" && window.requestAnimationFrame ? window.requestAnimationFrame.bind(window) : null;
const origCAF = typeof window !== "undefined" && window.cancelAnimationFrame ? window.cancelAnimationFrame.bind(window) : null;

if (origRAF && origCAF) {
  window.requestAnimationFrame = function(cb) {
    if (isPaused) return 0;
    const id = origRAF(function(ts) {
      activeRAFs.delete(id);
      if (!isPaused) cb(ts);
    });
    activeRAFs.add(id);
    return id;
  };

  window.cancelAnimationFrame = function(id) {
    activeRAFs.delete(id);
    origCAF(id);
  };
}

function freezeLoops() {
  isPaused = true;
  if (origCAF) {
    for (const id of activeRAFs) {
      origCAF(id);
    }
    activeRAFs.clear();
  }
}

function wrapController(api) {
  if (!api || api.__swarm_wrapped__) return;
  api.__swarm_wrapped__ = true;
  const origSeek = api.seek;
  const origPause = api.pause;
  const origPlay = api.play;

  if (origPlay) {
    api.play = async function() {
      isPaused = false;
      return await origPlay();
    };
  }

  api.pause = async function() {
    freezeLoops();
    if (typeof origPause === "function") await origPause();
  };

  api.seek = async function(time_ms) {
    freezeLoops();
    let res;
    if (typeof origSeek === "function") {
      res = await origSeek(time_ms);
    }
    freezeLoops();
    if (!res || typeof res !== "object") {
      res = { time_ms: time_ms };
    }
    if (!res.scene_id && typeof getSceneId === "function") {
      res.scene_id = getSceneId(time_ms);
    }
    return res;
  };
}

let _ctrl = globalThis.__SWARM_ANIMATION_V1__;
if (_ctrl) wrapController(_ctrl);
try {
  Object.defineProperty(globalThis, "__SWARM_ANIMATION_V1__", {
    configurable: true,
    enumerable: true,
    get: () => _ctrl,
    set: (v) => {
      _ctrl = v;
      if (v) wrapController(v);
    }
  });
} catch (_) {}

if (typeof window !== "undefined") {
  window.addEventListener("DOMContentLoaded", () => { if (globalThis.__SWARM_ANIMATION_V1__) wrapController(globalThis.__SWARM_ANIMATION_V1__); });
  window.addEventListener("load", () => { if (globalThis.__SWARM_ANIMATION_V1__) wrapController(globalThis.__SWARM_ANIMATION_V1__); });
}
})();</script>`, string(scenesJSON))

	if idx := strings.Index(strings.ToLower(html), "</head>"); idx >= 0 {
		html = html[:idx] + helperScript + "\n" + html[idx:]
	} else if idx := strings.Index(strings.ToLower(html), "<body"); idx >= 0 {
		html = html[:idx] + helperScript + "\n" + html[idx:]
	} else {
		html = helperScript + "\n" + html
	}

	reReturn := regexp.MustCompile(`(?i)return\s*\{\s*time_ms\s*:\s*([^,}]+)`)
	html = reReturn.ReplaceAllStringFunc(html, func(m string) string {
		sub := reReturn.FindStringSubmatch(m)
		if len(sub) > 1 {
			val := strings.TrimSpace(sub[1])
			return fmt.Sprintf("return { scene_id: globalThis.__swarmGetSceneId(%s), time_ms: %s", val, val)
		}
		return m
	})

	return html
}

func composeDirectDesignerSwarmPrompt(parentPrompt, baseTheme string, controls *taskSwarmIterationControls, delta taskSwarmHydratedDelta, isIteration bool, baseHTML string, targetPartID string, parts []pebblestore.SessionArtifactPart, profile *pebblestore.SessionArtifactAnimationProfile, durationMS int64) (string, string) {
	var sys strings.Builder
	sys.WriteString(`You are Designer, Swarm's compiled UI, animation, and multi-part HTML component engine.
Your assignment is to generate a complete, production-ready, standalone single-file HTML5 document.

CRITICAL REQUIREMENTS:
1. Complete Standalone HTML5:
   - Must be a complete HTML document starting with <!DOCTYPE html> and closing with </html>.
   - All styling in <style>, all scripts in <script>.
   - Zero external CDNs, remote scripts, or remote stylesheet links. The document must work fully offline.
   - High-contrast, modern visual character (e.g. deep spatial background, luminous accents, clean typography).

2. Animation Contract & Controller Implementation Pattern:
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
       // Synchronously calculate all particle positions, geometry transforms, opacities, and canvas/SVG state strictly from time_ms.
       // Draw directly to canvas or update SVG DOM.
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
         return { time_ms: time_ms, scene_id: getActivePartId(time_ms) };
       }
     };

   - CRITICAL CHROMEDP STABILITY AUDIT REQUIREMENT:
     The preview validation system captures two separate screenshots 100ms apart during inspection to audit frame stability.
     When seek(time_ms) or pause() is called, ALL continuous animation loops (requestAnimationFrame, setInterval, setTimeout) MUST BE STOPPED IMMEDIATELY.
     The canvas and DOM must remain 100% static and motionless at time_ms until playback resumes.
     Never leave requestAnimationFrame running in the background after seek(), or the preview audit will fail with "capture state changed during the fixed stability audit".

3. Multi-Part Architecture & DOM Region IDs:
   - Each declared chapter/part must be represented by an HTML container element (e.g. <section id="..."> or <div id="...">) with an id attribute exactly matching the part ID.
   - For example:
     <main id="swarm-animation-stage" style="position:relative;width:1920px;height:1080px;overflow:hidden;background:#020205;">
       <canvas id="animation-canvas" width="1920" height="1080" style="position:absolute;left:0;top:0;width:100%;height:100%;"></canvas>
       <section id="part-1" class="scene-container" style="position:absolute;inset:0;pointer-events:none;"></section>
       <section id="part-2" class="scene-container" style="position:absolute;inset:0;pointer-events:none;"></section>
       <section id="part-3" class="scene-container" style="position:absolute;inset:0;pointer-events:none;"></section>
     </main>
   - CRITICAL: When active at time_ms, the part container corresponding to the active part must be present in the DOM, positioned inside the 1920x1080 viewport, and visible (width > 0, height > 0, opacity > 0, display not none).

4. Output Format:
   - Return ONLY the complete single-file HTML document wrapped in a single ` + "```html ... ```" + ` block.
   - Do NOT include any conversational preamble or commentary outside the code block.`)

	var user strings.Builder
	if isIteration {
		user.WriteString("TARGETED SINGLE-PART REVISION:\n")
		user.WriteString(fmt.Sprintf("Target Part to modify: %s\n\n", targetPartID))
		user.WriteString("Revision Instructions:\n")
		user.WriteString(strings.TrimSpace(parentPrompt))
		if baseTheme != "" {
			user.WriteString("\nSpecialized Theme: " + baseTheme)
		}
		if delta.Role != "" {
			user.WriteString("\nCreative Direction: " + delta.Role)
		}
		user.WriteString("\n\nCRITICAL CONSTRAINTS:\n")
		user.WriteString(fmt.Sprintf("1. Modify ONLY the visual layout, styles, canvas rendering, or logic corresponding to the target part %q.\n", targetPartID))
		user.WriteString("2. You MUST preserve all non-target parts, their DOM IDs, their structure, and their rendering logic exactly as they are.\n")
		user.WriteString("3. You MUST preserve the #swarm-animation-manifest and window.__SWARM_ANIMATION_V1__ controller.\n")
		user.WriteString("4. Output the complete updated standalone HTML in a ```html ... ``` block.\n\n")
		user.WriteString("Existing Complete HTML Document:\n```html\n")
		user.WriteString(baseHTML)
		user.WriteString("\n```\n")
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
		if len(parts) > 0 {
			user.WriteString("\n\nRequired Multi-Part Timeline:\n")
			for _, p := range parts {
				timing := ""
				if p.EndMs > p.StartMs {
					timing = fmt.Sprintf(" (%dms - %dms)", p.StartMs, p.EndMs)
				}
				user.WriteString(fmt.Sprintf("- Part ID %q: %s%s\n", p.ID, p.Label, timing))
			}
			user.WriteString("Ensure each Part ID has a matching element in HTML, e.g. <section id=\"...\">.\n")
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
	targetPartID := ""
	var baseParts []pebblestore.SessionArtifactPart
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
		readHTML, readParts, readErr := s.tools.ReadManagedArtifactV3HTML(ctx, scope, iterationRefMap)
		if readErr != nil {
			return "", fmt.Errorf("read base Artifact V3 for targeted revision: %w", readErr)
		}
		baseHTML = readHTML
		baseParts = make([]pebblestore.SessionArtifactPart, len(readParts))
		for idx, p := range readParts {
			baseParts[idx] = pebblestore.SessionArtifactPart{
				ID:       p.ID,
				Label:    p.Label,
				Selector: p.Locator.Value,
			}
			if p.Temporal != nil {
				baseParts[idx].Kind = "temporal"
				baseParts[idx].StartMs = p.Temporal.StartMS
				baseParts[idx].EndMs = p.Temporal.EndMS
			}
		}
		if len(src.TargetPartIDs) > 0 {
			targetPartID = src.TargetPartIDs[0]
		} else if parsed.Swarm.SectionTarget != nil {
			targetPartID = parsed.Swarm.SectionTarget.ID
		}
		if targetPartID == "" && len(baseParts) > 0 {
			targetPartID = baseParts[0].ID
		}
	}

	// Prepare parts list for initial creation
	var initialParts []pebblestore.SessionArtifactPart
	if !isIteration {
		if len(parsed.Swarm.SectionTargets) > 0 {
			for _, st := range parsed.Swarm.SectionTargets {
				initialParts = append(initialParts, pebblestore.SessionArtifactPart{
					ID:       st.ID,
					Label:    st.Label,
					Kind:     st.Kind,
					StartMs:  st.StartMs,
					EndMs:    st.EndMs,
					Selector: "#" + st.ID,
				})
			}
		} else if parsed.Swarm.SectionTarget != nil {
			st := parsed.Swarm.SectionTarget
			initialParts = append(initialParts, pebblestore.SessionArtifactPart{
				ID:       st.ID,
				Label:    st.Label,
				Kind:     st.Kind,
				StartMs:  st.StartMs,
				EndMs:    st.EndMs,
				Selector: "#" + st.ID,
			})
		}
	}

	durationMS := int64(6000)
	if parsed.Swarm.AnimationProfile != nil {
		durationMS = 9000
		if parsedDuration := extractDurationFromPrompt(parsed.Prompt); parsedDuration > 0 {
			durationMS = parsedDuration
		}
		if len(initialParts) == 0 && !isIteration {
			initialParts = extractPartsFromPrompt(parsed.Prompt, durationMS)
		}
		if len(initialParts) > 0 {
			var maxEnd int64
			for _, p := range initialParts {
				if p.EndMs > maxEnd {
					maxEnd = p.EndMs
				}
			}
			if maxEnd > 0 {
				durationMS = maxEnd
			}
		}
	}

	// Gather required parts for pre-flight checking
	var requiredPartIDs []string
	if isIteration {
		for _, bp := range baseParts {
			requiredPartIDs = append(requiredPartIDs, bp.ID)
		}
	} else {
		for _, ip := range initialParts {
			requiredPartIDs = append(requiredPartIDs, ip.ID)
		}
	}

	type designerResult struct {
		Reference *taskArtifactReference
		Parts     []pebblestore.SessionArtifactPart
		Err       error
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
				targetPartID,
				initialParts,
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
				valErr := validateGeneratedDesignerHTML(extracted, isAnimation, requiredPartIDs)
				if valErr == nil {
					generatedHTML = extracted
					lastErr = nil
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
				generatedHTML = ensureAnimationSeekSceneID(generatedHTML, baseParts, durationMS)
				rawResult, persistErr = s.tools.ReviseManagedHTMLArtifactV3(ctx, scope, fmt.Sprintf("%s:designer:%d", callID, i+1), iterationRefMap, []string{targetPartID}, generatedHTML, run)
			} else {
				effectiveParts := initialParts
				if len(effectiveParts) == 0 && parsed.Swarm.AnimationProfile != nil {
					effectiveParts = extractPartsFromHTML(generatedHTML, durationMS)
				}
				generatedHTML = ensureAnimationSeekSceneID(generatedHTML, effectiveParts, durationMS)
				var partsList []map[string]any
				for _, ip := range effectiveParts {
					partMap := map[string]any{
						"id":       ip.ID,
						"label":    ip.Label,
						"selector": ip.Selector,
					}
					if ip.Kind == "temporal" {
						partMap["kind"] = "temporal"
						partMap["start_ms"] = ip.StartMs
						partMap["end_ms"] = ip.EndMs
					}
					partsList = append(partsList, partMap)
				}
				rawResult, persistErr = s.tools.CreateManagedHTMLArtifactV3(ctx, scope, fmt.Sprintf("%s:designer:%d", callID, i+1), hydrated[i].Title, generatedHTML, partsList, parsed.Swarm.AnimationProfile, run)
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
