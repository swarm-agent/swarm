package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockingMintReporter struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingMintReporter) ReportPending(context.Context) error {
	close(r.started)
	<-r.release
	return errors.New("remote unavailable")
}

func TestStartPendingMintReportDoesNotBlockStartup(t *testing.T) {
	reporter := &blockingMintReporter{started: make(chan struct{}), release: make(chan struct{})}
	returned := make(chan struct{})
	go func() {
		startPendingMintReport(context.Background(), reporter)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("startPendingMintReport blocked on network work")
	}
	select {
	case <-reporter.started:
	case <-time.After(time.Second):
		t.Fatal("background mint report did not start")
	}
	close(reporter.release)
}

func TestStartPendingMintReportSuppressedWhenDisabled(t *testing.T) {
	for _, val := range []string{"1", "true", "yes", "TRUE", " 1 "} {
		t.Run("env="+val, func(t *testing.T) {
			t.Setenv("SWARM_DISABLE_MINT_REPORT", val)
			reporter := &blockingMintReporter{started: make(chan struct{}), release: make(chan struct{})}
			startPendingMintReport(context.Background(), reporter)

			select {
			case <-reporter.started:
				t.Fatalf("expected mint report not to start when SWARM_DISABLE_MINT_REPORT=%q", val)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}
