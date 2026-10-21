package sandbox

import (
	"context"
	"io"
)

type outputTransport struct{ output string }

func (f *outputTransport) RunExec(_ context.Context, _ io.Reader, stdout, _ io.Writer, _ ...string) error {
	_, err := io.WriteString(stdout, f.output)
	return err
}
func (f *outputTransport) RunExecCombined(context.Context, ...string) ([]byte, error) {
	return nil, nil
}
