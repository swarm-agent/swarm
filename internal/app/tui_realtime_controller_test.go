package app

import (
	"context"

	"sync"

	"swarm-refactor/swarmtui/internal/client"
)

type tuiRealtimeFakeStreamCall struct {
	Index          int
	Subscriptions  []client.V3RealtimeSubscription
	Worksets       []client.V3RealtimeWorksetSubscription
	Capabilities   []string
	EndpointCursor string
}

type tuiRealtimeFakeStreamer struct {
	mu      sync.Mutex
	calls   []tuiRealtimeFakeStreamCall
	started chan tuiRealtimeFakeStreamCall
	ctxs    chan context.Context
	handler func(index int, ctx context.Context, options client.V3RealtimeResumeOptions, onFrame func(client.V3RealtimeFrame)) error
}

func (f *tuiRealtimeFakeStreamer) StreamV3Realtime(ctx context.Context, options client.V3RealtimeResumeOptions, onFrame func(client.V3RealtimeFrame)) error {
	f.mu.Lock()
	index := len(f.calls) + 1
	call := tuiRealtimeFakeStreamCall{Index: index, Subscriptions: cloneTUIRealtimeSubscriptions(options.Subscriptions), Worksets: cloneTUIRealtimeWorksets(options.Worksets), Capabilities: append([]string(nil), options.Capabilities...), EndpointCursor: options.EndpointCursor}
	f.calls = append(f.calls, call)
	handler := f.handler
	f.mu.Unlock()
	select {
	case f.started <- call:
	default:
	}
	select {
	case f.ctxs <- ctx:
	default:
	}
	if handler != nil {
		return handler(index, ctx, options, onFrame)
	}
	if options.OnResumeSent != nil {
		options.OnResumeSent()
	}
	<-ctx.Done()
	return nil
}
