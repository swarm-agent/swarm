package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Purpose: setup slot creation must send no credential material and print the
// daemon's exact manual login command. Reordering must preserve all supplied
// slot IDs in order. A private-socket fixture is the narrowest CLI boundary;
// malformed invocations must make no request and must never read secret stdin.
func TestSetupClaudeCodeAccounts(t *testing.T) {
	var calls int
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/claudecode/accounts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if calls == 1 {
			if string(raw) != "{}" {
				t.Errorf("slot request=%q", raw)
			}
			io.WriteString(w, `{"id":"test-slot","login_command":"exact manual login command"}`)
		} else {
			var req struct {
				Order []string `json:"order"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Error(err)
			}
			if strings.Join(req.Order, ",") != "second,default,third" {
				t.Errorf("order=%v", req.Order)
			}
			io.WriteString(w, `{"ok":true}`)
		}
	})
	var out bytes.Buffer
	if err := runSetup([]string{"add-credential", "--socket", socket, "--provider", "claudecode"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "exact manual login command") {
		t.Fatalf("output=%s", out.String())
	}
	if err := runSetup([]string{"credential-order", "--socket", socket, "--provider", "claudecode", "second,default,third"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := runSetup([]string{"add-credential", "--socket", socket, "--provider", "claudecode", "--api-key-stdin"}, strings.NewReader("not-a-credential"), &out); err == nil {
		t.Fatal("accepted secret input")
	}
	if err := runSetup([]string{"credential-order", "--socket", socket, "--provider", "anthropic", "default"}, nil, &out); err == nil {
		t.Fatal("accepted unsupported order")
	}
	if calls != 2 {
		t.Fatalf("invalid invocation made request; calls=%d", calls)
	}
}
