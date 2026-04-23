package updatehandoff

import "errors"

const ExitCodeDevUpdateRequested = 42

var ErrDevUpdateRequested = errors.New("swarm dev update requested")
