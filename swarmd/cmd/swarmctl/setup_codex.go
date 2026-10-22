package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// runSetupCodexLogin signs this daemon in to Codex with the device flow. It
// prints only the verification link and one-time code for the person to
// approve in their own browser; tokens stay inside the daemon.
func runSetupCodexLogin(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup codex-login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	timeout := fs.Duration("timeout", 15*time.Minute, "how long to wait for approval")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid codex-login flags; use --help")
	}
	if fs.NArg() != 0 || *timeout <= 0 || *timeout > time.Hour {
		return errors.New("usage: swarmctl setup codex-login [--timeout 15m]")
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	type login struct {
		SessionID       string `json:"session_id"`
		VerificationURL string `json:"verification_url"`
		UserCode        string `json:"user_code"`
		Status          string `json:"status"`
		Error           string `json:"error"`
		Credential      struct {
			AutoDefaults *struct {
				Applied bool   `json:"applied"`
				Error   string `json:"error"`
			} `json:"auto_defaults"`
		} `json:"credential"`
	}
	var started login
	if err := setupRequest(client, http.MethodPost, "/v1/auth/codex/oauth/start", map[string]any{"provider": "codex", "method": "device", "label": "Codex", "active": true}, &started); err != nil {
		return err
	}
	if started.SessionID == "" || started.VerificationURL == "" || started.UserCode == "" {
		return errors.New("Codex did not return a device code; try again")
	}
	fmt.Fprintf(output, "Open %s and enter the code %s\nWaiting for approval...\n", started.VerificationURL, started.UserCode)
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		var current login
		if err := setupCodexStatus(client, started.SessionID, &current); err != nil {
			return err
		}
		switch strings.ToLower(current.Status) {
		case "success":
			if defaults := current.Credential.AutoDefaults; defaults != nil {
				if defaults.Error != "" {
					return fmt.Errorf("Codex connected, but recommended agent models were not set up: %s; retry with setup model", defaults.Error)
				}
				if _, err := fmt.Fprintf(output, "Recommended agent models applied: %t\n", defaults.Applied); err != nil {
					return err
				}
			}
			_, err := fmt.Fprintln(output, "Codex connected.")
			return err
		case "lost":
			return errors.New("this sign-in is gone (Swarm restarted or it expired); start a new one with swarmctl setup codex-login")
		case "error", "failed", "expired":
			return fmt.Errorf("Codex sign-in %s; run codex-login again", strings.ToLower(current.Status))
		}
	}
	return errors.New("Codex sign-in was not approved in time; run codex-login again")
}

// setupCodexStatus keeps HTTP status handling local to device sign-in: the
// shared setup request contract deliberately withholds rejection bodies.
func setupCodexStatus(client *http.Client, sessionID string, out any) error {
	resp, err := client.Get("http://swarm-local-transport/v1/auth/codex/oauth/status?session_id=" + url.QueryEscape(sessionID))
	if err != nil {
		return errors.New("cannot reach private daemon socket; start the container and run setup as its daemon user")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("this sign-in is gone (Swarm restarted or it expired); start a new one with swarmctl setup codex-login")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("setup request rejected (HTTP %d); check prerequisites and selected inputs; response withheld to protect credentials", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("invalid setup response; operation may have been saved, inspect setup status before retrying")
	}
	return nil
}
