package artifactv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	MotionSceneMediaType    = "application/vnd.swarm.artifact-v2.motion-scene+json"
	MotionBehaviorMediaType = "application/vnd.swarm.artifact-v2.motion-behavior+json"
	MotionCompilerVersion   = "artifact-v2-motion-compiler-1"
	MotionTemplateVersion   = "artifact-v2-motion-template-1"
	MotionValidatorVersion  = "artifact-v2-motion-validator-1"
	MotionRuntimeVersion    = "swarm.animation/v1"
	MotionPlayerVersion     = "swarm-player/v1"
)

type MotionSection struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	StartMS int    `json:"start_ms"`
	EndMS   int    `json:"end_ms"`
}

type MotionElement struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Fill     string `json:"fill,omitempty"`
	Color    string `json:"color,omitempty"`
	FontSize int    `json:"font_size,omitempty"`
	Radius   int    `json:"radius,omitempty"`
}

type MotionScene struct {
	Version    string          `json:"version"`
	DurationMS int             `json:"duration_ms"`
	FPS        int             `json:"fps"`
	Background string          `json:"background,omitempty"`
	Elements   []MotionElement `json:"elements"`
	Sections   []MotionSection `json:"sections"`
}

type MotionBehavior struct {
	TargetID string  `json:"target_id"`
	Property string  `json:"property"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	StartMS  int     `json:"start_ms"`
	EndMS    int     `json:"end_ms"`
	Easing   string  `json:"easing,omitempty"`
}

type MotionBehaviorModule struct {
	Version string           `json:"version"`
	Tracks  []MotionBehavior `json:"tracks"`
}

type motionTemplateData struct {
	DurationMS        int
	FPS               int
	Background        string
	SceneJSON         template.JS
	BehaviorJSON      template.JS
	AnimationManifest template.JS
	IterationManifest template.JS
	ElementHTML       template.HTML
}

// MotionCompiler is a V2-owned compiler. Designers supply typed scene and
// behavior JSON only; this compiler owns every host/runtime byte.
type MotionCompiler struct{}

func (MotionCompiler) Compile(ctx context.Context, input CompileInput) (CompileProduct, error) {
	parts := append([]CompilePart(nil), input.Parts...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Definition.Order < parts[j].Definition.Order })
	if !containsMotionPart(parts) {
		return (DeterministicCompiler{}).Compile(ctx, input)
	}
	var scene MotionScene
	var behavior MotionBehaviorModule
	scenePartID, behaviorPartID := "", ""
	for _, part := range parts {
		switch part.Revision.Blob.MediaType {
		case MotionSceneMediaType:
			if scenePartID != "" {
				return motionCompileFailure("motion_scene_duplicate", "manifest", part.Definition.ID, "The composition contains more than one motion scene.")
			}
			scenePartID = part.Definition.ID
			if err := decodeStrictJSON(part.Body, &scene); err != nil {
				return motionCompileFailure("motion_scene_syntax", "syntax", scenePartID, "The motion scene JSON is invalid or contains unknown fields.")
			}
		case MotionBehaviorMediaType:
			if behaviorPartID != "" {
				return motionCompileFailure("motion_behavior_duplicate", "manifest", part.Definition.ID, "The composition contains more than one behavior module.")
			}
			behaviorPartID = part.Definition.ID
			if err := decodeStrictJSON(part.Body, &behavior); err != nil {
				return motionCompileFailure("motion_behavior_syntax", "syntax", behaviorPartID, "The behavior module JSON is invalid or contains unknown fields.")
			}
		default:
			return motionCompileFailure("motion_part_type_unsupported", "syntax", part.Definition.ID, "This part type is not accepted by the motion compiler.")
		}
	}
	if scenePartID == "" {
		return motionCompileFailure("motion_scene_missing", "manifest", "", "The composition requires one typed motion scene.")
	}
	if behaviorPartID == "" {
		behavior = MotionBehaviorModule{Version: "artifact.motion.behavior/v1"}
	}
	if diagnostic := validateMotionScene(scene, behavior, scenePartID, behaviorPartID); diagnostic != nil {
		return CompileProduct{CompilerVersion: MotionCompilerVersion, TemplateVersion: MotionTemplateVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{*diagnostic}}, errors.New(diagnostic.Code)
	}
	sceneJSON, _ := json.Marshal(scene)
	behaviorJSON, _ := json.Marshal(behavior)
	animationManifest, _ := json.Marshal(map[string]any{"version": MotionRuntimeVersion, "duration_ms": scene.DurationMS, "fps": scene.FPS})
	iterationManifest, _ := json.Marshal(map[string]any{"version": "swarm.iteration/v1", "duration_ms": scene.DurationMS, "sections": scene.Sections})
	var elements strings.Builder
	for _, element := range scene.Elements {
		style := fmt.Sprintf("left:%dpx;top:%dpx;width:%dpx;height:%dpx;background:%s;color:%s;font-size:%dpx;border-radius:%dpx", element.X, element.Y, element.Width, element.Height, safeCSSColor(element.Fill, "transparent"), safeCSSColor(element.Color, "#ffffff"), element.FontSize, element.Radius)
		fmt.Fprintf(&elements, `<div class="scene-element kind-%s" id="%s" style="%s">%s</div>`, template.HTMLEscapeString(element.Kind), template.HTMLEscapeString(element.ID), template.HTMLEscapeString(style), template.HTMLEscapeString(element.Text))
	}
	data := motionTemplateData{DurationMS: scene.DurationMS, FPS: scene.FPS, Background: safeCSSColor(scene.Background, "#090b10"), SceneJSON: template.JS(sceneJSON), BehaviorJSON: template.JS(behaviorJSON), AnimationManifest: template.JS(animationManifest), IterationManifest: template.JS(iterationManifest), ElementHTML: template.HTML(elements.String())}
	var output bytes.Buffer
	if err := motionHTMLTemplate.Execute(&output, data); err != nil {
		return CompileProduct{}, err
	}
	return CompileProduct{Bytes: output.Bytes(), MediaType: "text/html", CompilerVersion: MotionCompilerVersion, TemplateVersion: MotionTemplateVersion, DurationMS: scene.DurationMS, FPS: scene.FPS, RepresentativeTimestampsMS: RepresentativeTimestamps(scene.DurationMS, scene.Sections)}, nil
}

func containsMotionPart(parts []CompilePart) bool {
	for _, part := range parts {
		if part.Revision.Blob.MediaType == MotionSceneMediaType || part.Revision.Blob.MediaType == MotionBehaviorMediaType {
			return true
		}
	}
	return false
}

func motionCompileFailure(code, phase, partID, message string) (CompileProduct, error) {
	d := safeDiagnostic(code, phase, "error", "repairable", message)
	d.PartID = partID
	return CompileProduct{CompilerVersion: MotionCompilerVersion, TemplateVersion: MotionTemplateVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{d}}, errors.New(code)
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("multiple JSON values")
	}
	return nil
}

func validateMotionScene(scene MotionScene, behavior MotionBehaviorModule, scenePartID, behaviorPartID string) *pebblestore.ArtifactV2Diagnostic {
	failure := func(code, phase, partID, locator, message string) *pebblestore.ArtifactV2Diagnostic {
		d := safeDiagnostic(code, phase, "error", "repairable", message)
		d.PartID, d.AuthoredLocator = partID, locator
		return &d
	}
	if scene.Version != "artifact.motion.scene/v1" || scene.DurationMS < 100 || scene.DurationMS > 120000 || (scene.FPS != 24 && scene.FPS != 30 && scene.FPS != 60) {
		return failure("motion_manifest_invalid", "manifest", scenePartID, "scene", "The scene version, duration, or frame rate is outside the motion policy.")
	}
	if len(scene.Elements) == 0 || len(scene.Elements) > 128 || len(scene.Sections) == 0 || len(scene.Sections) > 64 {
		return failure("motion_manifest_invalid", "manifest", scenePartID, "elements", "The scene element or section count is outside fixed bounds.")
	}
	ids := map[string]bool{}
	for index, element := range scene.Elements {
		locator := "elements[" + strconv.Itoa(index) + "]"
		if !safeIdentifier(element.ID) || ids[element.ID] || (element.Kind != "text" && element.Kind != "rect") {
			return failure("motion_element_invalid", "syntax", scenePartID, locator, "A scene element has an invalid ID or kind.")
		}
		ids[element.ID] = true
		if element.X < 0 || element.Y < 0 || element.Width < 1 || element.Height < 1 || element.X+element.Width > 1920 || element.Y+element.Height > 1080 {
			d := failure("motion_viewport_overflow", "viewport", scenePartID, locator, "A scene element exceeds the fixed 1920 by 1080 stage.")
			d.Bounds = fmt.Sprintf("x=%d,y=%d,w=%d,h=%d", element.X, element.Y, element.Width, element.Height)
			return d
		}
	}
	lastEnd := 0
	sectionIDs := map[string]bool{}
	for index, section := range scene.Sections {
		if !safeIdentifier(section.ID) || sectionIDs[section.ID] || section.Label == "" || section.StartMS < lastEnd || section.EndMS <= section.StartMS || section.EndMS > scene.DurationMS {
			return failure("motion_section_invalid", "manifest", scenePartID, "sections["+strconv.Itoa(index)+"]", "Motion sections must be ordered, unique, non-overlapping, and inside the timeline.")
		}
		sectionIDs[section.ID], lastEnd = true, section.EndMS
	}
	if behavior.Version != "" && behavior.Version != "artifact.motion.behavior/v1" {
		return failure("motion_behavior_invalid", "syntax", behaviorPartID, "version", "The behavior module version is unsupported.")
	}
	for index, track := range behavior.Tracks {
		locator := "tracks[" + strconv.Itoa(index) + "]"
		if !ids[track.TargetID] || (track.Property != "opacity" && track.Property != "translate_x" && track.Property != "translate_y" && track.Property != "scale" && track.Property != "rotate") || track.StartMS < 0 || track.EndMS <= track.StartMS || track.EndMS > scene.DurationMS || (track.Easing != "" && track.Easing != "linear" && track.Easing != "ease_in_out") {
			return failure("motion_behavior_invalid", "syntax", behaviorPartID, locator, "A behavior track has an invalid target, property, easing, or timeline range.")
		}
	}
	return nil
}

func safeIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || (index > 0 && r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

func safeCSSColor(value, fallback string) string {
	value = strings.TrimSpace(value)
	if len(value) == 7 && value[0] == '#' {
		for _, r := range value[1:] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return fallback
			}
		}
		return value
	}
	if value == "transparent" {
		return value
	}
	return fallback
}

func RepresentativeTimestamps(durationMS int, sections []MotionSection) []int {
	seen := map[int]bool{}
	out := []int{0, durationMS / 2, maxInt(0, durationMS-1)}
	for _, section := range sections {
		out = append(out, section.StartMS, (section.StartMS+section.EndMS)/2, maxInt(section.StartMS, section.EndMS-1))
	}
	result := out[:0]
	for _, timestamp := range out {
		if timestamp < 0 {
			timestamp = 0
		}
		if timestamp >= durationMS {
			timestamp = durationMS - 1
		}
		if !seen[timestamp] {
			seen[timestamp] = true
			result = append(result, timestamp)
		}
	}
	sort.Ints(result)
	return result
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// MotionRenderer is the only primitive the V2 validator and derivative service
// can use. Production adapts the allowlisted trusted Chrome renderer here.
type MotionRenderer interface {
	Preflight(context.Context, []byte, int, int) (MotionRenderResult, error)
	Render(context.Context, []byte, int, int) (MotionRenderResult, error)
}

type MotionFrame struct {
	Slot        string
	TimestampMS int
	PNG         []byte
}
type MotionRenderDiagnostic struct {
	Stage, Outcome, Selector, Pseudo string
	TimestampMS                      *int
	Bounds                           *htmlcapture.AnimationBounds
	Lifecycle                        []string
}
type MotionRenderResult struct {
	MP4, PreviewPNG []byte
	Frames          []MotionFrame
	Diagnostics     []MotionRenderDiagnostic
}

type TrustedMotionRenderer struct{ Renderer htmlcapture.AnimationRenderer }

func (r TrustedMotionRenderer) Preflight(ctx context.Context, source []byte, durationMS, fps int) (MotionRenderResult, error) {
	return r.invoke(ctx, source, durationMS, fps, false)
}
func (r TrustedMotionRenderer) Render(ctx context.Context, source []byte, durationMS, fps int) (MotionRenderResult, error) {
	return r.invoke(ctx, source, durationMS, fps, true)
}
func (r TrustedMotionRenderer) invoke(ctx context.Context, source []byte, durationMS, fps int, render bool) (MotionRenderResult, error) {
	if r.Renderer == nil {
		return MotionRenderResult{}, htmlcapture.NewError("animation_renderer_unavailable", "trusted HTML animation renderer is not configured")
	}
	req := htmlcapture.AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": source}, DurationMS: durationMS, FPS: fps, RequireLivePlayback: true}
	var result htmlcapture.AnimationResult
	var err error
	if render {
		result, err = r.Renderer.RenderAnimation(ctx, req)
	} else {
		result, err = r.Renderer.PreflightAnimation(ctx, req)
	}
	out := MotionRenderResult{MP4: result.MP4, PreviewPNG: result.PreviewPNG}
	for _, frame := range result.InspectionFrames {
		out.Frames = append(out.Frames, MotionFrame{Slot: frame.Slot, TimestampMS: frame.TimestampMS, PNG: frame.PNG})
	}
	for _, diagnostic := range result.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, MotionRenderDiagnostic{Stage: diagnostic.Stage, Outcome: diagnostic.Outcome, Selector: diagnostic.Selector, Pseudo: diagnostic.Pseudo, TimestampMS: diagnostic.TimestampMS, Bounds: diagnostic.Bounds, Lifecycle: append([]string(nil), diagnostic.Lifecycle...)})
	}
	return out, err
}

// MotionValidator runs the trusted renderer only after the V2 compiler has
// accepted typed input. It returns bounded diagnostics bound to the exact build.
type MotionValidator struct{ Renderer MotionRenderer }

func (v MotionValidator) Validate(ctx context.Context, input ValidationInput) (ValidationProduct, error) {
	if input.Product.CompilerVersion != MotionCompilerVersion {
		return (DeterministicValidator{}).Validate(ctx, input)
	}
	scene, scenePartID, err := motionSceneFromCompileInput(input)
	if err != nil {
		d := safeDiagnostic("motion_scene_missing", "manifest", "error", "repairable", "The exact composition no longer contains a valid motion scene.")
		d.PartID = scenePartID
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationInvalid, ValidatorVersion: MotionValidatorVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{d}}, nil
	}
	if v.Renderer == nil {
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationFailedToRun, ValidatorVersion: MotionValidatorVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("animation_renderer_unavailable", "infrastructure", "error", "infrastructure", "Trusted animation validation is unavailable.")}}, errors.New("trusted motion renderer is unavailable")
	}
	result, renderErr := v.Renderer.Preflight(ctx, input.Product.Bytes, scene.DurationMS, scene.FPS)
	diagnostics := mapMotionDiagnostics(result.Diagnostics, input.Parts)
	if renderErr != nil {
		var captureErr *htmlcapture.Error
		code := "animation_renderer_failed"
		if errors.As(renderErr, &captureErr) {
			code = captureErr.Code
		}
		if len(diagnostics) == 0 {
			diagnostic := safeDiagnostic(code, motionFailurePhase(code), "error", motionRetryClass(code), motionSafeMessage(code))
			diagnostic.PartID = scenePartID
			diagnostics = []pebblestore.ArtifactV2Diagnostic{diagnostic}
		}
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationInvalid, ValidatorVersion: MotionValidatorVersion, RendererSnapshot: "trusted-chrome-animation-1", Diagnostics: diagnostics, EvidenceDigests: frameDigests(result)}, nil
	}
	return ValidationProduct{Status: pebblestore.ArtifactV2ValidationValid, ValidatorVersion: MotionValidatorVersion, RendererSnapshot: "trusted-chrome-animation-1", Diagnostics: diagnostics, EvidenceDigests: frameDigests(result)}, nil
}

func motionSceneFromCompileInput(input ValidationInput) (MotionScene, string, error) {
	for _, part := range input.Parts {
		if part.Revision.Blob.MediaType == MotionSceneMediaType {
			var scene MotionScene
			err := decodeStrictJSON(part.Body, &scene)
			return scene, part.Definition.ID, err
		}
	}
	return MotionScene{}, "", errors.New("motion scene missing")
}

func mapMotionDiagnostics(input []MotionRenderDiagnostic, parts []CompilePart) []pebblestore.ArtifactV2Diagnostic {
	partID := ""
	for _, part := range parts {
		if part.Revision.Blob.MediaType == MotionSceneMediaType {
			partID = part.Definition.ID
			break
		}
	}
	out := make([]pebblestore.ArtifactV2Diagnostic, 0, len(input))
	for _, source := range input {
		d := safeDiagnostic("animation_"+safeToken(source.Outcome, "diagnostic"), firstNonEmpty(source.Stage, "frame"), "error", "repairable", "The trusted renderer found a repairable animation issue.")
		d.PartID = partID
		d.AuthoredLocator = bounded(source.Selector, 256)
		if source.TimestampMS != nil {
			d.FrameSlotOrTime = strconv.Itoa(*source.TimestampMS) + "ms"
		}
		if source.Bounds != nil {
			d.Bounds = fmt.Sprintf("left=%.1f,top=%.1f,right=%.1f,bottom=%.1f", source.Bounds.Left, source.Bounds.Top, source.Bounds.Right, source.Bounds.Bottom)
		}
		for _, stage := range source.Lifecycle {
			d.PreservationProofs = append(d.PreservationProofs, "lifecycle:"+bounded(stage, 64))
		}
		out = append(out, d)
	}
	return safeDiagnostics(out)
}

func motionFailurePhase(code string) string {
	if strings.Contains(code, "manifest") || strings.Contains(code, "bootstrap") || strings.Contains(code, "ready") || strings.Contains(code, "seek") || strings.Contains(code, "playback") {
		return "lifecycle"
	}
	if strings.Contains(code, "viewport") {
		return "viewport"
	}
	if strings.Contains(code, "frame") || strings.Contains(code, "png") {
		return "frame"
	}
	return "infrastructure"
}
func motionRetryClass(code string) string {
	if strings.Contains(code, "unavailable") || strings.Contains(code, "timeout") || strings.Contains(code, "encoder") {
		return "infrastructure"
	}
	return "repairable"
}
func motionSafeMessage(code string) string {
	switch motionFailurePhase(code) {
	case "lifecycle":
		return "The compiled animation did not satisfy the server-owned lifecycle contract."
	case "viewport":
		return "The compiled animation exceeded the fixed stage."
	case "frame":
		return "A representative animation frame was invalid or unstable."
	default:
		return "Trusted animation validation could not complete."
	}
}
func frameDigests(result MotionRenderResult) []string {
	out := []string{}
	for _, frame := range result.Frames {
		if len(frame.PNG) > 0 {
			sum := sha256.Sum256(frame.PNG)
			out = append(out, hex.EncodeToString(sum[:]))
		}
	}
	if len(result.PreviewPNG) > 0 {
		sum := sha256.Sum256(result.PreviewPNG)
		out = append(out, hex.EncodeToString(sum[:]))
	}
	sort.Strings(out)
	return out
}

func sourceDigests(parts []CompilePart) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, part.Revision.Blob.DigestSHA256)
	}
	sort.Strings(out)
	return out
}

var motionHTMLTemplate = template.Must(template.New("motion-v2").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><script id="swarm-animation-manifest" type="application/json">{{.AnimationManifest}}</script><script id="swarm-iteration-manifest" type="application/json">{{.IterationManifest}}</script><script>(function(){"use strict";const D={{.DurationMS}},F={{.FPS}},scene={{.SceneJSON}},behavior={{.BehaviorJSON}};let stopped=false,start=0,raf=0;const clamp=t=>Math.max(0,Math.min(D-1,Math.round(t)));function ease(p,name){if(name==='ease_in_out')return p<.5?2*p*p:1-Math.pow(-2*p+2,2)/2;return p}function renderAt(value){const t=clamp(value);document.documentElement.dataset.swarmAnimationTimeMs=String(t);for(const track of behavior.tracks||[]){const node=document.getElementById(track.target_id);if(!node)continue;const p=ease(Math.max(0,Math.min(1,(t-track.start_ms)/(track.end_ms-track.start_ms))),track.easing);const v=track.from+(track.to-track.from)*p;if(track.property==='opacity')node.style.opacity=String(v);else{node.dataset[track.property]=String(v);const x=Number(node.dataset.translate_x||0),y=Number(node.dataset.translate_y||0),s=Number(node.dataset.scale||1),r=Number(node.dataset.rotate||0);node.style.transform='translate('+x+'px,'+y+'px) scale('+s+') rotate('+r+'deg)'}}return t}function tick(now){if(stopped||document.hidden)return;if(!start)start=now;renderAt((now-start)%D);raf=requestAnimationFrame(tick)}function play(){if(stopped||document.hidden||matchMedia('(prefers-reduced-motion: reduce)').matches)return;if(!raf){start=performance.now()-Number(document.documentElement.dataset.swarmAnimationTimeMs||0);raf=requestAnimationFrame(tick)}}const runtime={version:'swarm.animation/v1',ready:async()=>({duration_ms:D,fps:F}),seek:async t=>{stopped=true;if(raf)cancelAnimationFrame(raf);raf=0;const time=renderAt(t);return{time_ms:time}},stop:async()=>{stopped=true;if(raf)cancelAnimationFrame(raf);raf=0;return{time_ms:Number(document.documentElement.dataset.swarmAnimationTimeMs||0)}}};if(globalThis.__SWARM_ANIMATION_BIND__)globalThis.__SWARM_ANIMATION_BIND__(runtime);else globalThis.__SWARM_ANIMATION_V1__=runtime;function bridge(event){const request=event.data;if(!request||request.protocol!=='swarm-player/v1'||!request.id)return;let result;if(request.method==='describe'){result={{.IterationManifest}};stopped=false;play()}else if(request.method==='seek'){runtime.seek(request.params&&request.params.time_ms||0).then(value=>event.source.postMessage({protocol:'swarm-player/v1',id:request.id,ok:true,result:value},'*'));return}else if(request.method==='stop'){runtime.stop().then(value=>event.source.postMessage({protocol:'swarm-player/v1',id:request.id,ok:true,result:value},'*'));return}else return;event.source.postMessage({protocol:'swarm-player/v1',id:request.id,ok:true,result},'*')}addEventListener('message',bridge);addEventListener('DOMContentLoaded',()=>{renderAt(0);play()});document.addEventListener('visibilitychange',()=>{if(document.hidden){if(raf)cancelAnimationFrame(raf);raf=0}else{stopped=false;play()}})})();</script><style>html,body{width:1920px;height:1080px;margin:0;overflow:hidden;background:{{.Background}}}*{box-sizing:border-box}#stage{position:relative;width:1920px;height:1080px;overflow:hidden}.scene-element{position:absolute;display:flex;align-items:center;justify-content:center;transform-origin:center}</style></head><body><main id="stage">{{.ElementHTML}}</main></body></html>`))
