package sandbox

import (
	"context"

	"io"
	"strings"
	"sync"
)

type fakeResolver struct {
	root string
	img  EnvironmentImage
	err  error
}

func (f fakeResolver) SandboxEnvironment(root string) (EnvironmentImage, bool, error) {
	if f.err != nil {
		return EnvironmentImage{}, false, f.err
	}
	if root == f.root {
		return f.img, true, nil
	}
	return EnvironmentImage{}, false, nil
}

// scriptedRunner answers by the full joined command line.
type scriptedRunner struct {
	mu     sync.Mutex
	calls  []string
	answer func(cmd string) (string, error)
}

func (s *scriptedRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	s.mu.Lock()
	s.calls = append(s.calls, cmd)
	s.mu.Unlock()
	out, err := s.answer(cmd)
	return []byte(out), err
}

func (s *scriptedRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return s.Run(ctx, name, args...)
}

func (s *scriptedRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
	_, err := s.Run(ctx, name, args...)
	return err
}
