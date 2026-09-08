package artifactv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videocomposition"
)

const (
	StoryboardSectionMediaType = "application/vnd.swarm.artifact-v2.storyboard-section+json"
	StoryboardCatalogMediaType = "application/vnd.swarm.artifact-v2.storyboard-catalog+json"
	StoryboardCompilerVersion  = "artifact-v2-storyboard-compiler-1"
	StoryboardTemplateVersion  = "artifact-v2-storyboard-template-1"
	StoryboardValidatorVersion = "artifact-v2-storyboard-validator-1"
	StoryboardConstruction     = "artifact-v2-storyboard-compose-1"
	ArtifactKindStoryboard     = "storyboard"
)

type StoryboardSection struct {
	Version             string                 `json:"version"`
	ID                  string                 `json:"id"`
	CaptureStateID      string                 `json:"capture_state_id"`
	Title               string                 `json:"title"`
	DurationMS          int                    `json:"duration_ms"`
	Narration           string                 `json:"narration,omitempty"`
	OnScreenText        string                 `json:"on_screen_text,omitempty"`
	CreativeDirection   string                 `json:"creative_direction"`
	FilmingRequirements []string               `json:"filming_requirements"`
	ProductionState     string                 `json:"production_state"`
	Background          string                 `json:"background,omitempty"`
	Headline            string                 `json:"headline,omitempty"`
	Body                string                 `json:"body,omitempty"`
	Composition         *videocomposition.Link `json:"composition,omitempty"`
}

type StoryboardCatalog struct {
	Version string                    `json:"version"`
	Catalog *videocomposition.Catalog `json:"catalog,omitempty"`
}

type StoryboardHead struct {
	Reference ReadyReference
	Sections  []StoryboardHeadSection
	Catalog   *videocomposition.Catalog
}

type StoryboardHeadSection struct {
	Part     pebblestore.ArtifactV2Part
	Revision pebblestore.ArtifactV2PartRevision
	Section  StoryboardSection
	Still    *pebblestore.ArtifactV2Derivative
}

type storyboardTemplateSection struct {
	StoryboardSection
	PartID         string
	PartRevisionID string
}

type storyboardTemplateData struct {
	CaptureManifest template.JS
	SectionsJSON    template.JS
	Sections        []storyboardTemplateSection
}

// CreativeCompiler dispatches only on V2-owned typed media. It never parses or
// invokes the retired HTML storyboard manifest or managed-artifact writer.
type CreativeCompiler struct{ Motion MotionCompiler }

func (c CreativeCompiler) Compile(ctx context.Context, input CompileInput) (CompileProduct, error) {
	if containsStoryboardPart(input.Parts) {
		return (StoryboardCompiler{}).Compile(ctx, input)
	}
	return c.Motion.Compile(ctx, input)
}

type StoryboardCompiler struct{}

func (StoryboardCompiler) Compile(_ context.Context, input CompileInput) (CompileProduct, error) {
	parts := append([]CompilePart(nil), input.Parts...)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Definition.Order < parts[j].Definition.Order })
	sections := make([]storyboardTemplateSection, 0, len(parts))
	var catalog *videocomposition.Catalog
	stateIDs := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Revision.Blob.MediaType {
		case StoryboardSectionMediaType:
			var section StoryboardSection
			if err := decodeStrictJSON(part.Body, &section); err != nil {
				return storyboardCompileFailure("storyboard_section_syntax", part.Definition.ID, "The storyboard section JSON is invalid or contains unknown fields.")
			}
			if diagnostic := validateStoryboardSection(section, part.Definition); diagnostic != nil {
				return CompileProduct{CompilerVersion: StoryboardCompilerVersion, TemplateVersion: StoryboardTemplateVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{*diagnostic}}, errors.New(diagnostic.Code)
			}
			sections = append(sections, storyboardTemplateSection{StoryboardSection: section, PartID: part.Definition.ID, PartRevisionID: part.Revision.ID})
			stateIDs = append(stateIDs, section.CaptureStateID)
		case StoryboardCatalogMediaType:
			if catalog != nil {
				return storyboardCompileFailure("storyboard_catalog_duplicate", part.Definition.ID, "The storyboard contains more than one spatial composition catalog.")
			}
			var source StoryboardCatalog
			if err := decodeStrictJSON(part.Body, &source); err != nil || source.Version != "artifact.storyboard.catalog/v1" || videocomposition.ValidateCatalog(source.Catalog) != nil {
				return storyboardCompileFailure("storyboard_catalog_invalid", part.Definition.ID, "The spatial composition catalog is invalid.")
			}
			catalog = source.Catalog
		default:
			return storyboardCompileFailure("storyboard_part_type_unsupported", part.Definition.ID, "This part type is not accepted by the storyboard compiler.")
		}
	}
	if len(sections) == 0 || len(sections) > htmlcapture.MaxStates {
		return storyboardCompileFailure("storyboard_sections_invalid", "", "A storyboard requires 1 to 16 ordered section parts.")
	}
	seen := map[string]bool{}
	for _, section := range sections {
		if seen[section.CaptureStateID] {
			return storyboardCompileFailure("storyboard_capture_state_duplicate", section.PartID, "Storyboard capture state IDs must be unique.")
		}
		seen[section.CaptureStateID] = true
		if err := videocomposition.ValidateLink(catalog, section.Composition, int64(section.DurationMS)); err != nil {
			return storyboardCompileFailure("storyboard_composition_invalid", section.PartID, "A storyboard section spatial composition is invalid.")
		}
	}
	captureManifest, _ := json.Marshal(map[string]any{"version": "swarm.capture/v1", "states": func() []map[string]string {
		out := make([]map[string]string, 0, len(stateIDs))
		for _, id := range stateIDs {
			out = append(out, map[string]string{"id": id})
		}
		return out
	}()})
	sectionsJSON, _ := json.Marshal(sections)
	var output bytes.Buffer
	if err := storyboardHTMLTemplate.Execute(&output, storyboardTemplateData{CaptureManifest: template.JS(captureManifest), SectionsJSON: template.JS(sectionsJSON), Sections: sections}); err != nil {
		return CompileProduct{}, err
	}
	duration := 0
	for _, section := range sections {
		duration += section.DurationMS
	}
	return CompileProduct{Bytes: output.Bytes(), MediaType: "text/html", CompilerVersion: StoryboardCompilerVersion, TemplateVersion: StoryboardTemplateVersion, DurationMS: duration, RepresentativeTimestampsMS: []int{0}}, nil
}

func containsStoryboardPart(parts []CompilePart) bool {
	for _, part := range parts {
		if part.Revision.Blob.MediaType == StoryboardSectionMediaType || part.Revision.Blob.MediaType == StoryboardCatalogMediaType {
			return true
		}
	}
	return false
}

func validateStoryboardSection(section StoryboardSection, part pebblestore.ArtifactV2Part) *pebblestore.ArtifactV2Diagnostic {
	fail := func(code, locator, message string) *pebblestore.ArtifactV2Diagnostic {
		d := safeDiagnostic(code, "manifest", "error", "repairable", message)
		d.PartID, d.AuthoredLocator = part.ID, locator
		return &d
	}
	if section.Version != "artifact.storyboard.section/v1" || !safeIdentifier(section.ID) || section.ID != part.Key || !safeIdentifier(section.CaptureStateID) {
		return fail("storyboard_section_identity_invalid", "identity", "The storyboard section version, ID, part key, or capture state is invalid.")
	}
	if strings.TrimSpace(section.Title) == "" || len(section.Title) > 200 || section.DurationMS < 1 || section.DurationMS > 600000 {
		return fail("storyboard_section_invalid", "title", "The storyboard title or duration is outside fixed bounds.")
	}
	if strings.TrimSpace(section.CreativeDirection) == "" || len(section.CreativeDirection) > 2000 || len(section.FilmingRequirements) < 1 || len(section.FilmingRequirements) > 16 {
		return fail("storyboard_direction_invalid", "creative_direction", "Creative direction and 1 to 16 filming requirements are required.")
	}
	for _, requirement := range section.FilmingRequirements {
		if strings.TrimSpace(requirement) == "" || len(requirement) > 1000 {
			return fail("storyboard_filming_requirement_invalid", "filming_requirements", "A filming requirement is empty or exceeds fixed bounds.")
		}
	}
	if section.ProductionState != pebblestore.VideoProductionStatePending && section.ProductionState != pebblestore.VideoProductionStateReady {
		return fail("storyboard_production_state_invalid", "production_state", "Production state must be pending or ready.")
	}
	for _, value := range []string{section.Narration, section.OnScreenText, section.Headline, section.Body} {
		if len(value) > 4000 {
			return fail("storyboard_text_invalid", "text", "Storyboard text exceeds fixed bounds.")
		}
	}
	return nil
}

func storyboardCompileFailure(code, partID, message string) (CompileProduct, error) {
	d := safeDiagnostic(code, "manifest", "error", "repairable", message)
	d.PartID = partID
	return CompileProduct{CompilerVersion: StoryboardCompilerVersion, TemplateVersion: StoryboardTemplateVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{d}}, errors.New(code)
}

// StoryboardValidator proves every declared state can be selected and rendered
// from the exact compiler output. Durable still creation remains a separate V2
// service operation so validation failure cannot publish partial derivatives.
type StoryboardValidator struct {
	Renderer htmlcapture.Renderer
	Motion   MotionValidator
}

func (v StoryboardValidator) Validate(ctx context.Context, input ValidationInput) (ValidationProduct, error) {
	if input.Product.CompilerVersion != StoryboardCompilerVersion {
		return v.Motion.Validate(ctx, input)
	}
	if v.Renderer == nil {
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationFailedToRun, ValidatorVersion: StoryboardValidatorVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("storyboard_renderer_unavailable", "infrastructure", "error", "infrastructure", "Trusted storyboard rendering is unavailable.")}}, errors.New("storyboard renderer unavailable")
	}
	sections, _, err := storyboardSectionsFromCompileInput(input.Parts)
	if err != nil {
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationInvalid, ValidatorVersion: StoryboardValidatorVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("storyboard_exact_parts_invalid", "manifest", "error", "repairable", "The exact composition no longer contains valid storyboard parts.")}}, nil
	}
	states := make([]string, 0, len(sections))
	for _, section := range sections {
		states = append(states, section.Section.CaptureStateID)
	}
	results, renderErr := v.Renderer.Capture(ctx, htmlcapture.Request{Entry: "index.html", Files: map[string][]byte{"index.html": input.Product.Bytes}, StateIDs: states})
	if renderErr != nil || len(results) != len(states) {
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationInvalid, ValidatorVersion: StoryboardValidatorVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("storyboard_render_invalid", "frame", "error", "repairable", "Trusted rendering could not produce every exact storyboard state.")}}, nil
	}
	digests := make([]string, 0, len(results))
	for i, result := range results {
		if result.StateID != states[i] || len(result.PNG) == 0 {
			return ValidationProduct{Status: pebblestore.ArtifactV2ValidationInvalid, ValidatorVersion: StoryboardValidatorVersion, Diagnostics: []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("storyboard_render_mismatch", "frame", "error", "repairable", "Trusted rendering returned a mismatched storyboard state.")}}, nil
		}
		digests = append(digests, digest(result.PNG))
	}
	return ValidationProduct{Status: pebblestore.ArtifactV2ValidationValid, ValidatorVersion: StoryboardValidatorVersion, RendererSnapshot: "trusted-chrome-capture-1", EvidenceDigests: digests}, nil
}

func storyboardSectionsFromCompileInput(parts []CompilePart) ([]StoryboardHeadSection, *videocomposition.Catalog, error) {
	ordered := append([]CompilePart(nil), parts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Definition.Order < ordered[j].Definition.Order })
	var sections []StoryboardHeadSection
	var catalog *videocomposition.Catalog
	for _, part := range ordered {
		switch part.Revision.Blob.MediaType {
		case StoryboardSectionMediaType:
			var section StoryboardSection
			if decodeStrictJSON(part.Body, &section) != nil || validateStoryboardSection(section, part.Definition) != nil {
				return nil, nil, errors.New("invalid storyboard section")
			}
			sections = append(sections, StoryboardHeadSection{Part: part.Definition, Revision: part.Revision, Section: section})
		case StoryboardCatalogMediaType:
			var source StoryboardCatalog
			if decodeStrictJSON(part.Body, &source) != nil || source.Version != "artifact.storyboard.catalog/v1" || videocomposition.ValidateCatalog(source.Catalog) != nil {
				return nil, nil, errors.New("invalid storyboard catalog")
			}
			catalog = source.Catalog
		default:
			return nil, nil, fmt.Errorf("unsupported storyboard part media type %q", part.Revision.Blob.MediaType)
		}
	}
	if len(sections) == 0 {
		return nil, nil, errors.New("storyboard has no sections")
	}
	return sections, catalog, nil
}

var storyboardHTMLTemplate = template.Must(template.New("storyboard-v2").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><script id="swarm-capture-manifest" type="application/json">{{.CaptureManifest}}</script><script>(function(){"use strict";const sections={{.SectionsJSON}};function select(id){const section=sections.find(value=>value.CaptureStateID===id);if(!section)throw new Error('unknown capture state');document.documentElement.dataset.swarmCaptureState=id;for(const node of document.querySelectorAll('[data-state]'))node.hidden=node.dataset.state!==id;return Promise.resolve()}const runtime={version:'swarm.capture/v1',select,ready:async id=>{await document.fonts.ready;return{state_id:id}}};globalThis.__SWARM_CAPTURE_V1__=runtime;addEventListener('DOMContentLoaded',()=>select(sections[0].CaptureStateID))})();</script><style>html,body{width:1920px;height:1080px;margin:0;overflow:hidden;background:#090b10;color:#fff;font-family:Inter,ui-sans-serif,system-ui}*{box-sizing:border-box}.state{width:1920px;height:1080px;padding:120px;display:flex;flex-direction:column;justify-content:flex-end}.eyebrow{font-size:30px;letter-spacing:.14em;text-transform:uppercase;opacity:.75}.headline{font-size:92px;line-height:1;max-width:1500px;margin:28px 0}.body{font-size:38px;line-height:1.35;max-width:1300px;opacity:.86}</style></head><body>{{range .Sections}}<main class="state" data-state="{{.CaptureStateID}}" data-artifact-v2-part-id="{{.PartID}}" data-artifact-v2-part-revision-id="{{.PartRevisionID}}" style="background:{{if .Background}}{{.Background}}{{else}}#090b10{{end}}"><div class="eyebrow">{{.Title}}</div><h1 class="headline">{{if .Headline}}{{.Headline}}{{else}}{{.OnScreenText}}{{end}}</h1><div class="body">{{.Body}}</div></main>{{end}}</body></html>`))
