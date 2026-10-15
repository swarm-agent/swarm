package sandbox

import (
	"context"

	"io"

	"sync"
)

type lifecycleRunner struct {
	*fakeRunner
	started     chan struct{}
	unblock     chan struct{}
	stopStarted chan struct{}
	stopUnblock chan struct{}
	once        sync.Once
	stateMu     sync.Mutex
	stopped     bool
	unsafeExec  bool
}

func (r *lifecycleRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "stop" {
		if r.stopStarted != nil {
			close(r.stopStarted)
			select {
			case <-r.stopUnblock:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		r.stateMu.Lock()
		r.stopped = true
		r.stateMu.Unlock()
	}
	if len(args) > 0 && (args[0] == "run" || args[0] == "start") {
		r.stateMu.Lock()
		r.stopped = false
		r.stateMu.Unlock()
	}
	return r.fakeRunner.Run(ctx, name, args...)
}
func (r *lifecycleRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, _ string, args ...string) error {
	r.stateMu.Lock()
	r.unsafeExec = r.unsafeExec || r.stopped
	r.stateMu.Unlock()
	block := false
	r.once.Do(func() {
		block = true
		if r.started != nil {
			close(r.started)
		}
	})
	if block && r.unblock != nil {
		select {
		case <-r.unblock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
