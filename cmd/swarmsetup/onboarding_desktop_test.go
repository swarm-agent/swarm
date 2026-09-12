package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"swarm-refactor/swarmtui/internal/ui"
	"testing"
	"time"
)

// Requirement: the privileged prerequisite action admits only explicit consent
// from its exact launch origin/capability, once; rejected requests and Cancel
// must not invoke account provisioning. Real HTTP handler + fake operation is
// the narrowest hermetic proof of this temporary listener's mutation boundary.
func TestDesktopPrerequisiteAdmission(t *testing.T) {
	calls := 0
	done := make(chan struct{}, 1)
	h := prerequisiteHandler("http://127.0.0.1:1234", "capability", ui.PrerequisiteActions{Begin: func(name string) (bool, error) {
		calls++
		if name != "developer" {
			t.Fatal(name)
		}
		return true, nil
	}, Complete: func([]byte) error { return nil }}, done)
	request := func(path, host, origin, body string) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1:1234"+path, strings.NewReader(body))
		r.Host = host
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	body := `{"action":"begin","username":"developer","confirm":true}`
	for _, tc := range []struct {
		path, host, origin, body string
		code                     int
	}{
		{"/wrong", "127.0.0.1:1234", "http://127.0.0.1:1234", body, 404},
		{"/capability", "attacker.invalid", "http://127.0.0.1:1234", body, 404},
		{"/capability", "127.0.0.1:1234", "http://attacker.invalid", body, 403},
		{"/capability", "127.0.0.1:1234", "http://127.0.0.1:1234", `{"username":"developer"}`, 400},
	} {
		if got := request(tc.path, tc.host, tc.origin, tc.body); got != tc.code {
			t.Fatalf("status %d", got)
		}
		if calls != 0 {
			t.Fatal("rejection mutated account")
		}
	}
	if got := request("/capability", "127.0.0.1:1234", "http://127.0.0.1:1234", body); got != 200 {
		t.Fatal(got)
	}
	if got := request("/capability", "127.0.0.1:1234", "http://127.0.0.1:1234", body); got != 409 || calls != 1 {
		t.Fatalf("repeat %d calls %d", got, calls)
	}
}

func TestDesktopPrerequisiteCancel(t *testing.T) {
	calls := 0
	done := make(chan struct{}, 1)
	h := prerequisiteHandler("http://127.0.0.1:1234", "cap", ui.PrerequisiteActions{Begin: func(string) (bool, error) { calls++; return true, nil }, Complete: func([]byte) error { calls++; return nil }}, done)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1234/cap", strings.NewReader(`{"cancel":true}`))
	r.Header.Set("Origin", "http://127.0.0.1:1234")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close setup")
	}
	if w.Code != 204 || calls != 0 {
		t.Fatalf("cancel %d calls %d", w.Code, calls)
	}
}

// Requirement: password entry is a second consented action bound to this launch's
// created account. Invalid/replayed bodies and failures cannot silently install.
func TestDesktopPrerequisitePasswordAndSkip(t *testing.T) {
	for _, mode := range []string{"password", "skip", "existing"} {
		t.Run(mode, func(t *testing.T) {
			begins, finishes := 0, 0
			h := prerequisiteHandler("http://127.0.0.1:1234", "cap", ui.PrerequisiteActions{Begin: func(string) (bool, error) { begins++; return mode != "existing", nil }, Complete: func(p []byte) error {
				finishes++
				want := ""
				if mode == "password" {
					want = "test-only-password"
				}
				if string(p) != want {
					t.Fatal("wrong password handoff")
				}
				return nil
			}}, make(chan struct{}, 1))
			send := func(body string) int {
				r := httptest.NewRequest("POST", "http://127.0.0.1:1234/cap", strings.NewReader(body))
				r.Header.Set("Origin", "http://127.0.0.1:1234")
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if strings.Contains(w.Body.String(), "test-only-password") {
					t.Fatal("password echoed")
				}
				return w.Code
			}
			if send(`{"action":"skip","confirm":true}`) != 409 {
				t.Fatal("completion before account")
			}
			if send(`{"action":"begin","username":"developer","confirm":true} {}`) != 400 || begins != 0 {
				t.Fatal("trailing body mutated")
			}
			if send(`{"action":"begin","username":"developer","confirm":true}`) != 200 || begins != 1 {
				t.Fatal("begin failed")
			}
			for _, body := range []string{`{"action":"password","confirm":true,"password":"test-only-password","password_confirm":"different"}`, `{"action":"skip","confirm":true,"password":"test-only-password"}`, `{"action":"password","confirm":true,"password":"bad\nvalue","password_confirm":"bad\nvalue"}`} {
				if send(body) != 400 || finishes != 0 {
					t.Fatal("invalid password mutated")
				}
			}
			body := `{"action":"skip","confirm":true}`
			if mode == "password" {
				body = `{"action":"password","confirm":true,"password":"test-only-password","password_confirm":"test-only-password"}`
			}
			if mode == "existing" {
				if send(`{"action":"password","confirm":true,"password":"test-only-password","password_confirm":"test-only-password"}`) != 400 || finishes != 0 {
					t.Fatal("existing password reset")
				}
			}
			if send(body) != 200 || finishes != 1 {
				t.Fatal("completion failed")
			}
			if send(body) != 409 || finishes != 1 {
				t.Fatal("replayed completion")
			}
		})
	}
}
