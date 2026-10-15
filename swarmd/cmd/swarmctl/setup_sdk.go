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
	messagesPerMinute := fs.Int("messages-per-minute", 0, "with --agent: most messages (model runs) the token may start per minute (default 120)")
	sessionsPerHour := fs.Int("sessions-per-hour", 0, "with --agent: most new conversations per hour (default 300)")
	maxConcurrentRuns := fs.Int("max-concurrent-runs", 0, "with --agent: most model runs the agent may have going at once across its conversations (default 4, at most 64)")
	operator := fs.Bool("operator", false, "with --agent: mint the human-takeover key instead (read the agent's conversations, take one over, reply as a person, hand it back; starts no runs)")
	aiAccess := fs.String("ai-access", "", "mint an AI key for Swarm Control (/mcp): read (default level for AI clients), write, or full (the AI runs this box: approvals, models, workers, custom agents, client keys); lifetime up to 365 days")
	monitor := fs.Bool("monitor", false, "mint a read-only monitoring key: the machine summary and signal feed (signals:read) and nothing else; lifetime up to 365 days")
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
	if *operator && strings.TrimSpace(*agent) == "" {
		return errors.New("--operator needs --agent")
	}
	if strings.TrimSpace(*agent) != "" {
		if *workers || *usageLimits || *settings {
			return errors.New("--agent tokens take no other authority")
		}
		maxSeconds = 30 * 86400
	}
	if *monitor {
		if *workers || *usageLimits || *settings || strings.TrimSpace(*agent) != "" || strings.TrimSpace(*aiAccess) != "" {
			return errors.New("--monitor keys take no other authority")
		}
		maxSeconds = 365 * 86400
	}
	if level := strings.TrimSpace(*aiAccess); level != "" {
		if *workers || *usageLimits || *settings || strings.TrimSpace(*agent) != "" {
			return errors.New("--ai-access keys take no other authority")
		}
		if level != "read" && level != "write" && level != "full" {
			return errors.New("--ai-access must be read, write or full")
		}
		maxSeconds = 365 * 86400
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
	if *monitor {
		// A monitor reads how the machine is doing; it starts and sees nothing.
		scopes = []string{"signals:read"}
	}
	payload := map[string]any{"name": strings.TrimSpace(*name), "scopes": scopes, "expires_in_seconds": *seconds}
	if *monitor {
		if *workers || *usageLimits || *settings || strings.TrimSpace(*agent) != "" || strings.TrimSpace(*aiAccess) != "" {
			return errors.New("--monitor keys take no other authority")
		}
		maxSeconds = 365 * 86400
	}
	if level := strings.TrimSpace(*aiAccess); level != "" {
		// The daemon derives scopes and Swarm Control levels from the level.
		payload = map[string]any{"name": strings.TrimSpace(*name), "ai_access": level, "expires_in_seconds": *seconds}
	} else if agentName := strings.TrimSpace(*agent); agentName != "" {
		// The daemon refuses unless the agent is sealed (only client tools).
		payload = map[string]any{"name": strings.TrimSpace(*name), "agent_name": agentName, "expires_in_seconds": *seconds,
			"messages_per_minute": *messagesPerMinute, "sessions_per_hour": *sessionsPerHour, "max_concurrent_runs": *maxConcurrentRuns, "operator": *operator}
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

// runSetupSDKTokens lists the account's SDK tokens so one can be revoked by ID.
// It prints identity, kind, limits and dates only: never the token, its hash or
// its hint.
func runSetupSDKTokens(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid sdk-tokens flags; use --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected sdk-tokens arguments")
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var result struct {
		Tokens []struct {
			ID                string   `json:"id"`
			Name              string   `json:"name"`
			Scopes            []string `json:"scopes"`
			AgentName         string   `json:"agent_name"`
			AgentOperator     bool     `json:"agent_operator"`
			MessagesPerMinute int      `json:"messages_per_minute"`
			SessionsPerHour   int      `json:"sessions_per_hour"`
			MaxConcurrentRuns int      `json:"max_concurrent_runs"`
			CreatedAt         int64    `json:"created_at"`
			ExpiresAt         int64    `json:"expires_at"`
			LastUsedAt        int64    `json:"last_used_at"`
			Revoked           bool     `json:"revoked"`
		} `json:"tokens"`
	}
	if err := setupRequest(client, http.MethodGet, "/v3/auth/tokens", nil, &result); err != nil {
		return err
	}
	type listed struct {
		ID                string   `json:"id"`
		Name              string   `json:"name"`
		Kind              string   `json:"kind"`
		Scopes            []string `json:"scopes,omitempty"`
		MessagesPerMinute int      `json:"messages_per_minute,omitempty"`
		SessionsPerHour   int      `json:"sessions_per_hour,omitempty"`
		MaxConcurrentRuns int      `json:"max_concurrent_runs,omitempty"`
		CreatedAt         int64    `json:"created_at"`
		ExpiresAt         int64    `json:"expires_at"`
		LastUsedAt        int64    `json:"last_used_at,omitempty"`
		Revoked           bool     `json:"revoked"`
	}
	tokens := make([]listed, 0, len(result.Tokens))
	for _, token := range result.Tokens {
		item := listed{ID: token.ID, Name: token.Name, Kind: "scoped", Scopes: token.Scopes, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt, Revoked: token.Revoked}
		switch {
		case token.AgentName != "" && token.AgentOperator:
			item.Kind, item.Scopes = "operator:"+token.AgentName, nil
		case token.AgentName != "":
			item.Kind, item.Scopes = "gateway:"+token.AgentName, nil
			item.MessagesPerMinute, item.SessionsPerHour, item.MaxConcurrentRuns = token.MessagesPerMinute, token.SessionsPerHour, token.MaxConcurrentRuns
		}
		tokens = append(tokens, item)
	}
	return json.NewEncoder(output).Encode(map[string]any{"tokens": tokens})
}
