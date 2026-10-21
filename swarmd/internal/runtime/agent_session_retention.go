package runtime

import (
	"context"
	"log"
	"time"

	"swarm/packages/swarmd/internal/api"
)

const (
	agentRetentionInitialDelay = 2 * time.Minute
	agentRetentionInterval     = 10 * time.Minute
	// agentRetentionCatchUp is the pause before the next pass while a pass
	// left eligible conversations beyond its batch.
	agentRetentionCatchUp = time.Minute
)

// startAgentSessionRetention runs the sealed-agent conversation retention
// sweep (api.Server.SweepAgentSessionRetention) outside request handling.
// Each pass is bounded; a failed pass is logged (the sweep also raises a
// session.retention.failed signal) and the next pass retries.
func startAgentSessionRetention(ctx context.Context, server *api.Server) {
	if server == nil {
		return
	}
	go func() {
		next := agentRetentionInitialDelay
		for {
			timer := time.NewTimer(next)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			result, err := server.SweepAgentSessionRetention(ctx, time.Now())
			if err != nil && ctx.Err() == nil {
				log.Printf("warning: agent conversation retention pass failed: %v", err)
			}
			next = agentRetentionInterval
			if result.More {
				next = agentRetentionCatchUp
			}
		}
	}()
}
