package updatehandoff

import "errors"

const (
	ExitCodeDevUpdateRequested     = 42
	ExitCodeReleaseUpdateRequested = 43
)

var (
	ErrDevUpdateRequested     = errors.New("swarm dev update requested")
	ErrReleaseUpdateRequested = errors.New("swarm release update requested")
)
