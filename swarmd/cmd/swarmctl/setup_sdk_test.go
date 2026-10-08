package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Requirement: SDK credential issuance uses private IPC, fixed session scopes,
// bounded expiry and explicit secret export. runSetup is the authority; a Unix
// fixture proves exact requests and rejection before any token is minted.
func TestSetupSDKToken(t *testing.T) {
	calls := 0
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" {
			t.Error("wrong method")
		}
		if r.URL.Path == "/v3/auth/tokens/token-id/revoke" {
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		if r.URL.Path != "/v3/auth/tokens" {
			t.Error("wrong route")
		}
		var body struct {
			Scopes  []string `json:"scopes"`
			Seconds int      `json:"expires_in_seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(body.Scopes, []string{"sessions:read", "sessions:write"}) || body.Seconds != 3600 {
			t.Error("incorrect authority or lifetime")
		}
		fmt.Fprint(w, `{"token":"swk_fixture-only","record":{"id":"token-id"},"private":"do-not-export"}`)
	})
	var out bytes.Buffer
	if err := runSetup([]string{"sdk-token", "--socket", socket}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	var exported map[string]string
	if err := json.Unmarshal(out.Bytes(), &exported); err != nil || len(exported) != 2 || exported["id"] != "token-id" || exported["token"] != "swk_fixture-only" {
		t.Fatal("invalid export")
	}
	out.Reset()
	if err := runSetup([]string{"revoke-sdk-token", "--socket", socket, "--id", "token-id"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{{"--expires-in-seconds=0"}, {"--expires-in-seconds=86401"}, {"--scopes=admin"}, {"--token=do-not-echo"}} {
		out.Reset()
		if err := runSetup(append([]string{"sdk-token", "--socket", socket}, flags...), nil, &out); err == nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("unsafe arguments accepted or echoed")
		}
		if out.Len() != 0 {
			t.Fatal("output on validation failure")
		}
	}
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "unsafe.json"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Chmod(0644); err != nil {
		t.Fatal(err)
	}
	if err := runSetup([]string{"sdk-token", "--socket", socket}, nil, f); err == nil {
		t.Fatal("accepted public output file")
	}
	if calls != 2 {
		t.Fatalf("validation minted credential: calls %d", calls)
	}
}

// Requirement: --ai-access mints an AI key by level only (the daemon derives
// its scopes), refuses other authority, and allows up to a year.
func TestSetupSDKTokenAIAccess(t *testing.T) {
	var got map[string]any
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"token":"swk_ai","record":{"id":"ai-id"}}`)
	})
	var out bytes.Buffer
	if err := runSetup([]string{"sdk-token", "--socket", socket, "--name", "claude", "--ai-access", "read", "--expires-in-seconds", "2592000"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]any{"name": "claude", "ai_access": "read", "expires_in_seconds": float64(2592000)}) {
		t.Fatalf("payload = %v", got)
	}
	for _, flags := range [][]string{{"--ai-access", "admin"}, {"--ai-access", "write", "--workers"}, {"--ai-access", "read", "--agent", "frontdesk"}, {"--ai-access", "read", "--expires-in-seconds", "40000000"}} {
		out.Reset()
		if err := runSetup(append([]string{"sdk-token", "--socket", socket}, flags...), nil, &out); err == nil {
			t.Fatalf("accepted %v", flags)
		}
	}
}
