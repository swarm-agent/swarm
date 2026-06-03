package session

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// NormalizePlanDocumentForSave returns the structured one-plan document that
// should be stored with the next plan revision. A nil incoming document means
// the caller did not edit the structured model, so the existing document is
// preserved. A non-nil incoming document is normalized from the enclosing plan
// identity/title where needed and validated before it is saved.
func NormalizePlanDocumentForSave(planID, title string, incoming, existing *pebblestore.SessionPlanDocument) (*pebblestore.SessionPlanDocument, error) {
	planID = strings.TrimSpace(planID)
	title = strings.TrimSpace(title)
	if incoming == nil {
		return clonePlanDocument(existing), nil
	}
	doc := clonePlanDocument(incoming)
	if doc == nil {
		return nil, nil
	}
	doc.ID = strings.TrimSpace(doc.ID)
	if doc.ID == "" {
		doc.ID = planID
	}
	doc.Title = strings.TrimSpace(doc.Title)
	if doc.Title == "" {
		doc.Title = title
	}
	doc.Status = strings.TrimSpace(doc.Status)
	doc.SchemaVersion = strings.TrimSpace(doc.SchemaVersion)
	doc.RevisionID = strings.TrimSpace(doc.RevisionID)
	doc.ActiveCheckpointID = strings.TrimSpace(doc.ActiveCheckpointID)
	doc.RenderedText = strings.TrimSpace(doc.RenderedText)
	doc.DisplayText = strings.TrimSpace(doc.DisplayText)
	trimPlanInfo(&doc.Info)
	for i := range doc.Checkpoints {
		trimPlanCheckpoint(&doc.Checkpoints[i])
		if doc.Checkpoints[i].Order == 0 {
			doc.Checkpoints[i].Order = i + 1
		}
	}
	if err := ValidatePlanDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func ValidatePlanDocument(doc *pebblestore.SessionPlanDocument) error {
	if doc == nil {
		return nil
	}
	if strings.TrimSpace(doc.ID) == "" {
		return errors.New("plan document id is required")
	}
	if strings.TrimSpace(doc.Title) == "" {
		return errors.New("plan document title is required")
	}
	seen := make(map[string]struct{}, len(doc.Checkpoints))
	for i, checkpoint := range doc.Checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" {
			return fmt.Errorf("plan document checkpoint at index %d requires id", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("plan document checkpoint id %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	activeID := strings.TrimSpace(doc.ActiveCheckpointID)
	if activeID != "" {
		if _, ok := seen[activeID]; !ok {
			return fmt.Errorf("plan document active_checkpoint_id %q does not match a checkpoint", activeID)
		}
	}
	return nil
}

func clonePlanDocument(doc *pebblestore.SessionPlanDocument) *pebblestore.SessionPlanDocument {
	if doc == nil {
		return nil
	}
	clone := *doc
	clone.Info.Decisions = cloneStringSlice(doc.Info.Decisions)
	clone.Info.Constraints = cloneStringSlice(doc.Info.Constraints)
	clone.Info.Assumptions = cloneStringSlice(doc.Info.Assumptions)
	clone.Info.OpenQuestions = cloneStringSlice(doc.Info.OpenQuestions)
	clone.Info.RelevantFiles = cloneStringSlice(doc.Info.RelevantFiles)
	clone.Checkpoints = make([]pebblestore.SessionPlanCheckpoint, len(doc.Checkpoints))
	for i := range doc.Checkpoints {
		clone.Checkpoints[i] = doc.Checkpoints[i]
		clone.Checkpoints[i].Tasks = cloneStringSlice(doc.Checkpoints[i].Tasks)
		clone.Checkpoints[i].AcceptanceCriteria = cloneStringSlice(doc.Checkpoints[i].AcceptanceCriteria)
		clone.Checkpoints[i].ChangedFiles = cloneStringSlice(doc.Checkpoints[i].ChangedFiles)
		clone.Checkpoints[i].Validation = cloneStringSlice(doc.Checkpoints[i].Validation)
	}
	return &clone
}

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func trimPlanInfo(info *pebblestore.SessionPlanInfo) {
	if info == nil {
		return
	}
	info.Goal = strings.TrimSpace(info.Goal)
	info.Context = strings.TrimSpace(info.Context)
	info.ValidationStrategy = strings.TrimSpace(info.ValidationStrategy)
	info.Decisions = trimStringSlice(info.Decisions)
	info.Constraints = trimStringSlice(info.Constraints)
	info.Assumptions = trimStringSlice(info.Assumptions)
	info.OpenQuestions = trimStringSlice(info.OpenQuestions)
	info.RelevantFiles = trimStringSlice(info.RelevantFiles)
}

func trimPlanCheckpoint(checkpoint *pebblestore.SessionPlanCheckpoint) {
	if checkpoint == nil {
		return
	}
	checkpoint.ID = strings.TrimSpace(checkpoint.ID)
	checkpoint.Title = strings.TrimSpace(checkpoint.Title)
	checkpoint.Status = strings.TrimSpace(checkpoint.Status)
	checkpoint.Objective = strings.TrimSpace(checkpoint.Objective)
	checkpoint.Tasks = trimStringSlice(checkpoint.Tasks)
	checkpoint.AcceptanceCriteria = trimStringSlice(checkpoint.AcceptanceCriteria)
	checkpoint.Notes = strings.TrimSpace(checkpoint.Notes)
	checkpoint.Report = strings.TrimSpace(checkpoint.Report)
	checkpoint.Result = strings.TrimSpace(checkpoint.Result)
	checkpoint.ChangedFiles = trimStringSlice(checkpoint.ChangedFiles)
	checkpoint.Validation = trimStringSlice(checkpoint.Validation)
}

func trimStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
