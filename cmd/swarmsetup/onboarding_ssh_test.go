package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/ui"
)

// Requirement: prerequisiteHandler exposes one explicit SSH decision only after
// password/skip and never hands off early. Fake actions prove refresh, rejection,
// partial key failure, retry and late submissions without OS accounts/services.
func TestDesktopPrerequisiteSSHDecision(t *testing.T) {
	for _, password := range []string{"password", "skip"} {
		for _, choice := range []string{"ssh-add", "ssh-skip"} {
			t.Run(password+"/"+choice, func(t *testing.T) {
				passwordDone, sshDone := false, false
				mutations, keys, installs, handoffs := 0, 0, 0, 0
				failKey := true
				actions := ui.PrerequisiteActions{InitialResume: true, InitialCreated: true, PasswordDone: func() bool { return passwordDone }, SSHRequired: func() bool { return passwordDone && !sshDone }, Complete: func(p []byte) error {
					if !passwordDone {
						passwordDone = true
						mutations++
						return nil
					}
					if !sshDone {
						return nil
					}
					if len(p) != 0 {
						t.Error("password replay")
					}
					installs++
					return nil
				}, ChooseSSH: func(key string, skip bool) error {
					if !passwordDone || sshDone {
						t.Error("invalid phase")
					}
					keys++
					if failKey {
						return errors.New("injected partial failure")
					}
					sshDone = true
					return nil
				}, SSHGuidance: func(address string) string { return "Login NOT verified; configured port 2222" }, Handoff: func() error { handoffs++; return nil }}
				h := prerequisiteHandler("http://127.0.0.1:1234", "cap", actions, make(chan struct{}, 1))
				send := func(method, query, body string) *httptest.ResponseRecorder {
					r := httptest.NewRequest(method, "http://127.0.0.1:1234/cap"+query, strings.NewReader(body))
					r.Header.Set("Origin", "http://127.0.0.1:1234")
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					return w
				}
				decision := `{"action":"` + choice + `","confirm":true}`
				if w := send("POST", "", decision); w.Code != 409 || keys != 0 {
					t.Fatal("early SSH admitted")
				}
				body := `{"action":"skip","confirm":true}`
				if password == "password" {
					body = `{"action":"password","confirm":true,"password":"fixture","password_confirm":"fixture"}`
				}
				if w := send("POST", "", body); w.Code != 200 || !strings.Contains(w.Body.String(), `"ssh_required":true`) || handoffs != 0 || installs != 0 {
					t.Fatal(w.Body.String())
				}
				if w := send("GET", "?status", ""); !strings.Contains(w.Body.String(), `"ssh_required":true`) {
					t.Fatal("refresh lost SSH choice")
				}
				if w := send("POST", "", `{"action":"ssh-add","confirm":true,"username":"other"}`); w.Code != 409 || keys != 0 {
					t.Fatal("retarget accepted")
				}
				if w := send("POST", "", decision); w.Code != 422 || sshDone || handoffs != 0 {
					t.Fatal("partial failure hidden")
				}
				failKey = false
				if w := send("POST", "", decision); w.Code != 200 || !strings.Contains(w.Body.String(), `"ssh_saved":true`) || installs != 0 {
					t.Fatal(w.Body.String())
				}
				if w := send("POST", "", decision); w.Code != 409 || keys != 2 {
					t.Fatal("duplicate SSH mutation")
				}
				if w := send("POST", "", `{"action":"retry","confirm":true}`); w.Code != 200 || handoffs != 1 || installs != 1 || mutations != 1 {
					t.Fatal("did not advance once", w.Body.String())
				}
			})
		}
	}
}
