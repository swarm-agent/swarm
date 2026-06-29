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
	StreamV3Realtime(ctx context.Context, options client.V3RealtimeResumeOptions, onFrame func(client.V3RealtimeFrame)) error
}

type tuiRealtimeStatusKind string

type tuiRealtimeStatus struct {
	Kind           tuiRealtimeStatusKind
	Generation     uint64
	Attempt        int
	Subscriptions  []client.V3RealtimeSubscription
	Worksets       []client.V3RealtimeWorksetSubscription
	EndpointCursor string
	SessionID      string
	Frame          client.V3RealtimeFrame
	Err            error
	Reason         string
}

type tuiRealtimeController struct {
	mu sync.Mutex

	streamer tuiRealtimeStreamClient
	frames   chan<- client.V3RealtimeFrame
	statuses chan<- tuiRealtimeStatus
	wake     func()

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
	worksets       []client.V3RealtimeWorksetSubscription
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

func (c *tuiRealtimeController) SetWake(wake func()) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.wake = wake
	c.mu.Unlock()
}

func (c *tuiRealtimeController) Start(subscriptions []client.V3RealtimeSubscription, endpointCursor string) error {
	return c.Reconcile(subscriptions, nil, endpointCursor)
}

func (c *tuiRealtimeController) Reconcile(subscriptions []client.V3RealtimeSubscription, worksets []client.V3RealtimeWorksetSubscription, endpointCursor string) error {
	if c == nil {
		return nil
	}
	normalizedSubs, normalizedWorksets, cursor, err := normalizeTUIRealtimeResume(subscriptions, worksets, endpointCursor)
	if err != nil {
		c.sendStatus(tuiRealtimeStatus{Kind: tuiRealtimeStatusFailed, Err: err, Reason: err.Error()})
		return err
	}
	if len(normalizedSubs) == 0 && len(normalizedWorksets) == 0 {
		c.Stop()
		return nil
	}
	if c.streamer == nil {
		err := errors.New("tui realtime stream client is not configured")
		c.sendStatus(tuiRealtimeStatus{Kind: tuiRealtimeStatusFailed, Err: err, Reason: err.Error(), Subscriptions: cloneTUIRealtimeSubscriptions(normalizedSubs), Worksets: cloneTUIRealtimeWorksets(normalizedWorksets), EndpointCursor: cursor})
		return err
	}

	var oldCancel context.CancelFunc
	c.mu.Lock()
	if c.active != nil && c.active.endpointCursor == cursor && equalTUIRealtimeSubscriptions(c.active.subscriptions, normalizedSubs) && equalTUIRealtimeWorksets(c.active.worksets, normalizedWorksets) {
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
		subscriptions:  cloneTUIRealtimeSubscriptions(normalizedSubs),
		worksets:       cloneTUIRealtimeWorksets(normalizedWorksets),
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
	worksets := cloneTUIRealtimeWorksets(active.worksets)
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
			Worksets:       cloneTUIRealtimeWorksets(worksets),
			EndpointCursor: endpointCursor,
		})

		err := c.streamer.StreamV3Realtime(ctx, client.V3RealtimeResumeOptions{EndpointCursor: endpointCursor, Surface: "tui", Subscriptions: cloneTUIRealtimeSubscriptions(subscriptions), Worksets: cloneTUIRealtimeWorksets(worksets)}, func(frame client.V3RealtimeFrame) {
			if ctx.Err() != nil {
				return
			}
			if !c.sendFrameIfActive(ctx, active.generation, frame) {
				return
			}
			advanceTUIRealtimeResumeState(&endpointCursor, subscriptions, frame)
			c.advanceActiveStateIfActive(active.generation, endpointCursor, subscriptions)
			if tuiRealtimeFrameIsTerminal(frame) {
				terminal.Store(true)
				c.sendStatusIfActive(active.generation, tuiRealtimeStatus{
					Kind:           tuiRealtimeStatusTerminal,
					Generation:     active.generation,
					Attempt:        attempt,
					Subscriptions:  cloneTUIRealtimeSubscriptions(subscriptions),
					Worksets:       cloneTUIRealtimeWorksets(worksets),
					EndpointCursor: endpointCursor,
					SessionID:      firstNonEmpty(strings.TrimSpace(frame.SessionID), frameSessionID(frame)),
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
				Worksets:       cloneTUIRealtimeWorksets(worksets),
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
				Worksets:       cloneTUIRealtimeWorksets(worksets),
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
			Worksets:       cloneTUIRealtimeWorksets(worksets),
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

func (c *tuiRealtimeController) advanceActiveStateIfActive(generation uint64, endpointCursor string, subscriptions []client.V3RealtimeSubscription) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.generation != generation {
		return
	}
	if cursor := strings.TrimSpace(endpointCursor); cursor != "" {
		c.active.endpointCursor = cursor
	}
	c.active.subscriptions = cloneTUIRealtimeSubscriptions(subscriptions)
}

func (c *tuiRealtimeController) sendFrameIfActive(ctx context.Context, generation uint64, frame client.V3RealtimeFrame) bool {
	if c == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	var wake func()
	var frames chan<- client.V3RealtimeFrame
	c.mu.Lock()
	if c.active == nil || c.active.generation != generation {
		c.mu.Unlock()
		return false
	}
	frames = c.frames
	wake = c.wake
	c.mu.Unlock()
	if frames == nil {
		return true
	}
	if ctx == nil {
		frames <- frame
	} else {
		select {
		case frames <- frame:
		case <-ctx.Done():
			return false
		}
	}
	if !c.isActiveGeneration(generation) {
		return false
	}
	if wake != nil {
		wake()
	}
	return true
}

func (c *tuiRealtimeController) sendStatusIfActive(generation uint64, status tuiRealtimeStatus) {
	if c == nil {
		return
	}
	var wake func()
	c.mu.Lock()
	if c.active == nil || c.active.generation != generation {
		c.mu.Unlock()
		return
	}
	if c.sendStatusLocked(status) {
		wake = c.wake
	}
	c.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (c *tuiRealtimeController) sendStatus(status tuiRealtimeStatus) {
	if c == nil {
		return
	}
	var wake func()
	c.mu.Lock()
	if c.sendStatusLocked(status) {
		wake = c.wake
	}
	c.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (c *tuiRealtimeController) sendStatusLocked(status tuiRealtimeStatus) bool {
	if c == nil || c.statuses == nil {
		return false
	}
	status.Subscriptions = cloneTUIRealtimeSubscriptions(status.Subscriptions)
	status.Worksets = cloneTUIRealtimeWorksets(status.Worksets)
	select {
	case c.statuses <- status:
		return true
	default:
		return false
	}
}

func normalizeTUIRealtimeResume(subscriptions []client.V3RealtimeSubscription, worksets []client.V3RealtimeWorksetSubscription, endpointCursor string) ([]client.V3RealtimeSubscription, []client.V3RealtimeWorksetSubscription, string, error) {
	cursor := strings.TrimSpace(endpointCursor)
	outSubs := make([]client.V3RealtimeSubscription, 0, len(subscriptions))
	seen := make(map[string]struct{}, len(subscriptions))
	for _, sub := range subscriptions {
		sessionID := strings.TrimSpace(sub.SessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			return nil, nil, "", fmt.Errorf("duplicate tui realtime session subscription %q", sessionID)
		}
		seen[sessionID] = struct{}{}
		sub.SessionID = sessionID
		sub.EndpointCursor = strings.TrimSpace(sub.EndpointCursor)
		if cursor == "" && sub.EndpointCursor != "" {
			cursor = sub.EndpointCursor
		}
		sub.SubscriptionID = strings.TrimSpace(sub.SubscriptionID)
		outSubs = append(outSubs, sub)
	}
	for i := range outSubs {
		if cursor != "" {
			outSubs[i].EndpointCursor = cursor
		}
	}
	sort.Slice(outSubs, func(i, j int) bool { return outSubs[i].SessionID < outSubs[j].SessionID })

	outWorksets := make([]client.V3RealtimeWorksetSubscription, 0, len(worksets))
	seenWorksets := make(map[string]struct{}, len(worksets))
	for _, workset := range worksets {
		worksetID := strings.TrimSpace(workset.WorksetID)
		if worksetID == "" {
			continue
		}
		if _, ok := seenWorksets[worksetID]; ok {
			return nil, nil, "", fmt.Errorf("duplicate tui realtime workset subscription %q", worksetID)
		}
		seenWorksets[worksetID] = struct{}{}
		workset.WorksetID = worksetID
		workset.SubscriptionID = strings.TrimSpace(workset.SubscriptionID)
		if workset.SubscriptionID == "" {
			return nil, nil, "", fmt.Errorf("tui realtime workset %q requires subscription_id", worksetID)
		}
		workset.Surface = strings.TrimSpace(workset.Surface)
		workset.Selector.Kind = strings.TrimSpace(workset.Selector.Kind)
		workset.Selector.WorkspacePath = strings.TrimSpace(workset.Selector.WorkspacePath)
		workset.Selector.WorkspacePaths = trimTUIRealtimeStrings(workset.Selector.WorkspacePaths)
		workset.Selector.SessionIDs = trimTUIRealtimeStrings(workset.Selector.SessionIDs)
		workset.Selector.Recent.BeforeSessionID = strings.TrimSpace(workset.Selector.Recent.BeforeSessionID)
		workset.Resources = trimTUIRealtimeStrings(workset.Resources)
		outWorksets = append(outWorksets, workset)
	}
	sort.Slice(outWorksets, func(i, j int) bool { return outWorksets[i].WorksetID < outWorksets[j].WorksetID })
	if cursor == "" && (len(outSubs) > 0 || len(outWorksets) > 0) {
		return nil, nil, "", errors.New("tui realtime endpoint cursor is required")
	}
	return outSubs, outWorksets, cursor, nil
}

func advanceTUIRealtimeResumeState(endpointCursor *string, subscriptions []client.V3RealtimeSubscription, frame client.V3RealtimeFrame) {
	if endpointCursor != nil {
		if cursor := strings.TrimSpace(frame.EndpointCursor); cursor != "" {
			*endpointCursor = cursor
			for i := range subscriptions {
				subscriptions[i].EndpointCursor = cursor
			}
		}
	}
	sessionID := strings.TrimSpace(frame.SessionID)
	if sessionID == "" {
		return
	}
	lastSeq := frame.LastSeq
	if frame.Event != nil && frame.Event.Seq > lastSeq {
		lastSeq = frame.Event.Seq
	}
	if lastSeq == 0 {
		return
	}
	for i := range subscriptions {
		if subscriptions[i].SessionID == sessionID && subscriptions[i].LastSeq < lastSeq {
			subscriptions[i].LastSeq = lastSeq
		}
	}
}

func trimTUIRealtimeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cloneTUIRealtimeSubscriptions(in []client.V3RealtimeSubscription) []client.V3RealtimeSubscription {
	if len(in) == 0 {
		return nil
	}
	out := make([]client.V3RealtimeSubscription, len(in))
	copy(out, in)
	return out
}

func cloneTUIRealtimeWorksets(in []client.V3RealtimeWorksetSubscription) []client.V3RealtimeWorksetSubscription {
	if len(in) == 0 {
		return nil
	}
	out := make([]client.V3RealtimeWorksetSubscription, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Selector.WorkspacePaths = append([]string(nil), in[i].Selector.WorkspacePaths...)
		out[i].Selector.SessionIDs = append([]string(nil), in[i].Selector.SessionIDs...)
		out[i].Resources = append([]string(nil), in[i].Resources...)
	}
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

func equalTUIRealtimeWorksets(a, b []client.V3RealtimeWorksetSubscription) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].WorksetID != b[i].WorksetID ||
			a[i].SubscriptionID != b[i].SubscriptionID ||
			a[i].Surface != b[i].Surface ||
			a[i].AutoSubscribeSessions != b[i].AutoSubscribeSessions ||
			a[i].Selector.Kind != b[i].Selector.Kind ||
			a[i].Selector.Global != b[i].Selector.Global ||
			a[i].Selector.WorkspacePath != b[i].Selector.WorkspacePath ||
			a[i].Selector.Recent != b[i].Selector.Recent ||
			!equalStringSlices(a[i].Selector.WorkspacePaths, b[i].Selector.WorkspacePaths) ||
			!equalStringSlices(a[i].Selector.SessionIDs, b[i].Selector.SessionIDs) ||
			!equalStringSlices(a[i].Resources, b[i].Resources) {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
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
