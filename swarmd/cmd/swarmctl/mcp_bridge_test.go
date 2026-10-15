package main

import (
	"encoding/json"

	"net/http"
	"os"

	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Fake credential: never a real key. The bridge must keep it out of every
// output stream and file.
const mcpTestSecret = "swk_fake-bridge-key-0000"

// mcpTestDaemon is a Unix-socket fixture standing in for the daemon's owner
// socket: it issues fake AI keys, deletes them, and answers /mcp only for a
// live key, echoing the method so forwarding can be checked exactly.
type mcpTestDaemon struct {
	t      *testing.T
	socket string

	mu        sync.Mutex
	now       time.Time
	issued    []string
	live      map[string]bool
	purged    []string
	mintBody  map[string]any
	mcpCalls  []string
	mintCode  int
	mintError string
	mcpCode   int
	requests  int
	authOnKey []string
}

func (d *mcpTestDaemon) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v3/auth/tokens":
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Swarm-Token") != "" {
			d.t.Error("key issuance must ride on socket ownership, not a credential")
		}
		if d.mintCode != 0 {
			w.WriteHeader(d.mintCode)
			message := d.mintError
			if message == "" {
				message = "trusted principal required"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			return
		}
		d.mintBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&d.mintBody)
		id := "key-" + string(rune('1'+len(d.issued)))
		d.issued = append(d.issued, id)
		d.live[id] = true
		secret := mcpTestSecret
		if len(d.issued) > 1 {
			secret = strings.TrimSuffix(mcpTestSecret, "0000") + id
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "token": secret, "record": map[string]any{"id": id, "expires_at": d.now.Add(30 * time.Minute).UnixMilli()}})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v3/auth/tokens/"):
		id := strings.TrimPrefix(r.URL.Path, "/v3/auth/tokens/")
		if r.URL.Query().Get("purge") != "true" || !d.live[id] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(d.live, id)
		d.purged = append(d.purged, id)
		_, _ = w.Write([]byte(`{"ok":true,"deleted":true}`))
	case r.Method == http.MethodPost && r.URL.Path == "/mcp":
		auth := r.Header.Get("Authorization")
		d.authOnKey = append(d.authOnKey, auth)
		if r.Header.Get("Content-Type") != "application/json" {
			d.t.Error("MCP message forwarded without application/json")
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			d.t.Errorf("forwarded message is not the client's JSON: %v", err)
		}
		d.mcpCalls = append(d.mcpCalls, msg.Method)
		if d.mcpCode != 0 {
			w.WriteHeader(d.mcpCode)
			_, _ = w.Write([]byte(`{"error":"Swarm Control requires a scoped bearer token"}`))
			return
		}
		if !strings.HasPrefix(auth, "Bearer swk_") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// Indented on purpose: the bridge must re-emit it as one line.
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"method": msg.Method, "text": "line one\nline two"}})
	default:
		d.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

type fakeOwnerInfo struct {
	os.FileInfo
	uid  uint32
	mode os.FileMode
}

func (f fakeOwnerInfo) Mode() os.FileMode {
	if f.mode != 0 {
		return f.mode
	}
	return f.FileInfo.Mode()
}
func (f fakeOwnerInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }
