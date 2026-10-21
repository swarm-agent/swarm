package sandbox

import (
	"context"
	"errors"
	"strings"
)

// Purpose: Docker-like missing-container errors must be retryable only after
// absence verification; fresh managers at every durable phase must retain deny
// identity and finish without resources. This engine-state test proves restart
// ordering, not kernel mount enforcement.
type cleanupEngine struct {
	*fakeRunner
	container, volume bool
	failVolume        bool
	inspectFailure    bool
}

func (r *cleanupEngine) Run(ctx context.Context, engine string, args ...string) ([]byte, error) {
	switch strings.Join(args[:min(2, len(args))], " ") {
	case "rm -f":
		if !r.container {
			return []byte("No such container"), errors.New("missing")
		}
		r.container = false
		return nil, nil
	case "container inspect":
		if r.inspectFailure {
			return []byte("daemon unavailable"), errors.New("engine unavailable")
		}
		if !r.container {
			return []byte("Error: No such container"), errors.New("missing")
		}
		return []byte("{}"), nil
	case "volume ls":
		if r.volume {
			return []byte("home\n"), nil
		}
		return nil, nil
	case "volume rm":
		if r.failVolume {
			return nil, errors.New("volume busy")
		}
		r.volume = false
		return nil, nil
	}
	return r.fakeRunner.Run(ctx, engine, args...)
}
