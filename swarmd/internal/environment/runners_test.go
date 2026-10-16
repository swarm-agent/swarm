package environment

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

type fallbackProvider func(context.Context, Spec) (Environment, error)

func (f fallbackProvider) Acquire(ctx context.Context, s Spec) (Environment, error) { return f(ctx, s) }

// Purpose: Auto.Acquire must expose infrastructure fallback without leaking
// credentials, but must not retry ordinary errors or cancellation. Provider
// fakes exercise the actual selection callback and logging boundary without Docker.
func TestAutoFallbackDiagnostic(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	for _, mode := range []string{"failure", "ordinary", "cancelled", "no_runner"} {
		output.Reset()
		ctx, cancel := context.WithCancel(context.Background())
		var err error = &Failure{Reason: "HTTP 400 invalid bucket\nAuthorization: Bearer private-test-token\n" + strings.Repeat("detail ", 150)}
		if mode == "ordinary" {
			err = errors.New("ordinary")
		}
		if mode == "cancelled" {
			cancel()
		}
		if mode == "no_runner" {
			err = errors.Join(&Failure{Reason: "no runner available"}, errNoRunnerAvailable)
		}
		localCalls := 0
		note, selected := "", ""
		p := Auto{RemoteName: "cloud build", Remote: fallbackProvider(func(context.Context, Spec) (Environment, error) { return nil, err }), Local: fallbackProvider(func(context.Context, Spec) (Environment, error) { localCalls++; return nil, nil })}
		spec := Spec{Fallback: func(s string) { note = s }, Selected: func(s string) { selected = s }}
		_, gotErr := p.Acquire(ctx, spec)
		cancel()
		if mode == "ordinary" || mode == "cancelled" {
			if gotErr == nil || localCalls != 0 || note != "" || output.Len() != 0 {
				t.Fatalf("unexpected fallback for %s", mode)
			}
			continue
		}
		if gotErr != nil || localCalls != 1 || selected != "local" || !strings.Contains(note, "cloud build unavailable") || len([]rune(note)) > 500 {
			t.Fatalf("fallback: %v %q %q", gotErr, selected, note)
		}
		if strings.Contains(note+output.String(), "private-test-token") || strings.Contains(note, "\n") {
			t.Fatal("unsafe fallback diagnostic")
		}
		if mode == "failure" && !strings.Contains(output.String(), "cloud build unavailable, using local:") {
			t.Fatal("missing fallback log")
		}
		if mode == "no_runner" {
			output.Reset()
			_, _ = p.Acquire(context.Background(), spec)
			if output.Len() != 0 {
				t.Fatal("repeated normal no-runner log")
			}
		}
	}
}
