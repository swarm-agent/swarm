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
	"strconv"
	"strings"
)

// maxFixedDateSeconds is the furthest fixed end date a key may have (the
// daemon's security.KeyFixedDateMax). Keys need none: they work while used and
// stop after 90 days unused.
const maxFixedDateSeconds = 365 * 86400

func runSetupSDKToken(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	name := fs.String("name", "headless-sdk", "token name")
	seconds := fs.Int64("expires-in-seconds", 0, "optional fixed end date, 60 to 31536000 seconds from now; without it the key works while it is used and stops after 90 days unused")
	id := fs.String("id", "", "token record ID to revoke")
	workers := fs.Bool("workers", false, "also allow creating, managing and tasking workers (Swarm Control worker tools)")
	usageLimits := fs.Bool("usage-limits", false, "also allow changing the account's daily usage limits")
	settings := fs.Bool("settings", false, "also allow changing agent role default models")
	agent := fs.String("agent", "", "mint a bot key: a gateway token limited to this sealed agent's sessions and client tool calls")
	messagesPerMinute := fs.Int("messages-per-minute", 0, "with --agent: most messages (model runs) the token may start per minute (default 120)")
	sessionsPerHour := fs.Int("sessions-per-hour", 0, "with --agent: most new conversations per hour (default 300)")
	maxConcurrentRuns := fs.Int("max-concurrent-runs", 0, "with --agent: most model runs the agent may have going at once across its conversations (default 4, at most 64)")
	endUser := endUserLimitFlags(fs)
	operator := fs.Bool("operator", false, "with --agent: mint the human-takeover key instead (read the agent's conversations, take one over, reply as a person, hand it back; starts no runs)")
	aiAccess := fs.String("ai-access", "", "mint an AI key for Swarm Control (/mcp): read (default level for AI clients), write, or full (the AI runs this box: approvals, models, workers, custom agents, client keys)")
	monitor := fs.Bool("monitor", false, "mint a read-only monitoring key: the machine summary and signal feed (signals:read) and nothing else")
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
	if *operator && strings.TrimSpace(*agent) == "" {
		return errors.New("--operator needs --agent")
	}
	if strings.TrimSpace(*agent) != "" {
		if *workers || *usageLimits || *settings {
			return errors.New("--agent tokens take no other authority")
		}
	}
	if endUser.set() && (strings.TrimSpace(*agent) == "" || *operator) {
		return errors.New("--end-user-* limits need --agent (a gateway token)")
	}
	if *monitor {
		if *workers || *usageLimits || *settings || strings.TrimSpace(*agent) != "" || strings.TrimSpace(*aiAccess) != "" {
			return errors.New("--monitor keys take no other authority")
		}
	}
	if level := strings.TrimSpace(*aiAccess); level != "" {
		if *workers || *usageLimits || *settings || strings.TrimSpace(*agent) != "" {
			return errors.New("--ai-access keys take no other authority")
		}
		if level != "read" && level != "write" && level != "full" {
			return errors.New("--ai-access must be read, write or full")
		}
	}
	if !revoke {
		if strings.TrimSpace(*name) == "" {
			return errors.New("--name is required")
		}
		if *seconds != 0 && (*seconds < 60 || *seconds > maxFixedDateSeconds) {
			return fmt.Errorf("--expires-in-seconds is a fixed end date of 60-%d seconds, or left out (the key then works until 90 days unused)", maxFixedDateSeconds)
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
	}
	if level := strings.TrimSpace(*aiAccess); level != "" {
		// The daemon derives scopes and Swarm Control levels from the level.
		payload = map[string]any{"name": strings.TrimSpace(*name), "ai_access": level, "expires_in_seconds": *seconds}
	} else if agentName := strings.TrimSpace(*agent); agentName != "" {
		// The daemon refuses unless the agent is sealed (only client tools).
		payload = map[string]any{"name": strings.TrimSpace(*name), "agent_name": agentName, "expires_in_seconds": *seconds,
			"messages_per_minute": *messagesPerMinute, "sessions_per_hour": *sessionsPerHour, "max_concurrent_runs": *maxConcurrentRuns, "operator": *operator,
			"end_user_messages_per_minute": *endUser.messagesPerMinute, "end_user_sessions_per_hour": *endUser.sessionsPerHour, "end_user_max_tokens_per_day": *endUser.maxTokensPerDay}
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

// endUserFlags are a gateway token's per-end-user limits (sessions created
// with an end_user); zero leaves the daemon default.
type endUserFlags struct {
	messagesPerMinute, sessionsPerHour *int
	maxTokensPerDay                    *int64
}

func endUserLimitFlags(fs *flag.FlagSet) endUserFlags {
	return endUserFlags{
		messagesPerMinute: fs.Int("end-user-messages-per-minute", 0, "with --agent: most messages one end user's conversations may send per minute (default 20)"),
		sessionsPerHour:   fs.Int("end-user-sessions-per-hour", 0, "with --agent: most new conversations per end user per hour (default 30)"),
		maxTokensPerDay:   fs.Int64("end-user-max-tokens-per-day", 0, "with --agent: model tokens one end user may use per UTC day (default no cap)"),
	}
}

func (f endUserFlags) set() bool {
	return *f.messagesPerMinute != 0 || *f.sessionsPerHour != 0 || *f.maxTokensPerDay != 0
}

// runSetupSDKTokenLimits changes a gateway token's limits in place (the owner
// edits them; the token keeps working). Only the flags given change.
func runSetupSDKTokenLimits(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	id := fs.String("id", "", "gateway token record ID (see sdk-tokens)")
	fs.Int("messages-per-minute", 0, "messages (model runs) the token may start per minute")
	fs.Int("sessions-per-hour", 0, "new conversations per hour")
	fs.Int("max-concurrent-runs", 0, "model runs at once (1-64)")
	endUserLimitFlags(fs)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid sdk-token-limits flags; use --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected sdk-token-limits arguments")
	}
	if strings.TrimSpace(*id) == "" {
		return errors.New("--id is required")
	}
	fields := map[string]string{"messages-per-minute": "messages_per_minute", "sessions-per-hour": "sessions_per_hour", "max-concurrent-runs": "max_concurrent_runs",
		"end-user-messages-per-minute": "end_user_messages_per_minute", "end-user-sessions-per-hour": "end_user_sessions_per_hour", "end-user-max-tokens-per-day": "end_user_max_tokens_per_day"}
	payload := map[string]any{}
	var parseErr error
	fs.Visit(func(f *flag.Flag) {
		field, ok := fields[f.Name]
		if !ok {
			return
		}
		value, err := strconv.ParseInt(f.Value.String(), 10, 64)
		if err != nil || value < 0 {
			parseErr = fmt.Errorf("--%s must be a non-negative number (0 restores the default)", f.Name)
			return
		}
		payload[field] = value
	})
	if parseErr != nil {
		return parseErr
	}
	if len(payload) == 0 {
		return errors.New("give at least one limit to change")
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var result struct {
		OK     bool `json:"ok"`
		Record struct {
			ID                       string `json:"id"`
			AgentName                string `json:"agent_name"`
			MessagesPerMinute        int    `json:"messages_per_minute"`
			SessionsPerHour          int    `json:"sessions_per_hour"`
			MaxConcurrentRuns        int    `json:"max_concurrent_runs"`
			EndUserMessagesPerMinute int    `json:"end_user_messages_per_minute"`
			EndUserSessionsPerHour   int    `json:"end_user_sessions_per_hour"`
			EndUserMaxTokensPerDay   int64  `json:"end_user_max_tokens_per_day"`
		} `json:"record"`
	}
	if err := setupRequest(client, http.MethodPost, "/v3/auth/tokens/"+url.PathEscape(strings.TrimSpace(*id))+"/limits", payload, &result); err != nil {
		return err
	}
	if !result.OK || result.Record.ID == "" {
		return errors.New("limit change was not confirmed")
	}
	// The record's identity and limits only: never its hash or hint.
	return json.NewEncoder(output).Encode(result.Record)
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
			ID                       string   `json:"id"`
			Name                     string   `json:"name"`
			Scopes                   []string `json:"scopes"`
			AgentName                string   `json:"agent_name"`
			AgentOperator            bool     `json:"agent_operator"`
			MessagesPerMinute        int      `json:"messages_per_minute"`
			SessionsPerHour          int      `json:"sessions_per_hour"`
			MaxConcurrentRuns        int      `json:"max_concurrent_runs"`
			EndUserMessagesPerMinute int      `json:"end_user_messages_per_minute"`
			EndUserSessionsPerHour   int      `json:"end_user_sessions_per_hour"`
			EndUserMaxTokensPerDay   int64    `json:"end_user_max_tokens_per_day"`
			CreatedAt                int64    `json:"created_at"`
			ExpiresAt                int64    `json:"expires_at"`
			LastUsedAt               int64    `json:"last_used_at"`
			Revoked                  bool     `json:"revoked"`
			NeverExpires             bool     `json:"never_expires"`
			KeyKind                  string   `json:"key_kind"`
			State                    string   `json:"state"`
			LapsesAt                 int64    `json:"lapses_at"`
			LapseReason              string   `json:"lapse_reason"`
			LastUsedFrom             string   `json:"last_used_from"`
		} `json:"tokens"`
	}
	if err := setupRequest(client, http.MethodGet, "/v3/auth/tokens", nil, &result); err != nil {
		return err
	}
	type listed struct {
		ID                       string   `json:"id"`
		Name                     string   `json:"name"`
		Kind                     string   `json:"kind"`
		Scopes                   []string `json:"scopes,omitempty"`
		MessagesPerMinute        int      `json:"messages_per_minute,omitempty"`
		SessionsPerHour          int      `json:"sessions_per_hour,omitempty"`
		MaxConcurrentRuns        int      `json:"max_concurrent_runs,omitempty"`
		EndUserMessagesPerMinute int      `json:"end_user_messages_per_minute,omitempty"`
		EndUserSessionsPerHour   int      `json:"end_user_sessions_per_hour,omitempty"`
		EndUserMaxTokensPerDay   int64    `json:"end_user_max_tokens_per_day,omitempty"`
		CreatedAt                int64    `json:"created_at"`
		ExpiresAt                int64    `json:"expires_at"`
		LastUsedAt               int64    `json:"last_used_at,omitempty"`
		Revoked                  bool     `json:"revoked"`
		// KeyKind is ai, app, bot or internal; State is active, expired or
		// revoked; LapsesAt is when the key stops on its own (LapseReason
		// "unused": 90 days after its last use; "date": its fixed end date).
		KeyKind      string `json:"key_kind,omitempty"`
		State        string `json:"state,omitempty"`
		LapsesAt     int64  `json:"lapses_at,omitempty"`
		LapseReason  string `json:"lapse_reason,omitempty"`
		LastUsedFrom string `json:"last_used_from,omitempty"`
		// NeverExpires marks Swarm's own internal keys, which last as long
		// as the setup that made them.
		NeverExpires bool `json:"never_expires,omitempty"`
	}
	tokens := make([]listed, 0, len(result.Tokens))
	for _, token := range result.Tokens {
		item := listed{ID: token.ID, Name: token.Name, Kind: "scoped", Scopes: token.Scopes, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt, Revoked: token.Revoked,
			NeverExpires: token.NeverExpires, KeyKind: token.KeyKind, State: token.State, LapsesAt: token.LapsesAt, LapseReason: token.LapseReason, LastUsedFrom: token.LastUsedFrom}
		switch {
		case token.AgentName != "" && token.AgentOperator:
			item.Kind, item.Scopes = "operator:"+token.AgentName, nil
		case token.AgentName != "":
			item.Kind, item.Scopes = "gateway:"+token.AgentName, nil
			item.MessagesPerMinute, item.SessionsPerHour, item.MaxConcurrentRuns = token.MessagesPerMinute, token.SessionsPerHour, token.MaxConcurrentRuns
			item.EndUserMessagesPerMinute, item.EndUserSessionsPerHour, item.EndUserMaxTokensPerDay = token.EndUserMessagesPerMinute, token.EndUserSessionsPerHour, token.EndUserMaxTokensPerDay
		}
		tokens = append(tokens, item)
	}
	return json.NewEncoder(output).Encode(map[string]any{"tokens": tokens})
}
