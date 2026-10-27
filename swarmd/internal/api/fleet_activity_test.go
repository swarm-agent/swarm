package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const fleetActivityTestTitle = "private-title-never-in-the-feed"

type fleetActivityTestFrame struct {
	event string
	raw   string
	data  map[string]any
}

// fleetActivityTestLink links b to a as machine "b" and returns a server for
// a's fleet activity routes as a's owner.
func fleetActivityTestLink(t *testing.T) (*fleetTestSwarm, *fleetTestSwarm, *httptest.Server) {
	t.Helper()
	a := newFleetTestSwarm(t, "home")
	b := newFleetTestSwarm(t, "machine")
	t.Cleanup(b.cleanup)
	t.Cleanup(a.cleanup)
	body, _ := json.Marshal(map[string]string{"code": b.invite(t), "name": "b"})
	if code, out := a.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: %d %v", code, out)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.s.handleFleetActivity(w, requestWithTestPrincipalForAccount(r, a.user, a.account))
	}))
	t.Cleanup(server.Close)
	return a, b, server
}

func fleetActivityTestSession(t *testing.T, f *fleetTestSwarm, id string) {
	t.Helper()
	snapshot := pebblestore.SessionSnapshot{ID: id, UserID: f.user, AccountScopeID: f.account, Title: fleetActivityTestTitle}
	if _, err := f.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: f.user, AccountScopeID: f.account, Session: &snapshot, ClientRequestID: id, IdempotencyKey: id, PayloadHash: id, NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
}

func fleetActivityTestSetWork(t *testing.T, f *fleetTestSwarm, machine, level string) {
	t.Helper()
	account, err := f.s.sessions.Store().GetAccountPermissions(f.account)
	if err != nil {
		t.Fatal(err)
	}
	account.Linked[machine] = pebblestore.LinkedPermissionRule{Work: level}
	if _, err := f.s.sessions.Store().Underlying().SetAccountPermissions(f.account, account.Revision, account); err != nil {
		t.Fatal(err)
	}
}

// fleetActivityTestOpen opens the stream and delivers its frames in order.
func fleetActivityTestOpen(t *testing.T, server *httptest.Server, query string) (<-chan fleetActivityTestFrame, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+fleetActivityStreamPath+query, nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		t.Fatalf("stream: HTTP %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	frames := make(chan fleetActivityTestFrame, 1024)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		event := ""
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				raw := strings.TrimPrefix(line, "data: ")
				var data map[string]any
				_ = json.Unmarshal([]byte(raw), &data)
				frames <- fleetActivityTestFrame{event: event, raw: raw, data: data}
			}
		}
	}()
	return frames, cancel
}

// fleetActivityTestAwait reads frames until match, returning every frame read.
func fleetActivityTestAwait(t *testing.T, frames <-chan fleetActivityTestFrame, timeout time.Duration, what string, match func(fleetActivityTestFrame) bool) []fleetActivityTestFrame {
	t.Helper()
	seen := []fleetActivityTestFrame{}
	deadline := time.After(timeout)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("stream ended waiting for %s; saw %v", what, seen)
			}
			if strings.Contains(frame.raw, fleetActivityTestTitle) {
				t.Fatalf("content crossed the feed: %s", frame.raw)
			}
			seen = append(seen, frame)
			if match(frame) {
				return seen
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s; saw %v", what, seen)
		}
	}
}

func fleetActivityTestIs(event, machine, key, value string) func(fleetActivityTestFrame) bool {
	return func(f fleetActivityTestFrame) bool {
		return f.event == event && f.data["machine"] == machine && (key == "" || f.data[key] == value)
	}
}

// Purpose: the home's fleet stream merges its own activity with a linked
// machine's, read through the pinned link, each frame tagged by machine, with
// per-machine cursors that resume each machine where it was.
// Threat: a frame without (or with the wrong) machine; one machine's cursor
// applied to another; replay of what a client already saw; content (titles)
// crossing the feed. Boundary: Server.handleFleetActivity over two in-process
// daemons, the second reached only through its link listener.
func TestFleetActivityStreamMerges(t *testing.T) {
	a, b, server := fleetActivityTestLink(t)
	fleetActivityTestSession(t, a, "home-first")
	fleetActivityTestSession(t, b, "box-first")

	frames, stop := fleetActivityTestOpen(t, server, "")
	seen := fleetActivityTestAwait(t, frames, 10*time.Second, "home's first session", fleetActivityTestIs("activity", "home", "session_id", "home-first"))
	seen = append(seen, fleetActivityTestAwait(t, frames, 10*time.Second, "b's first session", fleetActivityTestIs("activity", "b", "session_id", "box-first"))...)
	online := map[string]bool{}
	for _, f := range seen {
		if f.event == "machine" && f.data["kind"] == "machine" && f.data["state"] == "online" {
			online[f.data["machine"].(string)] = true
		}
		if f.event == "activity" {
			allowed := map[string]bool{"machine": true, "seq": true, "at": true, "kind": true, "session_id": true, "job_id": true, "worker_id": true, "worker_name": true, "project_id": true, "state": true, "summary": true, "tool_name": true, "file_path": true}
			for key := range f.data {
				if !allowed[key] {
					t.Fatalf("activity frame field %q: %s", key, f.raw)
				}
			}
			if f.data["kind"] != "session.created" || f.data["state"] != "started" {
				t.Fatalf("activity frame: %s", f.raw)
			}
		}
	}
	if !online["home"] {
		t.Fatalf("home not announced online: %v", seen)
	}
	// Live: a new session on the machine arrives tagged with it.
	fleetActivityTestSession(t, b, "box-live")
	fleetActivityTestAwait(t, frames, 10*time.Second, "b's live session", fleetActivityTestIs("activity", "b", "session_id", "box-live"))
	// Each machine's cursor after its last record, from the frames as they come.
	fleetActivityTestSession(t, a, "home-live")
	cursors, live := map[string]string{}, map[string]bool{"b": true}
	fleetActivityTestAwait(t, frames, 10*time.Second, "both cursors after the live records", func(f fleetActivityTestFrame) bool {
		machine, _ := f.data["machine"].(string)
		if f.event == "activity" && f.data["session_id"] == "home-live" {
			live["home"] = true
		}
		if f.event == "cursor" && f.data["kind"] == "cursor" && live[machine] {
			cursors[machine] = f.data["cursor"].(string)
		}
		return cursors["home"] != "" && cursors["b"] != ""
	})
	stop()

	// Resume: only what came after each machine's cursor.
	fleetActivityTestSession(t, a, "home-after")
	fleetActivityTestSession(t, b, "box-after")
	frames, stop = fleetActivityTestOpen(t, server, "?cursors=home:"+cursors["home"]+",b:"+cursors["b"])
	defer stop()
	got := map[string]string{}
	for len(got) < 2 {
		frame := fleetActivityTestAwait(t, frames, 10*time.Second, "resumed activity", func(f fleetActivityTestFrame) bool { return f.event == "activity" })
		last := frame[len(frame)-1]
		machine := last.data["machine"].(string)
		if _, dup := got[machine]; !dup {
			got[machine] = last.data["session_id"].(string)
		}
	}
	if got["home"] != "home-after" || got["b"] != "box-after" {
		t.Fatalf("resume replayed seen records: %v (cursors %v)", got, cursors)
	}
}

// Purpose: a machine's gap is forwarded tagged with that machine and the
// machine resumes from the gap's cursor without being called offline; a
// machine that drops is announced offline and online again when it returns,
// while the home's own activity keeps flowing.
// Threat: one machine's loss hidden or blamed on another; a dropped machine
// stalling the stream; a gap mistaken for an outage.
// Boundary: Server.handleFleetActivity, the link listener's close and reopen.
func TestFleetActivityStreamGapsAndOutages(t *testing.T) {
	a, b, server := fleetActivityTestLink(t)
	frames, stop := fleetActivityTestOpen(t, server, "?cursors=home:999999,b:999999")
	defer stop()
	for _, name := range []string{"home", "b"} {
		seen := fleetActivityTestAwait(t, frames, 10*time.Second, name+" gap", fleetActivityTestIs("gap", name, "kind", "gap"))
		gap := seen[len(seen)-1].data
		if gap["from"] != float64(1000000) || gap["cursor"] == "999999" {
			t.Fatalf("%s gap: %v", name, gap)
		}
	}
	fleetActivityTestSession(t, b, "box-after-gap")
	seen := fleetActivityTestAwait(t, frames, 10*time.Second, "b resumed after its gap", fleetActivityTestIs("activity", "b", "session_id", "box-after-gap"))
	for _, f := range seen {
		if f.event == "machine" && f.data["state"] == "offline" {
			t.Fatalf("a gap reported as an outage: %s", f.raw)
		}
	}

	address := b.s.fleetListenAddress()
	b.s.CloseFleetListener()
	seen = fleetActivityTestAwait(t, frames, 10*time.Second, "b offline", fleetActivityTestIs("machine", "b", "state", "offline"))
	if errText, _ := seen[len(seen)-1].data["error"].(string); errText == "" {
		t.Fatalf("offline without a reason: %s", seen[len(seen)-1].raw)
	}
	// The home keeps flowing while b is away.
	fleetActivityTestSession(t, a, "home-while-b-away")
	fleetActivityTestAwait(t, frames, 10*time.Second, "home during b's outage", fleetActivityTestIs("activity", "home", "session_id", "home-while-b-away"))
	fleetActivityTestSession(t, b, "box-while-away")
	if _, err := b.s.listenForHomes(address); err != nil {
		t.Fatal(err)
	}
	fleetActivityTestAwait(t, frames, 15*time.Second, "b online again", fleetActivityTestIs("machine", "b", "state", "online"))
	fleetActivityTestAwait(t, frames, 10*time.Second, "b's record from its outage", fleetActivityTestIs("activity", "b", "session_id", "box-while-away"))
}

// Purpose: a machine whose linked work rule is off is not read at all, by the
// stream or the page, and its absence is said once.
// Threat: reading a machine the person turned off. Boundary: the roster in
// Server.handleFleetActivity; the machine record's last contact proves no call.
func TestFleetActivityWorkOffIsNotRead(t *testing.T) {
	a, b, server := fleetActivityTestLink(t)
	fleetActivityTestSetWork(t, a, "b", "off")
	fleetActivityTestSession(t, b, "box-hidden")
	fleetActivityTestSession(t, a, "home-shown")
	before, _, _ := a.s.fleet.GetMachine("b")
	time.Sleep(5 * time.Millisecond) // a contact would move LastContactAt
	frames, stop := fleetActivityTestOpen(t, server, "")
	seen := fleetActivityTestAwait(t, frames, 10*time.Second, "b not shown", fleetActivityTestIs("machine", "b", "state", "not_shown"))
	if seen[len(seen)-1].data["reason"] != fleetActivityOffReason {
		t.Fatalf("not_shown: %s", seen[len(seen)-1].raw)
	}
	seen = fleetActivityTestAwait(t, frames, 10*time.Second, "home's session", fleetActivityTestIs("activity", "home", "session_id", "home-shown"))
	stop()
	resp, err := server.Client().Get(server.URL + fleetActivityPagePath)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Events   []map[string]any  `json:"events"`
		Machines map[string]string `json:"machines"`
		Cursors  map[string]string `json:"cursors"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if page.Machines["b"] != "not_shown" || page.Machines["home"] != "online" || page.Cursors["b"] != "" {
		t.Fatalf("page: %+v", page)
	}
	for _, f := range append(seen, fleetActivityTestFrame{}) {
		if f.data["machine"] == "b" && f.event != "machine" {
			t.Fatalf("b was read: %s", f.raw)
		}
	}
	for _, event := range page.Events {
		if event["machine"] == "b" {
			t.Fatalf("b was read by the page: %v", event)
		}
	}
	after, _, _ := a.s.fleet.GetMachine("b")
	if after.LastContactAt != before.LastContactAt || after.LastError != before.LastError {
		t.Fatal("b was contacted")
	}
}

// Purpose: the page is the same merge as one bounded replay page per machine,
// with per-machine cursors, tagged gaps and machine states, offline included.
// Boundary: Server.serveFleetActivityPage over two in-process daemons.
func TestFleetActivityPage(t *testing.T) {
	a, b, server := fleetActivityTestLink(t)
	fleetActivityTestSession(t, a, "home-page")
	fleetActivityTestSession(t, b, "box-page")
	type page struct {
		Events   []map[string]any  `json:"events"`
		Cursors  map[string]string `json:"cursors"`
		Gaps     []map[string]any  `json:"gaps"`
		Machines map[string]string `json:"machines"`
	}
	get := func(query string) page {
		resp, err := server.Client().Get(server.URL + fleetActivityPagePath + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out page
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
			t.Fatalf("page: HTTP %d", resp.StatusCode)
		}
		return out
	}
	first := get("")
	found := map[string]bool{}
	for _, event := range first.Events {
		found[event["machine"].(string)+"/"+event["session_id"].(string)] = true
		if _, ok := event["title"]; ok {
			t.Fatalf("content in page: %v", event)
		}
	}
	if !found["home/home-page"] || !found["b/box-page"] || first.Machines["home"] != "online" || first.Machines["b"] != "online" || first.Cursors["home"] == "" || first.Cursors["b"] == "" || len(first.Gaps) != 0 {
		t.Fatalf("first page: %+v", first)
	}
	again := get("?cursors=home:" + first.Cursors["home"] + ",b:" + first.Cursors["b"])
	if len(again.Events) != 0 {
		t.Fatalf("resumed page replayed: %+v", again.Events)
	}
	ahead := get("?cursors=b:999999")
	if len(ahead.Gaps) != 1 || ahead.Gaps[0]["machine"] != "b" || ahead.Cursors["b"] != first.Cursors["b"] {
		t.Fatalf("gap page: %+v", ahead)
	}
	b.s.CloseFleetListener()
	offline := get("?cursors=b:" + first.Cursors["b"])
	if offline.Machines["b"] != "offline" || offline.Cursors["b"] != first.Cursors["b"] || offline.Machines["home"] != "online" {
		t.Fatalf("offline page: %+v", offline)
	}
	if code := func() int {
		resp, err := server.Client().Get(server.URL + fleetActivityPagePath + "?cursors=b")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}(); code != http.StatusBadRequest {
		t.Fatalf("malformed cursors: HTTP %d", code)
	}
}

// Purpose: the fleet feed takes the same scope as /v3/activity, not the owner
// scope of the other fleet routes, and refuses gateway tokens.
// Threat: a key without sessions:read, or an application gateway's token,
// reading the fleet. Boundary: the routed Handler and handleFleetActivity.
func TestFleetActivityScope(t *testing.T) {
	a, _, _ := fleetActivityTestLink(t)
	handler := a.s.withDesktopBoundary(a.s.Handler())
	call := func(token, path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Host = "127.0.0.1"
		req.Header.Set("X-Swarm-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	reader, _, err := a.s.security.CreateScopedTokenUnder(nil, "reader", []string{"sessions:read"}, a.account, a.user, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	writer, _, err := a.s.security.CreateScopedTokenUnder(nil, "writer", []string{"sessions:write"}, a.account, a.user, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code := call(reader, fleetActivityPagePath); code != http.StatusOK {
		t.Fatalf("sessions:read key: HTTP %d", code)
	}
	if code := call(reader, "/v3/fleet"); code != http.StatusForbidden {
		t.Fatalf("sessions:read key reached an owner fleet route: HTTP %d", code)
	}
	if code := call(writer, fleetActivityPagePath); code != http.StatusForbidden {
		t.Fatalf("key without sessions:read: HTTP %d", code)
	}
	if code := call("", fleetActivityPagePath); code != http.StatusUnauthorized {
		t.Fatalf("no credential: HTTP %d", code)
	}
	for _, path := range []string{fleetActivityPagePath, fleetActivityStreamPath} {
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, path, nil), a.user, a.account)
		req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}, AgentName: "support", AccountScopeID: a.account, UserID: a.user})
		w := httptest.NewRecorder()
		a.s.handleFleetActivity(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("gateway token %s: HTTP %d", path, w.Code)
		}
	}
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, fleetActivityPagePath, nil), a.user, a.account)
	w := httptest.NewRecorder()
	a.s.handleFleetActivity(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: HTTP %d", w.Code)
	}
}
