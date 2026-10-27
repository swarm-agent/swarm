package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
)

// The fleet's activity: one metadata-only feed on a home that merges its own
// activity (activity.go, the same in-process subscription and outbox) with
// each linked machine's GET /v3/activity/stream, read through the link as that
// machine's home. Every frame names its machine; each machine keeps its own
// cursor. A machine is read only while linked.{name}.work is ask or allowed.
// What a machine sends is untrusted: only the known metadata fields of an
// activity record are kept, strings are clipped, and nothing is acted on.
//
//	GET /v3/fleet/activity/stream?cursors=home:12,box:40   live SSE
//	GET /v3/fleet/activity?cursors=home:12,box:40          one replay page

const (
	fleetActivityPagePath   = "/v3/fleet/activity"
	fleetActivityStreamPath = "/v3/fleet/activity/stream"

	fleetActivityMinBackoff = 2 * time.Second
	fleetActivityMaxBackoff = 60 * time.Second
	// After a gap a machine's feed resumes from the gap's cursor.
	fleetActivityResume = time.Second
	// An upstream that sends nothing, not even a heartbeat, this long is dropped.
	fleetActivityIdle        = 3 * activityHeartbeat
	fleetActivityFrameLimit  = 64 << 10
	fleetActivityPageLimit   = 4 << 20
	fleetActivityPageTimeout = 10 * time.Second
	fleetActivityFieldLimit  = 1024
	fleetActivityOffReason   = "linked work rule is off"
)

type fleetActivityEvent struct {
	Machine string `json:"machine"`
	activityEvent
}

type fleetActivityCursorFrame struct {
	Kind    string `json:"kind"`
	Machine string `json:"machine"`
	Cursor  string `json:"cursor"`
}

type fleetActivityGapFrame struct {
	Kind    string `json:"kind"`
	Machine string `json:"machine"`
	From    uint64 `json:"from"`
	To      uint64 `json:"to"`
	Cursor  string `json:"cursor"`
}

type fleetActivityMachineFrame struct {
	Kind    string `json:"kind"`
	Machine string `json:"machine"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type fleetActivityMachine struct {
	name   string
	shown  bool
	reason string
}

// parseFleetActivityCursors reads cursors=machine:cursor,...; a machine left
// out starts at 0, as the per-machine feed does.
func parseFleetActivityCursors(raw string) (map[string]uint64, error) {
	out := map[string]uint64{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, ":")
		cursor, err := strconv.ParseUint(value, 10, 64)
		if !ok || strings.TrimSpace(name) == "" || err != nil {
			return nil, errors.New("invalid cursors: give machine:cursor pairs separated by commas")
		}
		if len(out) >= 256 {
			return nil, errors.New("too many cursors")
		}
		out[strings.TrimSpace(name)] = cursor
	}
	return out, nil
}

// fleetActivityRoster is this Swarm's own fleet name and each machine linked
// for the account, with whether its activity may be read.
func (s *Server) fleetActivityRoster(p identity.Principal) (string, []fleetActivityMachine, error) {
	home := s.fleetName()
	if home == "" {
		return "", nil, errors.New("this Swarm has no name to show its activity under: give it one (its setup, or --name NAME)")
	}
	if s.fleet == nil || s.fleetTokens == nil {
		return home, nil, nil
	}
	linked, err := s.fleet.ListMachines()
	if err != nil {
		return home, nil, err
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return home, nil, err
	}
	machines := []fleetActivityMachine{}
	for _, machine := range linked {
		if machine.AccountScopeID != p.AccountScopeID {
			continue
		}
		entry := fleetActivityMachine{name: machine.Name, shown: true}
		switch {
		case machine.Name == home:
			entry.shown, entry.reason = false, "it has this Swarm's own name"
		case access.Link(machine.Name).Work != "ask" && access.Link(machine.Name).Work != "allowed":
			entry.shown, entry.reason = false, fleetActivityOffReason
		}
		machines = append(machines, entry)
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].name < machines[j].name })
	return home, machines, nil
}

// fleetActivityGatewayCaller is an application gateway's token: it serves one
// sealed agent and never reads the fleet.
func fleetActivityGatewayCaller(r *http.Request) bool {
	if token, ok := ScopedTokenFromRequest(r); ok && strings.TrimSpace(token.AgentName) != "" {
		return true
	}
	_, bound := r.Context().Value(applicationAgentContextKey{}).(applicationAgentBinding)
	return bound
}

func (s *Server) handleFleetActivity(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read the fleet's activity"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil || s.v3RealtimeOutbox == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("activity unavailable"))
		return
	}
	cursors, err := parseFleetActivityCursors(r.URL.Query().Get("cursors"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	home, machines, err := s.fleetActivityRoster(principal)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if r.URL.Path == fleetActivityPagePath {
		s.serveFleetActivityPage(w, r, principal, home, machines, cursors)
		return
	}
	s.serveFleetActivityStream(w, r, principal, home, machines, cursors)
}

type fleetActivityFrame struct {
	event string
	data  []byte
}

// fleetActivityFeed is one fleet stream: each source pushes its frames in its
// own order; one writer sends them.
type fleetActivityFeed struct {
	s         *Server
	principal identity.Principal
	out       chan fleetActivityFrame
	mu        sync.Mutex
	cursors   map[string]uint64
}

func (f *fleetActivityFeed) push(ctx context.Context, event string, value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	select {
	case f.out <- fleetActivityFrame{event: event, data: data}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (f *fleetActivityFeed) cursor(name string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursors[name]
}

func (f *fleetActivityFeed) advance(ctx context.Context, name string, cursor uint64) bool {
	f.mu.Lock()
	f.cursors[name] = cursor
	f.mu.Unlock()
	return f.push(ctx, "cursor", fleetActivityCursorFrame{Kind: "cursor", Machine: name, Cursor: strconv.FormatUint(cursor, 10)})
}

// gap forwards a machine's gap and moves its cursor to where it resumes.
func (f *fleetActivityFeed) gap(ctx context.Context, name string, gap activityGap) bool {
	if !f.push(ctx, "gap", fleetActivityGapFrame{Kind: "gap", Machine: name, From: gap.From, To: gap.To, Cursor: strconv.FormatUint(gap.Cursor, 10)}) {
		return false
	}
	return f.advance(ctx, name, gap.Cursor)
}

func (f *fleetActivityFeed) state(ctx context.Context, name, state, detail string) bool {
	frame := fleetActivityMachineFrame{Kind: "machine", Machine: name, State: state}
	if state == "offline" {
		frame.Error = detail
	} else {
		frame.Reason = detail
	}
	return f.push(ctx, "machine", frame)
}

func waitFleetActivity(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) serveFleetActivityStream(w http.ResponseWriter, r *http.Request, principal identity.Principal, home string, machines []fleetActivityMachine, cursors map[string]uint64) {
	// One stream on this Swarm; each upstream is one stream on its machine.
	if s.activityStreams.Add(1) > activityStreamLimit {
		s.activityStreams.Add(-1)
		writeError(w, http.StatusTooManyRequests, errors.New("activity stream limit"))
		return
	}
	defer s.activityStreams.Add(-1)
	ctx, cancel := context.WithCancel(r.Context())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	feed := &fleetActivityFeed{s: s, principal: principal, out: make(chan fleetActivityFrame, 64), cursors: cursors}
	sub := s.v3RealtimeOutbox.subscribe()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(frame fleetActivityFrame) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.event, frame.data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	comment := func(text string) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, ": %s\n\n", text); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	// The home's subscription exists before the client is told it is connected.
	if !comment("connected") {
		s.v3RealtimeOutbox.unsubscribe(sub)
		return
	}
	feed.state(ctx, home, "online", "")
	wg.Add(1)
	go func() {
		defer wg.Done()
		feed.followHome(ctx, cancel, home, sub)
	}()

	running := map[string]context.CancelFunc{}
	announced := map[string]bool{}
	notShown := func(name, reason string) bool {
		if announced[name] {
			return true
		}
		announced[name] = true
		data, _ := json.Marshal(fleetActivityMachineFrame{Kind: "machine", Machine: name, State: "not_shown", Reason: reason})
		return write(fleetActivityFrame{event: "machine", data: data})
	}
	apply := func(machines []fleetActivityMachine) bool {
		listed := map[string]bool{}
		for _, machine := range machines {
			listed[machine.name] = true
			if !machine.shown {
				if stop, ok := running[machine.name]; ok {
					stop()
					delete(running, machine.name)
				}
				if !notShown(machine.name, machine.reason) {
					return false
				}
				continue
			}
			if _, ok := running[machine.name]; ok {
				continue
			}
			announced[machine.name] = false
			machineCtx, stop := context.WithCancel(ctx)
			running[machine.name] = stop
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				feed.followMachine(machineCtx, name)
			}(machine.name)
		}
		for name, stop := range running {
			if !listed[name] {
				stop()
				delete(running, name)
				if !notShown(name, "unlinked") {
					return false
				}
			}
		}
		return true
	}
	if !apply(machines) {
		return
	}
	ticker := time.NewTicker(activityHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-feed.out:
			if !write(frame) {
				return
			}
		case <-ticker.C:
			if !comment("heartbeat") {
				return
			}
			// Links and work rules are read again on each heartbeat: a machine
			// whose rule turned off stops being read; a new one joins.
			if _, current, err := s.fleetActivityRoster(principal); err == nil && !apply(current) {
				return
			}
		}
	}
}

// followHome follows this Swarm's own activity. A gap is forwarded and the
// feed resumes from its cursor; any other end closes the whole stream.
func (f *fleetActivityFeed) followHome(ctx context.Context, closeAll context.CancelFunc, name string, sub *v3RealtimeOutboxSubscriber) {
	defer func() { f.s.v3RealtimeOutbox.unsubscribe(sub) }()
	emit := func(kind string, value any) bool {
		switch v := value.(type) {
		case activityEvent:
			return f.push(ctx, "activity", fleetActivityEvent{Machine: name, activityEvent: v})
		case map[string]string:
			cursor, err := strconv.ParseUint(v["cursor"], 10, 64)
			return err == nil && f.advance(ctx, name, cursor)
		}
		return false
	}
	for {
		gap := f.s.followActivity(ctx, sub, f.principal, f.cursor(name), "", "", emit, nil, nil)
		if gap == nil {
			closeAll()
			return
		}
		if !f.gap(ctx, name, *gap) {
			return
		}
		f.s.v3RealtimeOutbox.unsubscribe(sub)
		sub = nil
		if !waitFleetActivity(ctx, fleetActivityResume) {
			return
		}
		sub = f.s.v3RealtimeOutbox.subscribe()
	}
}

// followMachine follows one linked machine's feed through the link, from its
// cursor, reconnecting with capped backoff without holding up the others.
func (f *fleetActivityFeed) followMachine(ctx context.Context, name string) {
	backoff, state := fleetActivityMinBackoff, ""
	for ctx.Err() == nil {
		client, machine, status, err := f.s.fleetMachineClient(f.principal.AccountScopeID, name)
		if status == http.StatusNotFound {
			return // unlinked: the roster says so on its next read
		}
		resume := false
		if err == nil {
			resume, err = f.readMachine(ctx, client, name, func(callErr error) {
				f.s.noteFleetContact(machine, callErr)
			}, func() bool {
				backoff = fleetActivityMinBackoff
				if state == "online" {
					return true
				}
				state = "online"
				return f.state(ctx, name, "online", "")
			})
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if resume && !waitFleetActivity(ctx, fleetActivityResume) {
				return
			}
			continue
		}
		if state != "offline" {
			state = "offline"
			if !f.state(ctx, name, "offline", fleetActivityShortError(err)) {
				return
			}
		}
		if !waitFleetActivity(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, fleetActivityMaxBackoff)
	}
}

var (
	errFleetActivityEnded   = errors.New("stream ended")
	errFleetActivityIdle    = errors.New("no heartbeat")
	errFleetActivityInvalid = errors.New("invalid stream")
)

// fleetActivityShortError says why a machine is offline without carrying the
// machine's own words: a status, or this Swarm's transport error, clipped.
func fleetActivityShortError(err error) string {
	var refusal *fleet.Error
	if errors.As(err, &refusal) {
		return "HTTP " + strconv.Itoa(refusal.Status)
	}
	return clipActivityText(err.Error(), 160)
}

// readMachine reads one connection to a machine's feed. It returns resume
// when the machine closed after a gap (its cursor moved to the gap's).
func (f *fleetActivityFeed) readMachine(ctx context.Context, client *fleet.Client, name string, contact func(error), connected func() bool) (bool, error) {
	upstream, cancel := context.WithCancel(ctx)
	defer cancel()
	path := activityStreamPath + "?cursor=" + strconv.FormatUint(f.cursor(name), 10)
	resp, err := client.Do(upstream, http.MethodGet, path, nil, http.Header{"Accept": {"text/event-stream"}})
	if err == nil && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		err = &fleet.Error{Status: resp.StatusCode}
	}
	contact(err)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return false, errFleetActivityInvalid
	}
	if !connected() {
		return false, nil
	}
	var idle atomic.Bool
	timer := time.AfterFunc(fleetActivityIdle, func() {
		idle.Store(true)
		cancel()
	})
	defer timer.Stop()
	reader := bufio.NewReaderSize(resp.Body, fleetActivityFrameLimit)
	event, data, sawGap := "", []byte(nil), false
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return false, errFleetActivityInvalid
		}
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return false, nil
			case idle.Load():
				return false, errFleetActivityIdle
			case err == io.EOF && sawGap:
				return true, nil
			case err == io.EOF:
				return false, errFleetActivityEnded
			}
			return false, err
		}
		timer.Reset(fleetActivityIdle)
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			if event != "" || len(data) > 0 {
				gap, err := f.forward(ctx, name, event, data)
				if err != nil {
					return false, err
				}
				sawGap = sawGap || gap
			}
			event, data = "", nil
		case line[0] == ':':
		case bytes.HasPrefix(line, []byte("event:")):
			event = strings.TrimSpace(string(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimPrefix(line[len("data:"):], []byte(" "))...)
			if len(data) > fleetActivityFrameLimit {
				return false, errFleetActivityInvalid
			}
		}
	}
}

// forward re-encodes one machine frame under its machine name: only the known
// metadata fields survive. It reports whether the frame was a gap.
func (f *fleetActivityFeed) forward(ctx context.Context, name, event string, data []byte) (bool, error) {
	switch event {
	case "activity":
		var record activityEvent
		if json.Unmarshal(data, &record) != nil || record.Kind == "" {
			return false, errFleetActivityInvalid
		}
		f.push(ctx, "activity", fleetActivityEvent{Machine: name, activityEvent: clipActivityEvent(record)})
	case "cursor":
		var frame struct {
			Cursor string `json:"cursor"`
		}
		if json.Unmarshal(data, &frame) != nil {
			return false, errFleetActivityInvalid
		}
		cursor, err := strconv.ParseUint(frame.Cursor, 10, 64)
		if err != nil {
			return false, errFleetActivityInvalid
		}
		f.advance(ctx, name, cursor)
	case "gap":
		var frame struct {
			From   uint64 `json:"from"`
			To     uint64 `json:"to"`
			Cursor string `json:"cursor"`
		}
		if json.Unmarshal(data, &frame) != nil {
			return false, errFleetActivityInvalid
		}
		cursor, err := strconv.ParseUint(frame.Cursor, 10, 64)
		if err != nil {
			return false, errFleetActivityInvalid
		}
		f.gap(ctx, name, activityGap{From: frame.From, To: frame.To, Cursor: cursor})
		return true, nil
	}
	return false, nil
}

func clipActivityText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}

func clipActivityEvent(event activityEvent) activityEvent {
	for _, field := range []*string{&event.Kind, &event.SessionID, &event.JobID, &event.WorkerID, &event.WorkerName, &event.ProjectID, &event.State, &event.Summary, &event.ToolName, &event.FilePath} {
		*field = clipActivityText(*field, fleetActivityFieldLimit)
		if !utf8.ValidString(*field) {
			*field = strings.ToValidUTF8(*field, "")
		}
	}
	return event
}

type fleetActivityPageGap struct {
	Machine string `json:"machine"`
	Cursor  string `json:"cursor"`
}

// serveFleetActivityPage is one bounded replay page per machine, read in
// parallel; a machine that does not answer keeps its cursor.
func (s *Server) serveFleetActivityPage(w http.ResponseWriter, r *http.Request, principal identity.Principal, home string, machines []fleetActivityMachine, cursors map[string]uint64) {
	events, next, gap, err := s.activityPage(principal, cursors[home], "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("activity replay failed"))
		return
	}
	out := make([]fleetActivityEvent, 0, len(events))
	for _, event := range events {
		out = append(out, fleetActivityEvent{Machine: home, activityEvent: event})
	}
	pageCursors := map[string]string{home: strconv.FormatUint(next, 10)}
	gaps := []fleetActivityPageGap{}
	if gap {
		gaps = append(gaps, fleetActivityPageGap{Machine: home, Cursor: strconv.FormatUint(next, 10)})
	}
	states := map[string]string{home: "online"}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, machine := range machines {
		if !machine.shown {
			states[machine.name] = "not_shown"
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			page, err := s.fleetActivityMachinePage(r.Context(), principal, name, cursors[name])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				states[name] = "offline"
				pageCursors[name] = strconv.FormatUint(cursors[name], 10)
				return
			}
			states[name] = "online"
			pageCursors[name] = page.Cursor
			if page.Gap {
				gaps = append(gaps, fleetActivityPageGap{Machine: name, Cursor: page.Cursor})
			}
			for _, event := range page.Events {
				out = append(out, fleetActivityEvent{Machine: name, activityEvent: clipActivityEvent(event)})
			}
		}(machine.name)
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At < out[j].At
		}
		return out[i].Machine < out[j].Machine
	})
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Machine < gaps[j].Machine })
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "cursors": pageCursors, "gaps": gaps, "machines": states})
}

type fleetActivityMachinePage struct {
	Events []activityEvent `json:"events"`
	Cursor string          `json:"cursor"`
	Gap    bool            `json:"gap"`
}

func (s *Server) fleetActivityMachinePage(ctx context.Context, principal identity.Principal, name string, cursor uint64) (fleetActivityMachinePage, error) {
	var page fleetActivityMachinePage
	client, machine, _, err := s.fleetMachineClient(principal.AccountScopeID, name)
	if err != nil {
		return page, err
	}
	ctx, cancel := context.WithTimeout(ctx, fleetActivityPageTimeout)
	defer cancel()
	resp, err := client.Do(ctx, http.MethodGet, activityPagePath+"?cursor="+strconv.FormatUint(cursor, 10), nil, http.Header{"Accept": {"application/json"}})
	if err == nil && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		err = &fleet.Error{Status: resp.StatusCode}
	}
	s.noteFleetContact(machine, err)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, fleetActivityPageLimit+1))
	if err != nil {
		return page, err
	}
	if len(data) > fleetActivityPageLimit || json.Unmarshal(data, &page) != nil || len(page.Events) > 256 {
		return page, errFleetActivityInvalid
	}
	if _, err := strconv.ParseUint(page.Cursor, 10, 64); err != nil {
		return page, errFleetActivityInvalid
	}
	for _, event := range page.Events {
		if event.Kind == "" {
			return page, errFleetActivityInvalid
		}
	}
	return page, nil
}
