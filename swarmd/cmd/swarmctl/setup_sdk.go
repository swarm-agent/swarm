package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func runSetupSDKToken(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	name := fs.String("name", "headless-sdk", "token name")
	seconds := fs.Int64("expires-in-seconds", 3600, "token lifetime (60-86400 seconds)")
	id := fs.String("id", "", "token record ID to revoke")
	workers := fs.Bool("workers", false, "also allow creating, managing and tasking workers (Swarm Control worker tools)")
	usageLimits := fs.Bool("usage-limits", false, "also allow changing the account's daily usage limits")
	settings := fs.Bool("settings", false, "also allow changing agent role default models")
	agent := fs.String("agent", "", "mint a gateway token limited to this sealed agent's sessions and client tool calls (lifetime up to 30 days)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid SDK token flags; use --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected SDK token arguments")
	}
	revoke := args[0] == "revoke-sdk-token"
	if revoke && strings.TrimSpace(*id) == "" {
		return errors.New("--id is required")
	}
	maxSeconds := int64(86400)
	if strings.TrimSpace(*agent) != "" {
		if *workers || *usageLimits || *settings {
			return errors.New("--agent tokens take no other authority")
		}
		maxSeconds = 30 * 86400
	}
	if !revoke {
		if *seconds < 60 || *seconds > maxSeconds || strings.TrimSpace(*name) == "" {
			return fmt.Errorf("token name and lifetime of 60-%d seconds required", maxSeconds)
		}
		// Token export is explicit, pipe/file only. Never display it in a terminal.
		if f, ok := output.(*os.File); ok {
			st, err := f.Stat()
			if err != nil || st.Mode()&os.ModeCharDevice != 0 {
				return errors.New("redirect token output to a private file or pipe; do not use a terminal")
			}
			if st.Mode().IsRegular() && st.Mode().Perm()&0077 != 0 {
				return errors.New("token output file must have private permissions (0600)")
			}
		}
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	if revoke {
		var result struct {
			OK bool `json:"ok"`
		}
		if err := setupRequest(client, http.MethodPost, "/v3/auth/tokens/"+url.PathEscape(strings.TrimSpace(*id))+"/revoke", nil, &result); err != nil {
			return err
		}
		if !result.OK {
			return errors.New("token revocation was not confirmed")
		}
		_, err = fmt.Fprintln(output, "SDK token revoked.")
		return err
	}
	var result struct {
		Token  string `json:"token"`
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	// Session access is the default; worker, spend-limit and model-default
	// authority are explicit opt-ins because they outlive a single session.
	scopes := []string{"sessions:read", "sessions:write"}
	if *workers {
		scopes = append(scopes, "automations:read", "automations:write")
	}
	if *usageLimits {
		scopes = append(scopes, "usage:write")
	}
	if *settings {
		scopes = append(scopes, "settings:write")
	}
	payload := map[string]any{"name": strings.TrimSpace(*name), "scopes": scopes, "expires_in_seconds": *seconds}
	if agentName := strings.TrimSpace(*agent); agentName != "" {
		// The daemon refuses unless the agent is sealed (only client tools).
		payload = map[string]any{"name": strings.TrimSpace(*name), "agent_name": agentName, "expires_in_seconds": *seconds}
	}
	if err := setupRequest(client, http.MethodPost, "/v3/auth/tokens", payload, &result); err != nil {
		return err
	}
	if !strings.HasPrefix(result.Token, "swk_") || result.Record.ID == "" {
		return errors.New("invalid token issuance response; inspect token records before retrying")
	}
	// Deliberately serialize only the credential and revocation ID, not arbitrary
	// server response fields. The receiver must protect this one-time export.
	return json.NewEncoder(output).Encode(map[string]string{"token": result.Token, "id": result.Record.ID})
}
