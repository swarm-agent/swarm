package runtime

import (
	"context"
	"log"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	v3SessionRetentionInitialDelay = time.Minute
	v3SessionRetentionInterval     = 15 * time.Minute
)

// startV3SessionRetention schedules bounded store maintenance outside request
// handling. Every pass has a hard row budget; failures are logged as failures
// and the durable resume state remains at the last successfully committed pass.
func startV3SessionRetention(ctx context.Context, sessions *sessionruntime.Service) {
	if sessions == nil {
		return
	}
	go func() {
		run := func() {
			result, err := sessions.RunSessionRetentionPass(ctx, time.Now(), pebblestore.DefaultV3SessionRetentionPolicy())
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("warning: v3 session retention pass failed: %v", err)
				}
				return
			}
			if result.RealtimeRecordsDeleted > 0 || result.IdempotencyRecordsDeleted > 0 {
				log.Printf("v3 session retention pass: realtime_deleted=%d idempotency_deleted=%d oldest_retained_endpoint=%d more_realtime=%t more_idempotency=%t", result.RealtimeRecordsDeleted, result.IdempotencyRecordsDeleted, result.OldestRetainedEndpointSeq, result.MoreRealtimeWork, result.MoreIdempotencyWork)
			}
			migration, migrationErr := sessions.RunSessionSearchMigrationPass(ctx, time.Now(), 10)
			if migrationErr != nil {
				if ctx.Err() == nil {
					log.Printf("warning: v3 session search migration pass failed: %v", migrationErr)
				}
				return
			}
			if migration.SessionsMigrated > 0 || migration.SessionsDeferred > 0 {
				log.Printf("v3 session search migration pass: migrated=%d deferred=%d more=%t", migration.SessionsMigrated, migration.SessionsDeferred, migration.MoreWork)
			}
		}

		initial := time.NewTimer(v3SessionRetentionInitialDelay)
		select {
		case <-ctx.Done():
			if !initial.Stop() {
				<-initial.C
			}
			return
		case <-initial.C:
			run()
		}
		ticker := time.NewTicker(v3SessionRetentionInterval)
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
