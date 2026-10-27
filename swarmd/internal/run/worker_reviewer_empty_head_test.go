package run

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
	"testing"
)

// Requirement: ResolveTaskReviewTarget must refuse an absent delivery head
// before resolving or admitting review work. Threat: empty-head reviews fail
// misleadingly as stale or touch a checkout. This boundary test uses no service
// dependencies, so reaching target resolution would fail rather than pass.
func TestResolveTaskReviewTargetNoHead(t *testing.T) {
	for _, head := range []string{"", "  "} {
		var service *WorkerExecutionService
		target, err := service.ResolveTaskReviewTarget(context.Background(), identity.Principal{}, nil, "", "", head)
		if err == nil || err.Error() != "no head to review" || target != nil {
			t.Fatalf("target=%v error=%v", target, err)
		}
	}
}
