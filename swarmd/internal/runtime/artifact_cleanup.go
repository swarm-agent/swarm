package runtime

import (
	"context"
	"log"
	"time"

	"swarm/packages/swarmd/internal/artifact"
)

const (
	artifactMaintenanceInitialDelay = time.Minute
	artifactMaintenanceInterval     = 15 * time.Minute
)

// startArtifactMaintenance repairs bounded Pebble projections and acknowledges
// obsolete workspace-byte cleanup tombstones. Git remains the byte authority.
func startArtifactMaintenance(ctx context.Context, artifacts *artifact.Registry) {
	if artifacts == nil {
		return
	}
	run := func() {
		report, err := artifacts.RunMaintenance(artifact.DefaultMaintenanceLimit)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("warning: artifact maintenance pass failed: %v", err)
			}
			return
		}
		if report.DeletedSessions > 0 || report.CollectionsRepaired > 0 {
			log.Printf("artifact maintenance pass: visited=%d acknowledged_deleted_sessions=%d repaired_collections=%d", report.SessionsVisited, report.DeletedSessions, report.CollectionsRepaired)
		}
	}

	go func() {
		// The first goroutine pass closes the crash window after durable session
		// deletion without delaying daemon startup on bounded filesystem I/O.
		run()
		initial := time.NewTimer(artifactMaintenanceInitialDelay)
		select {
		case <-ctx.Done():
			if !initial.Stop() {
				<-initial.C
			}
			return
		case <-initial.C:
			run()
		}
		ticker := time.NewTicker(artifactMaintenanceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
