package runtime

import (
	"context"
	"errors"
	"log"

	"swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The V2 scheduler is the only installed automation executor. Startup is owned
// by Run and shutdown joins the loop before stores close. The loop sleeps
// until the next due slot and is woken by committed store changes; it never
// polls and never touches legacy automation records.
func (d *Daemon) StartAutomationV2Scheduling(ctx context.Context) error {
	d.automationMu.Lock()
	defer d.automationMu.Unlock()
	if d.automationClosed || d.automationLoop != nil || d.automationExecution != nil || d.automationV2Scheduler == nil || d.store == nil {
		return errors.New("automation v2 scheduler lifecycle conflict")
	}
	scheduler := run.NewWorkerScheduler(d.workerExecution, run.WorkerSchedulerOptions{
		V2:      d.automationV2Scheduler,
		V2Hints: pebblestore.NewSessionStore(d.store),
		Report:  func(err error) { log.Printf("automation v2 sweep failed: %v; durable pending work retained", err) },
	})
	d.store.SetSchedulingObserver(scheduler.Notify)
	loopCtx, cancel := context.WithCancel(ctx)
	loop := &automationLoop{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(loop.done)
		defer d.store.SetSchedulingObserver(nil)
		scheduler.Run(loopCtx)
	}()
	d.automationLoop = loop
	return nil
}
