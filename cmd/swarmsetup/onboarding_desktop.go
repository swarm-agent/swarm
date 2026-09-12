package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/ui"
)

//go:embed onboarding_desktop.html
var prerequisiteHTML string

// This short-lived local setup host is not the daemon. It exposes no shell or
// generic password-reset API. An unguessable per-launch URL, exact Host/Origin, strict body
// bounds, and a single-flight action protect the explicit account operation.
func prerequisiteHandler(origin, capability string, actions ui.PrerequisiteActions, done chan<- struct{}) http.Handler {
	var mu sync.Mutex
	completed := false
	guidance := ""
	begun, created := actions.InitialResume, actions.InitialCreated
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+capability+"'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != strings.TrimPrefix(origin, "http://") || r.URL.Path != "/"+capability {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.RawQuery == "status" {
			w.Header().Set("Content-Type", "application/json")
			state := map[string]any{}
			if actions.Status != nil {
				state["message"] = actions.Status()
			}
			if mu.TryLock() {
				state["begun"], state["created"], state["completed"] = begun, created, completed
				state["guidance"] = guidance
				state["ssh_required"] = actions.SSHRequired != nil && actions.SSHRequired()
				if actions.PasswordDone != nil {
					state["retry"] = actions.PasswordDone()
				}
				if completed && actions.Destination != nil {
					state["destination"] = actions.Destination()
				}
				mu.Unlock()
			} else {
				state["busy"] = true
			}
			_ = json.NewEncoder(w).Encode(state)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, strings.ReplaceAll(prerequisiteHTML, "{{NONCE}}", capability))
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Origin") != origin || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "Request rejected", http.StatusForbidden)
			return
		}
		var input struct {
			Username        string `json:"username"`
			Confirm         bool   `json:"confirm"`
			Cancel          bool   `json:"cancel"`
			Action          string `json:"action"`
			Password        string `json:"password"`
			PasswordConfirm string `json:"password_confirm"`
			PublicKey       string `json:"public_key"`
			Address         string `json:"address"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&input); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		if dec.Decode(new(any)) != io.EOF {
			http.Error(w, "Invalid request", 400)
			return
		}
		defer func() { input.Password, input.PasswordConfirm = "", "" }()
		if !mu.TryLock() {
			http.Error(w, "Setup already running", 409)
			return
		}
		defer mu.Unlock()
		if completed {
			http.Error(w, "Setup already finished", 409)
			return
		}
		if input.Cancel {
			completed = true
			w.WriteHeader(204)
			time.AfterFunc(time.Second, func() {
				select {
				case done <- struct{}{}:
				default:
				}
			})
			return
		}
		fail := func(message string, status int) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "retry": actions.PasswordDone != nil && actions.PasswordDone(), "ssh_required": actions.SSHRequired != nil && actions.SSHRequired()})
		}
		if !input.Confirm {
			fail("Confirm your account setup first", 400)
			return
		}
		if input.Action != "ssh-add" && input.Action != "ssh-skip" && (input.PublicKey != "" || input.Address != "") {
			fail("Unexpected SSH fields", 400)
			return
		}
		switch input.Action {
		case "ssh-add", "ssh-skip":
			if !begun || !created || actions.SSHRequired == nil || !actions.SSHRequired() || actions.ChooseSSH == nil || input.Username != "" || input.Password != "" || input.PasswordConfirm != "" || len(input.Address) > 64 {
				fail("SSH choice unavailable or invalid", 409)
				return
			}
			skip := input.Action == "ssh-skip"
			if err := actions.ChooseSSH(input.PublicKey, skip); err != nil {
				fail(err.Error(), 422)
				return
			}
			message := "SSH key skipped."
			if !skip {
				message = "SSH public key installed.\n"
				if actions.SSHGuidance != nil {
					message += actions.SSHGuidance(input.Address)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			guidance = message
			_ = json.NewEncoder(w).Encode(map[string]any{"ssh_saved": true, "guidance": message})
			return
		case "begin":
			if begun {
				fail("Account already selected", 409)
				return
			}
			if strings.TrimSpace(input.Username) == "" || input.Password != "" || input.PasswordConfirm != "" {
				fail("Invalid account request", 400)
				return
			}
			var err error
			created, err = actions.Begin(input.Username)
			if err != nil {
				fail(err.Error(), 422)
				return
			}
			begun = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"created": created})
			return
		case "password", "skip", "retry":
			if !begun || input.Username != "" {
				fail("Create or select your account first", 409)
				return
			}
			if input.Action == "password" && actions.PasswordDone != nil && actions.PasswordDone() {
				fail("Password choice already saved; retry the current stage instead", 409)
				return
			}
			if input.Action == "retry" && created && (actions.PasswordDone == nil || !actions.PasswordDone()) {
				fail("No completed password stage to retry", 409)
				return
			}
			if input.Action != "password" && (input.Password != "" || input.PasswordConfirm != "") {
				fail("Skip cannot include a password", 400)
				return
			}
			if input.Action == "password" && (!created || input.Password == "" || input.Password != input.PasswordConfirm) {
				fail("Enter matching passwords for the new account", 400)
				return
			}
		default:
			fail("Invalid setup action", 400)
			return
		}
		password := []byte(input.Password)
		defer clear(password)
		if err := launcher.ValidateOnboardingPassword(password); err != nil {
			fail(err.Error(), 400)
			return
		}
		if err := actions.Complete(password); err != nil {
			fail(err.Error(), 422)
			return
		}
		if actions.SSHRequired != nil && actions.SSHRequired() {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"ssh_required": true})
			return
		}
		if actions.Handoff != nil {
			if err := actions.Handoff(); err != nil {
				fail(err.Error(), 422)
				return
			}
		}
		completed = true
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"ok": true}
		if actions.Destination != nil {
			response["destination"] = actions.Destination()
		}
		_ = json.NewEncoder(w).Encode(response)
		// Give the browser time to show completion before the launcher opens Identity.
		time.AfterFunc(time.Second, func() {
			select {
			case done <- struct{}{}:
			default:
			}
		})
	})
}

func runDesktopPrerequisite(actions ui.PrerequisiteActions) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		return err
	}
	capability := hex.EncodeToString(bytes)
	origin := "http://" + listener.Addr().String()
	done := make(chan struct{}, 1)
	server := &http.Server{Handler: prerequisiteHandler(origin, capability, actions, done), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	if err = launcher.OpenBrowser(origin + "/" + capability); err != nil {
		return fmt.Errorf("could not open the Desktop setup window; no account changed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return nil
	case err = <-served:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-time.After(30 * time.Minute):
		return fmt.Errorf("device setup expired; open Swarm again")
	}
}
