package artifactv3video

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const StoryboardFilename = "swarm-storyboard.json"

// TemporalSection is a shot, not a spatial HTML Part. Each capture state names
// one complete HTML entry in the same immutable Git project. Manifest order is
// timeline order; the stable ID is preserved when a later conversion replaces it.
type TemporalSection struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	CaptureStateID      string   `json:"capture_state_id"`
	Entrypoint          string   `json:"entrypoint"`
	DurationMs          int64    `json:"duration_ms"`
	ProductionState     string   `json:"production_state"`
	FilmingRequirements []string `json:"filming_requirements"`
}

type Storyboard struct {
	SchemaVersion string            `json:"schema_version"`
	Sections      []TemporalSection `json:"sections"`
}

func projectSections(project Project, selection Selection) ([]TemporalSection, error) {
	if selection.PartID != "" {
		return nil, errors.New("spatial Part targeting is not temporal video section selection")
	}
	body, present := project.Files[StoryboardFilename]
	if !present {
		if selection.CaptureStateID != "" {
			return nil, errors.New("capture-state target requires swarm-storyboard.json")
		}
		duration, _, err := normalizedTiming(selection.DurationMs, selection.FPS)
		return []TemporalSection{{ID: "artifact-v3", Title: "Artifact V3 animation", DurationMs: duration, ProductionState: "ready", FilmingRequirements: []string{"Preserve the authenticated Artifact V3 project and deterministic animation timing."}}}, err
	}
	if selection.DurationMs != 0 {
		return nil, errors.New("storyboard duration belongs to each temporal section")
	}
	if len(body) > 65536 {
		return nil, errors.New("storyboard manifest exceeds 64 KiB")
	}
	var board Storyboard
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&board) != nil || decoder.Decode(new(any)) != io.EOF || board.SchemaVersion != "swarm.artifact-storyboard/v3" || len(board.Sections) == 0 || len(board.Sections) > 16 {
		return nil, errors.New("invalid native V3 temporal storyboard manifest")
	}
	ids, states := map[string]bool{}, map[string]bool{}
	var selected []TemporalSection
	var total int64
	for _, section := range board.Sections {
		if strings.TrimSpace(section.ID) == "" || strings.TrimSpace(section.Title) == "" || strings.TrimSpace(section.CaptureStateID) == "" || ids[section.ID] || states[section.CaptureStateID] {
			return nil, errors.New("storyboard requires unique stable section and capture-state IDs")
		}
		ids[section.ID], states[section.CaptureStateID] = true, true
		if section.Entrypoint == "" || path.Clean(section.Entrypoint) != section.Entrypoint || strings.HasPrefix(section.Entrypoint, "/") || strings.HasPrefix(section.Entrypoint, "../") || strings.Contains(section.Entrypoint, "\\") || !strings.HasSuffix(section.Entrypoint, ".html") || len(project.Files[section.Entrypoint]) == 0 {
			return nil, fmt.Errorf("section %q requires an exact project HTML entrypoint", section.ID)
		}
		if section.DurationMs <= 0 || section.DurationMs > maxDurationMs || (section.ProductionState != "pending" && section.ProductionState != "ready") || len(section.FilmingRequirements) == 0 {
			return nil, fmt.Errorf("section %q requires bounded duration, production state and filming requirements", section.ID)
		}
		for _, requirement := range section.FilmingRequirements {
			if strings.TrimSpace(requirement) == "" {
				return nil, errors.New("empty filming requirement")
			}
		}
		total += section.DurationMs
		if total > maxDurationMs {
			return nil, errors.New("storyboard total duration exceeds 60 seconds")
		}
		if selection.CaptureStateID == "" || selection.CaptureStateID == section.CaptureStateID {
			selected = append(selected, section)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("requested capture state is not declared in storyboard")
	}
	return selected, nil
}

// Sections resolves declared state entries for the trusted renderer.
func Sections(project Project, stateID string) ([]TemporalSection, error) {
	return projectSections(project, Selection{CaptureStateID: stateID})
}
