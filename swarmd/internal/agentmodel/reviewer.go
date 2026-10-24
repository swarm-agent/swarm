package agentmodel

import (
	"errors"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
)

// EffectiveReviewerAssignment shares the native and worker reviewer policy.
// Explicit settings win; otherwise use the coder provider's review recommendation,
// retaining the coder assignment only when that provider has no review pick.
func EffectiveReviewerAssignment(models *model.Service, settings agentmodelsettings.Settings) (agentmodelsettings.Assignment, string, error) {
	if settings.SystemAgents.Reviewer.Model != "" {
		return settings.SystemAgents.Reviewer, "setting", nil
	}
	assignment := settings.SystemAgents.Coder
	if assignment.Provider == "" {
		return assignment, "setting", nil
	}
	if models == nil {
		return agentmodelsettings.Assignment{}, "", errors.New("reviewer model catalog unavailable")
	}
	records, ok, err := models.RecommendedCatalogRoleDefaults(assignment.Provider, "review")
	if err != nil {
		return agentmodelsettings.Assignment{}, "", err
	}
	if ok {
		record := records["review"]
		for _, rec := range record.Recommendations {
			if rec.Role == "review" {
				assignment.Model = record.Model
				assignment.Thinking = rec.Thinking
				if assignment.Thinking == "" {
					assignment.Thinking = record.DefaultThinking
				}
				assignment.ServiceTier = ""
				if rec.Serving == "fast" || rec.Serving == "priority" {
					assignment.ServiceTier = "priority"
				}
				assignment.ContextMode = ""
				return assignment, "catalog", nil
			}
		}
	}
	return assignment, "setting", nil
}
