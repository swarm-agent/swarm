package tool

import (
	"context"
	"strings"
)

type VideoRunContext struct {
	SessionID string
	RunID     string
	MessageID string
}

type videoRunContextKey struct{}

func WithVideoRunContext(parent context.Context, run VideoRunContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	run.SessionID = strings.TrimSpace(run.SessionID)
	run.RunID = strings.TrimSpace(run.RunID)
	run.MessageID = strings.TrimSpace(run.MessageID)
	return context.WithValue(parent, videoRunContextKey{}, run)
}

func VideoRunContextFromContext(ctx context.Context) (VideoRunContext, bool) {
	if ctx == nil {
		return VideoRunContext{}, false
	}
	run, ok := ctx.Value(videoRunContextKey{}).(VideoRunContext)
	return run, ok && run.SessionID != "" && run.RunID != ""
}
