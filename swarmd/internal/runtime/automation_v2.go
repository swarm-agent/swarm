package runtime

import (
	"context"
	"errors"
	"log"
)

// The V2 scheduler is disabled for launch. The background scheduler loop is no-oped
// so the 1-second sweep never runs.
func (d *Daemon) StartAutomationV2Scheduling(ctx context.Context) error {
	d.automationMu.Lock()
	defer d.automationMu.Unlock()
	if d.automationClosed {
		return errors.New("automation v2 scheduler lifecycle conflict")
	}
	log.Printf("automation v2 scheduler disabled; background sweep skipped")
	return nil
}
