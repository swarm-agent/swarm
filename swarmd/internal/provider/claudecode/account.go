package claudecode

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// globalConfigName is Claude Code's global config file: in CLAUDE_CONFIG_DIR
// when that is set, otherwise in HOME. Its oauthAccount names the signed-in
// Claude account; the credentials themselves live elsewhere.
const globalConfigName = ".claude.json"

// maxGlobalConfigBytes bounds the read: the file also holds per-project
// history and can grow large. A larger file reports no account.
const maxGlobalConfigBytes = 32 << 20

// Account is who the daemon user's Claude Code is signed in as: identity
// only, read from the oauthAccount of Claude Code's global config. It never
// holds a token, key or any other credential; a missing field stays empty.
type Account struct {
	EmailAddress     string `json:"emailAddress,omitempty"`
	OrganizationName string `json:"organizationName,omitempty"`
	OrganizationUUID string `json:"organizationUuid,omitempty"`
	AccountUUID      string `json:"accountUuid,omitempty"`
}

// globalConfigPath is the global config the CLI reads under the same
// environment Swarm runs it with (Env), or "" when neither directory is set.
func (c *CLI) globalConfigPath() string {
	if dir, ok := c.lookupEnv("CLAUDE_CONFIG_DIR"); ok && strings.TrimSpace(dir) != "" {
		return filepath.Join(strings.TrimSpace(dir), globalConfigName)
	}
	if home, ok := c.lookupEnv("HOME"); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(strings.TrimSpace(home), globalConfigName)
	}
	return ""
}

// readAccount returns the signed-in account's identity fields, or an empty
// Account when the config is absent, unreadable or names no account.
func (c *CLI) readAccount() Account {
	path := c.globalConfigPath()
	if path == "" {
		return Account{}
	}
	file, err := os.Open(path)
	if err != nil {
		return Account{}
	}
	defer file.Close()
	return parseAccount(io.LimitReader(file, maxGlobalConfigBytes))
}

// parseAccount decodes only oauthAccount's identity fields; every other field
// of the config (tokens included) is discarded by the decoder.
func parseAccount(r io.Reader) Account {
	var config struct {
		OAuthAccount *Account `json:"oauthAccount"`
	}
	if err := json.NewDecoder(r).Decode(&config); err != nil || config.OAuthAccount == nil {
		return Account{}
	}
	return Account{
		EmailAddress:     strings.TrimSpace(config.OAuthAccount.EmailAddress),
		OrganizationName: strings.TrimSpace(config.OAuthAccount.OrganizationName),
		OrganizationUUID: strings.TrimSpace(config.OAuthAccount.OrganizationUUID),
		AccountUUID:      strings.TrimSpace(config.OAuthAccount.AccountUUID),
	}
}
