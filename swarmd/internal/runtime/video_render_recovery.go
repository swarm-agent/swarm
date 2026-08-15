package runtime

import (
	"context"
	"log"

	"swarm/packages/swarmd/internal/videorender"
)

// startVideoRenderRecovery performs one bounded durable reconciliation pass.
// The render service owns job admission and recovery decisions; runtime only
// supplies daemon lifetime cancellation and startup wiring.
func startVideoRenderRecovery(ctx context.Context, renders *videorender.Service) {
	if renders == nil {
		return
	}
	go func() {
		count, err := renders.RecoverJobs(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("warning: recover durable video renders: %v", err)
			}
			return
		}
		if count > 0 {
			log.Printf("video render recovery admitted %d durable job(s)", count)
		}
	}()
}
