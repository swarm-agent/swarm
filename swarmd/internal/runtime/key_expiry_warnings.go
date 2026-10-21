package runtime

import (
	"context"
	"log"
	"time"

	"swarm/packages/swarmd/internal/security"
)

// Keys are checked a minute after start and then every few hours, so a key
// about to stop working (unused for almost 90 days, or near its fixed end
// date) raises key.expiring well inside the week's notice.
const (
	keyExpiryCheckInitialDelay = time.Minute
	keyExpiryCheckInterval     = 6 * time.Hour
)

func startKeyExpiryWarnings(ctx context.Context, svc *security.Service) {
	if ctx == nil || svc == nil {
		return
	}
	go func() {
		timer := time.NewTimer(keyExpiryCheckInitialDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if ids, err := svc.WarnExpiringKeys(time.Now()); err != nil {
				log.Printf("swarmd could not check keys for expiry: %v", err)
			} else if len(ids) > 0 {
				log.Printf("swarmd: %d key(s) stop working within 7 days; key.expiring raised", len(ids))
			}
			timer.Reset(keyExpiryCheckInterval)
		}
	}()
}
