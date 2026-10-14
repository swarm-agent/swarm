package run

import (
	"errors"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func validateReviewRequest(role string, input map[string]any) error {
	job, _ := input["review_job"].(string)
	ref, _ := input["review_ref"].(string)
	if role != store.WorkerRoleReviewer {
		if job != "" || ref != "" {
			return errors.New("review_job and review_ref are only accepted for reviewer workers")
		}
		return nil
	}
	if strings.TrimSpace(job) == "" && strings.TrimSpace(ref) == "" {
		return errors.New("reviewer requires review_job or review_ref naming a branch/commit")
	}
	if job != "" && ref != "" {
		return errors.New("use review_job or review_ref, not both")
	}
	if job != "" {
		parts := strings.Split(job, "/")
		if len(parts) != 3 || parts[0] != "worker" || parts[1] == "" || parts[2] == "" {
			return errors.New("review_job must be worker/<worker id>/<run id>")
		}
	}
	return nil
}
