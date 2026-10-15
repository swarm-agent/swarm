package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

type deathRunner struct {
	*fakeRunner
	after  containerState
	states int
}

func (r *deathRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 3 && args[0] == "container" && args[1] == "inspect" && args[3] == "{{json .State}}" {
		r.states++
		return json.Marshal(r.after)
	}
	return r.fakeRunner.Run(ctx, name, args...)
}
func (r *deathRunner) RunWithIO(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
	return errors.New("exec transport lost")
}
