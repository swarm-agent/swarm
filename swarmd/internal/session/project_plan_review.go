package session

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ValidateProjectPlanReview requires the exact structured content a task card
// needs before publication or acceptance. Markdown and pipeline summaries are
// not substitutes for an executable document and bound authored outcomes.
func ValidateProjectPlanReview(doc *pebblestore.SessionPlanDocument) error {
	if err := ValidateExecutablePlanDocument(doc); err != nil {
		return err
	}
	if len(doc.Requirements) == 0 {
		return fmt.Errorf("plan review requires authored requirements bound to acceptance criteria")
	}
	if strings.TrimSpace(doc.Title) == "" || strings.TrimSpace(doc.Info.Goal) == "" {
		return fmt.Errorf("plan review requires a title and goal")
	}
	for _, cp := range doc.Checkpoints {
		if strings.TrimSpace(cp.Title) == "" || len(cp.Tasks) == 0 {
			return fmt.Errorf("plan review requires readable checkpoint titles and tasks")
		}
		for _, task := range cp.Tasks {
			if strings.TrimSpace(task) == "" {
				return fmt.Errorf("plan review cannot contain blank tasks")
			}
		}
	}
	return validatePlanRequirements(doc)
}
