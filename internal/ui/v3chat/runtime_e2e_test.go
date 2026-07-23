package v3chat

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

func TestV3NewSessionServerSequenceStreamsThenCommitsVisibleState(t *testing.T) {
	var (
		sequenceMu sync.Mutex
		sequence   []string
		liveOnce   sync.Once
		titleOnce  sync.Once
	)
	resumeRead := make(chan struct{})
	messageSeen := make(chan struct{})
	allowDurable := make(chan struct{})
	liveSeen := make(chan struct{})
	titleSeen := make(chan struct{})
	record := func(step string) {
		sequenceMu.Lock()
		sequence = append(sequence, step)
		sequenceMu.Unlock()
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			record("create")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-e2e", "title": "created", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-e2e", "last_event_seq": 0},
				"messages":   []any{}, "events": []any{}, "snapshot_endpoint_cursor": "cursor-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			conn, rw, err := hijackV3ChatTestWebsocket(w, r)
			if err != nil {
				t.Errorf("hijack realtime: %v", err)
				return
			}
			defer conn.Close()
			_, payload, err := readV3ChatClientFrame(rw)
			if err != nil {
				t.Errorf("read resume: %v", err)
				return
			}
			var resume map[string]any
			if err := json.Unmarshal(payload, &resume); err != nil {
				t.Errorf("decode resume: %v", err)
				return
			}
			capabilities, _ := resume["capabilities"].([]any)
			if resume["kind"] != "resume" || resume["endpoint_cursor"] != "cursor-1" || len(capabilities) != 1 || capabilities[0] != client.V3RealtimeCapabilityLivePatchV1 {
				t.Errorf("resume = %#v", resume)
				return
			}
			record("realtime-ready")
			close(resumeRead)
			<-messageSeen
			writeV3ChatServerFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "live.patch", "session_id": "session-e2e", "live": map[string]any{"session_id": "session-e2e", "run_id": "run-1", "stream_id": "assistant:run-1", "stream_kind": "assistant_text", "operation": "append", "live_seq_start": 1, "live_seq_end": 1, "offset_start": 0, "offset_end": 6, "text": "hello "}})
			writeV3ChatServerFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "live.patch", "session_id": "session-e2e", "live": map[string]any{"session_id": "session-e2e", "run_id": "run-1", "stream_id": "assistant:run-1", "stream_kind": "assistant_text", "operation": "append", "live_seq_start": 2, "live_seq_end": 2, "offset_start": 6, "offset_end": 11, "text": "world"}})
			<-allowDurable
			writeV3ChatServerFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-e2e", "event_type": "message.appended", "endpoint_cursor": "cursor-2", "last_seq": 1, "rev": 1, "prevRev": 0, "event": map[string]any{"id": "event-assistant", "session_id": "session-e2e", "seq": 1, "event_type": "message.appended", "payload": map[string]any{"message": map[string]any{"id": "assistant-1", "session_id": "session-e2e", "global_seq": 2, "role": "assistant", "content": "hello world", "metadata": map[string]any{"run_id": "run-1"}}}}})
			writeV3ChatServerFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-e2e", "event_type": "session.title.updated", "endpoint_cursor": "cursor-3", "last_seq": 2, "rev": 2, "prevRev": 1, "event": map[string]any{"id": "event-title", "session_id": "session-e2e", "seq": 2, "event_type": "session.title.updated", "payload": map[string]any{"title": "renamed live"}}})
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-e2e/messages":
			select {
			case <-resumeRead:
			case <-time.After(2 * time.Second):
				t.Error("message mutation arrived before realtime resume")
				return
			}
			record("message")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode message: %v", err)
				return
			}
			close(messageSeen)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-e2e", "title": "created", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-e2e", "last_event_seq": 0},
				"message":    map[string]any{"id": body["message_id"], "session_id": "session-e2e", "global_seq": 1, "role": "user", "content": body["content"], "metadata": body["metadata"]},
				"run_intent": map[string]any{"session_id": "session-e2e", "run_id": "run-1", "status": "running"},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")
	var runtime *Runtime
	runtime = NewRuntime(api, nil, func() {
		if runtime == nil {
			return
		}
		state := runtime.Store().Snapshot()
		live := SelectLiveSegments(state)
		if len(live) == 1 && live[0].Text == "hello world" {
			liveOnce.Do(func() { close(liveSeen) })
		}
		if SelectTitle(state) == "renamed live" {
			titleOnce.Do(func() { close(titleSeen) })
		}
	})
	defer runtime.Stop()
	_, err := runtime.CreateAndSend(context.Background(), NewSessionRequest{
		Create:        client.SessionCreateOptions{Title: "created", WorkspacePath: "/workspace", WorkspaceBindingID: "binding", SwarmID: "host", Mode: "auto", AgentName: "swarm"},
		InitialPrompt: "question",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-liveSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("live assistant patches were not projected immediately")
	}
	liveState := runtime.Store().Snapshot()
	if live := SelectLiveSegments(liveState); len(live) != 1 || live[0].Text != "hello world" {
		t.Fatalf("visible live state = %#v", live)
	}
	page := NewPage(runtime, testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 18)
	page.Draw(screen)
	screen.Show()
	liveDrawn := simulationText(screen, 80, 18)
	if !containsAll(liveDrawn, "question", "hello world") {
		t.Fatalf("live assistant text was not rendered before durable reconciliation:\n%s", liveDrawn)
	}
	close(allowDurable)
	select {
	case <-titleSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("durable assistant/title events were not projected immediately")
	}

	state := runtime.Store().Snapshot()
	messages := SelectMessages(state)
	if SelectTitle(state) != "renamed live" || len(messages) != 2 || messages[0].Content != "question" || messages[1].Content != "hello world" || len(SelectLiveSegments(state)) != 0 {
		t.Fatalf("final canonical state = %#v", state)
	}
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 80, 18)
	if !containsAll(drawn, "renamed live", "question", "hello world") || strings.Count(drawn, "hello world") != 1 {
		t.Fatalf("visible page did not reconcile to one durable assistant message:\n%s", drawn)
	}
	sequenceMu.Lock()
	gotSequence := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	if !reflect.DeepEqual(gotSequence, []string{"create", "realtime-ready", "message"}) {
		t.Fatalf("server request ordering = %#v", gotSequence)
	}
}

func containsAll(value string, wanted ...string) bool {
	for _, item := range wanted {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}

func hijackV3ChatTestWebsocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	hash := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(hash[:]) + "\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	return conn, rw, err
}

func readV3ChatClientFrame(reader io.Reader) (byte, []byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(reader, head); err != nil {
		return 0, nil, err
	}
	length := uint64(head[1] & 0x7f)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	} else if length == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if head[1]&0x80 != 0 {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return head[0] & 0x0f, payload, nil
}

func writeV3ChatServerFrame(t *testing.T, writer io.Writer, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	header := []byte{0x81}
	if len(raw) <= 125 {
		header = append(header, byte(len(raw)))
	} else {
		header = append(header, 126, byte(len(raw)>>8), byte(len(raw)))
	}
	if _, err := writer.Write(append(header, raw...)); err != nil {
		t.Errorf("write realtime frame: %v", err)
	}
}
