package runtime

import (
	"context"
	"errors"
	"log"
	"time"
)

// The V2 scheduler is the only installed automation executor. Startup is owned
// by Run and shutdown joins the loop before stores close. A bounded database
// sweep is not frontend polling and never touches legacy automation records.
func (d *Daemon) StartAutomationV2Scheduling(ctx context.Context) error {
	d.automationMu.Lock()
	defer d.automationMu.Unlock()
	if d.automationClosed || d.automationLoop != nil || d.automationExecution != nil || d.automationV2Scheduler == nil {
		return errors.New("automation v2 scheduler lifecycle conflict")
	}
	d.automationLoop = startAutomationLoop(ctx, time.Second, func(ctx context.Context) error { return d.automationV2Scheduler.Sweep(ctx, time.Now()) }, func(err error) { log.Printf("automation v2 sweep failed: %v; durable pending work retained", err) })
	return nil
}
