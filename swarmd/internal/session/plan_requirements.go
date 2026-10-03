package session

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirements are not a second summary authority: each is an exact executable
// checkpoint acceptance criterion. Definition edits cannot silently detach them.
func validatePlanRequirements(doc *pebblestore.SessionPlanDocument) error {
	seen := map[string]bool{}
	criteria := map[string]bool{}
	for _, r := range doc.Requirements {
		key := r.CheckpointID + "\x00" + r.Text
		if criteria[key] {
			return fmt.Errorf("requirements cannot share an acceptance criterion")
		}
		criteria[key] = true
		if r.ID == "" || seen[r.ID] || strings.TrimSpace(r.Text) == "" || len(r.Text) > 500 || strings.ContainsAny(r.Text, "\r\n") {
			return fmt.Errorf("requirement needs a unique id and a single plain-English sentence (maximum 500 bytes)")
		}
		seen[r.ID] = true
		matches := 0
		for _, cp := range doc.Checkpoints {
			if cp.ID == r.CheckpointID {
				for _, criterion := range cp.AcceptanceCriteria {
					if criterion == r.Text {
						matches++
					}
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("requirement %q must match exactly one acceptance criterion in checkpoint %q", r.ID, r.CheckpointID)
		}
	}
	return nil
}

func applyRequirementEdit(doc *pebblestore.SessionPlanDocument, op PlanDocumentPatch) error {
	operation := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(op.Operation)), "-", "_")
	if err := validatePlanRequirements(doc); err != nil {
		return err
	}
	index := -1
	for i, r := range doc.Requirements {
		if r.ID == op.RequirementID {
			index = i
		}
	}
	if operation == "reorder_requirements" {
		if len(op.RequirementOrder) != len(doc.Requirements) {
			return fmt.Errorf("requirement_order must contain every requirement exactly once")
		}
		ordered := make([]pebblestore.SessionPlanRequirement, 0, len(doc.Requirements))
		seen := map[string]bool{}
		for _, id := range op.RequirementOrder {
			found := false
			for _, r := range doc.Requirements {
				if r.ID == id && !seen[id] {
					ordered = append(ordered, r)
					seen[id] = true
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("unknown or duplicate requirement %q", id)
			}
		}
		doc.Requirements = ordered
		doc.RequirementChanges = append(doc.RequirementChanges, "Reordered requirements")
		return nil
	}
	if operation != "add_requirement" && index < 0 {
		return fmt.Errorf("unknown requirement %q", op.RequirementID)
	}
	var next pebblestore.SessionPlanRequirement
	if operation != "remove_requirement" {
		if op.Requirement == nil {
			return fmt.Errorf("requirement is required")
		}
		next = *op.Requirement
		if operation == "edit_requirement" && next.ID != op.RequirementID {
			return fmt.Errorf("requirement identity cannot change")
		}
		if operation == "add_requirement" {
			for _, r := range doc.Requirements {
				if r.ID == next.ID {
					return fmt.Errorf("duplicate requirement %q", next.ID)
				}
			}
		}
	}
	// Only pending execution can be changed; never rewrite completed work.
	checkPending := func(id string) (int, error) {
		for i, cp := range doc.Checkpoints {
			if cp.ID == id {
				if cp.Status != "" && cp.Status != "pending" {
					return -1, fmt.Errorf("requirement checkpoint %q is not pending", id)
				}
				return i, nil
			}
		}
		return -1, fmt.Errorf("unknown checkpoint %q", id)
	}
	if index >= 0 {
		old := doc.Requirements[index]
		cp, err := checkPending(old.CheckpointID)
		if err != nil {
			return err
		}
		criteria := doc.Checkpoints[cp].AcceptanceCriteria
		for i, text := range criteria {
			if text == old.Text {
				doc.Checkpoints[cp].AcceptanceCriteria = append(criteria[:i], criteria[i+1:]...)
				break
			}
		}
	}
	if operation != "remove_requirement" {
		cp, err := checkPending(next.CheckpointID)
		if err != nil {
			return err
		}
		present := false
		for _, text := range doc.Checkpoints[cp].AcceptanceCriteria {
			if text == next.Text {
				present = true
			}
		}
		if !present {
			doc.Checkpoints[cp].AcceptanceCriteria = append(doc.Checkpoints[cp].AcceptanceCriteria, next.Text)
		}
	}
	switch operation {
	case "add_requirement":
		doc.Requirements = append(doc.Requirements, next)
		doc.RequirementChanges = append(doc.RequirementChanges, "Added: "+next.Text)
	case "edit_requirement":
		doc.Requirements[index] = next
		doc.RequirementChanges = append(doc.RequirementChanges, "Changed: "+next.Text)
	case "remove_requirement":
		doc.RequirementChanges = append(doc.RequirementChanges, "Removed: "+doc.Requirements[index].Text)
		doc.Requirements = append(doc.Requirements[:index], doc.Requirements[index+1:]...)
	}
	return validatePlanRequirements(doc)
}
