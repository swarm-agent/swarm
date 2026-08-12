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

// startArtifactMaintenance retries byte deletion from durable deleted-session
// tombstones and removes incomplete staging for active sessions. Each pass is
// bounded and archived tombstones are never cleaned.
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
		if report.DeletedSessions > 0 || report.RemovedStaging > 0 {
			log.Printf("artifact maintenance pass: visited=%d deleted_sessions=%d removed_staging=%d removed_bytes=%d", report.SessionsVisited, report.DeletedSessions, report.RemovedStaging, report.RemovedBytes)
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
