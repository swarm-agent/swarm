package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

const (
	tuiRealtimeStatusStarted  tuiRealtimeStatusKind = "started"
	tuiRealtimeStatusRetrying tuiRealtimeStatusKind = "retrying"
	tuiRealtimeStatusTerminal tuiRealtimeStatusKind = "terminal"
	tuiRealtimeStatusStopped  tuiRealtimeStatusKind = "stopped"
	tuiRealtimeStatusFailed   tuiRealtimeStatusKind = "failed"
)

const (
	defaultTUIRealtimeRetryBudget = 3
	defaultTUIRealtimeRetryBase   = 100 * time.Millisecond
	defaultTUIRealtimeRetryMax    = 2 * time.Second
)

type tuiRealtimeStreamClient interface {
	StreamSessionsV3Realtime(ctx context.Context, subscriptions []client.V3RealtimeSubscription, onFrame func(client.V3RealtimeFrame)) error
}

type tuiRealtimeStatusKind string

type tuiRealtimeStatus struct {
	Kind           tuiRealtimeStatusKind
	Generation     uint64
	Attempt        int
	Subscriptions  []client.V3RealtimeSubscription
	EndpointCursor string
	Frame          client.V3RealtimeFrame
	Err            error
	Reason         string
}

type tuiRealtimeController struct {
	mu sync.Mutex

	streamer tuiRealtimeStreamClient
	frames   chan<- client.V3RealtimeFrame
	statuses chan<- tuiRealtimeStatus

	generation  uint64
	active      *tuiRealtimeActiveGeneration
	retryBudget int
	retryBase   time.Duration
	retryMax    time.Duration
}

type tuiRealtimeActiveGeneration struct {
	generation     uint64
	cancel         context.CancelFunc
	subscriptions  []client.V3RealtimeSubscription
	endpointCursor string
}

func newTUIRealtimeController(streamer tuiRealtimeStreamClient, frames chan<- client.V3RealtimeFrame, statuses chan<- tuiRealtimeStatus) *tuiRealtimeController {
	return &tuiRealtimeController{
		streamer:    streamer,
		frames:      frames,
		statuses:    statuses,
		retryBudget: defaultTUIRealtimeRetryBudget,
		retryBase:   defaultTUIRealtimeRetryBase,
		retryMax:    defaultTUIRealtimeRetryMax,
	}
}

func (c *tuiRealtimeController) Start(subscriptions []client.V3RealtimeSubscription, endpointCursor string) error {
	return c.Reconcile(subscriptions, endpointCursor)
}

func (c *tuiRealtimeController) Reconcile(subscriptions []client.V3RealtimeSubscription, endpointCursor string) error {
	if c == nil {
		return nil
	}
	normalized, cursor, err := normalizeTUIRealtimeSubscriptions(subscriptions, endpointCursor)
	if err != nil {
		c.sendStatus(tuiRealtimeStatus{Kind: tuiRealtimeStatusFailed, Err: err, Reason: err.Error()})
		return err
	}
	if len(normalized) == 0 {
		c.Stop()
		return nil
	}
	if c.streamer == nil {
		err := errors.New("tui realtime stream client is not configured")
		c.sendStatus(tuiRealtimeStatus{Kind: tuiRealtimeStatusFailed, Err: err, Reason: err.Error(), Subscriptions: cloneTUIRealtimeSubscriptions(normalized), EndpointCursor: cursor})
		return err
	}

	var oldCancel context.CancelFunc
	c.mu.Lock()
	if c.active != nil && c.active.endpointCursor == cursor && equalTUIRealtimeSubscriptions(c.active.subscriptions, normalized) {
		c.mu.Unlock()
		return nil
	}
	if c.active != nil {
		oldCancel = c.active.cancel
	}
	c.generation++
	generation := c.generation
	ctx, cancel := context.WithCancel(context.Background())
	active := &tuiRealtimeActiveGeneration{
		generation:     generation,
		cancel:         cancel,
		subscriptions:  cloneTUIRealtimeSubscriptions(normalized),
		endpointCursor: cursor,
	}
	c.active = active
	c.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	go c.runGeneration(ctx, active)
	return nil
}

func (c *tuiRealtimeController) Stop() {
	if c == nil {
		return
	}
	var cancel context.CancelFunc
	c.mu.Lock()
	if c.active != nil {
		cancel = c.active.cancel
		c.active = nil
		c.generation++
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *tuiRealtimeController) runGeneration(ctx context.Context, active *tuiRealtimeActiveGeneration) {
	if c == nil || active == nil {
		return
	}
	subscriptions := cloneTUIRealtimeSubscriptions(active.subscriptions)
	endpointCursor := active.endpointCursor
	attempt := 0
	var terminal atomic.Bool

	for {
		if !c.isActiveGeneration(active.generation) || ctx.Err() != nil {
			return
		}
		c.sendStatusIfActive(active.generation, tuiRealtimeStatus{
			Kind:           tuiRealtimeStatusStarted,
			Generation:     active.generation,
			Attempt:        attempt,
			Subscriptions:  cloneTUIRealtimeSubscriptions(subscriptions),
			EndpointCursor: endpointCursor,
		})

		err := c.streamer.StreamSessionsV3Realtime(ctx, cloneTUIRealtimeSubscriptions(subscriptions), func(frame client.V3RealtimeFrame) {
			if ctx.Err() != nil {
				return
			}
			if !c.sendFrameIfActive(active.generation, frame) {
				return
			}
			if tuiRealtimeFrameIsTerminal(frame) {
				terminal.Store(true)
				c.sendStatusIfActive(active.generation, tuiRealtimeStatus{
					Kind:           tuiRealtimeStatusTerminal,
					Generation:     active.generation,
					Attempt:        attempt,
					Subscriptions:  cloneTUIRealtimeSubscriptions(subscriptions),
					EndpointCursor: endpointCursor,
					Frame:          frame,
					Err:            frame.Err(),
					Reason:         tuiRealtimeFrameTerminalReason(frame),
				})
				c.cancelActiveGeneration(active.generation)
			}
		})

		if terminal.Load() || !c.isActiveGeneration(active.generation) || ctx.Err() != nil {
			return
		}
		if err == nil {
			c.sendStatusIfActive(active.generation, tuiRealtimeStatus{
				Kind:           tuiRealtimeStatusStopped,
				Generation:     active.generation,
				Attempt:        attempt,
				Subscriptions:  cloneTUIRealtimeSubscriptions(subscriptions),
				EndpointCursor: endpointCursor,
			})
			c.finishGeneration(active.generation)
			return
		}
		if !tuiRealtimeErrorIsTransient(err) || attempt >= c.retryBudget {
			c.sendStatusIfActive(active.generation, tuiRealtimeStatus{
				Kind:           tuiRealtimeStatusFailed,
				Generation:     active.generation,
				Attempt:        attempt,
				Subscriptions:  cloneTUIRealtimeSubscriptions(subscriptions),
				EndpointCursor: endpointCursor,
				Err:            err,
				Reason:         err.Error(),
			})
			c.finishGeneration(active.generation)
			return
		}
		attempt++
		c.sendStatusIfActive(active.generation, tuiRealtimeStatus{
			Kind:           tuiRealtimeStatusRetrying,
			Generation:     active.generation,
			Attempt:        attempt,
			Subscriptions:  cloneTUIRealtimeSubscriptions(subscriptions),
			EndpointCursor: endpointCursor,
			Err:            err,
			Reason:         err.Error(),
		})
		if !c.sleepWhileActive(ctx, active.generation, c.retryDelay(attempt)) {
			return
		}
	}
}

func (c *tuiRealtimeController) isActiveGeneration(generation uint64) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active != nil && c.active.generation == generation
}

func (c *tuiRealtimeController) cancelActiveGeneration(generation uint64) {
	if c == nil {
		return
	}
	var cancel context.CancelFunc
	c.mu.Lock()
	if c.active != nil && c.active.generation == generation {
		cancel = c.active.cancel
		c.active = nil
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *tuiRealtimeController) finishGeneration(generation uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.active != nil && c.active.generation == generation {
		c.active = nil
	}
	c.mu.Unlock()
}

func (c *tuiRealtimeController) sleepWhileActive(ctx context.Context, generation uint64, delay time.Duration) bool {
	if delay <= 0 {
		return c.isActiveGeneration(generation) && ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return c.isActiveGeneration(generation) && ctx.Err() == nil
	}
}

func (c *tuiRealtimeController) retryDelay(attempt int) time.Duration {
	if c == nil {
		return defaultTUIRealtimeRetryBase
	}
	base := c.retryBase
	if base <= 0 {
		base = defaultTUIRealtimeRetryBase
	}
	maxDelay := c.retryMax
	if maxDelay <= 0 {
		maxDelay = defaultTUIRealtimeRetryMax
	}
	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func (c *tuiRealtimeController) sendFrameIfActive(generation uint64, frame client.V3RealtimeFrame) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.generation != generation {
		return false
	}
	if c.frames != nil {
		select {
		case c.frames <- frame:
		default:
		}
	}
	return true
}

func (c *tuiRealtimeController) sendStatusIfActive(generation uint64, status tuiRealtimeStatus) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.generation != generation {
		return
	}
	c.sendStatusLocked(status)
}

func (c *tuiRealtimeController) sendStatus(status tuiRealtimeStatus) {
	if c == nil {
		return
	}
	c.sendStatusLocked(status)
}

func (c *tuiRealtimeController) sendStatusLocked(status tuiRealtimeStatus) {
	if c == nil || c.statuses == nil {
		return
	}
	status.Subscriptions = cloneTUIRealtimeSubscriptions(status.Subscriptions)
	select {
	case c.statuses <- status:
	default:
	}
}

func normalizeTUIRealtimeSubscriptions(subscriptions []client.V3RealtimeSubscription, endpointCursor string) ([]client.V3RealtimeSubscription, string, error) {
	cursor := strings.TrimSpace(endpointCursor)
	out := make([]client.V3RealtimeSubscription, 0, len(subscriptions))
	seen := make(map[string]struct{}, len(subscriptions))
	for _, sub := range subscriptions {
		sessionID := strings.TrimSpace(sub.SessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			return nil, "", fmt.Errorf("duplicate tui realtime session subscription %q", sessionID)
		}
		seen[sessionID] = struct{}{}
		sub.SessionID = sessionID
		sub.EndpointCursor = strings.TrimSpace(sub.EndpointCursor)
		if cursor == "" && sub.EndpointCursor != "" {
			cursor = sub.EndpointCursor
		}
		sub.SubscriptionID = strings.TrimSpace(sub.SubscriptionID)
		out = append(out, sub)
	}
	for i := range out {
		if cursor != "" {
			out[i].EndpointCursor = cursor
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, cursor, nil
}

func cloneTUIRealtimeSubscriptions(in []client.V3RealtimeSubscription) []client.V3RealtimeSubscription {
	if len(in) == 0 {
		return nil
	}
	out := make([]client.V3RealtimeSubscription, len(in))
	copy(out, in)
	return out
}

func equalTUIRealtimeSubscriptions(a, b []client.V3RealtimeSubscription) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tuiRealtimeFrameIsTerminal(frame client.V3RealtimeFrame) bool {
	kind := strings.ToLower(strings.TrimSpace(frame.Kind))
	if kind == tuiRealtimeKindCursorError || kind == tuiRealtimeKindAuthDenied || kind == tuiRealtimeKindSlowConsumer || kind == tuiRealtimeKindSessionGap {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(frame.ErrorCode), tuiRealtimeKindSessionGap)
}

func tuiRealtimeFrameTerminalReason(frame client.V3RealtimeFrame) string {
	if reason := strings.TrimSpace(frame.ErrorCode); reason != "" {
		return reason
	}
	if reason := strings.TrimSpace(frame.Reason); reason != "" {
		return reason
	}
	if reason := strings.TrimSpace(frame.Error); reason != "" {
		return reason
	}
	return strings.TrimSpace(frame.Kind)
}

func tuiRealtimeErrorIsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connect v3 realtime stream") ||
		strings.Contains(msg, "read v3 realtime stream") ||
		strings.Contains(msg, "websocket") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "temporary")
}
