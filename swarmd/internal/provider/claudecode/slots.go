package claudecode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ConfigureSlots binds machine-local account routing to the daemon's state root.
// The existing CLI environment remains slot default; no credentials are moved.
func (c *CLI) ConfigureSlots(store *pebblestore.AuthStore, dataDir string) {
	c.slots = store
	c.slotsRoot = filepath.Join(dataDir, "providers", ProviderID, "accounts")
	c.slotClients = make(map[string]*CLI)
}

func (c *CLI) clientForSlot(slot pebblestore.ClaudeCodeSlot) *CLI {
	if slot.Legacy {
		return c
	}
	c.slotMu.Lock()
	defer c.slotMu.Unlock()
	if cli := c.slotClients[slot.ID]; cli != nil {
		return cli
	}
	dir := filepath.Join(c.slotsRoot, slot.ID)
	cli := &CLI{lookPath: c.lookPath, run: c.run, now: c.now, lookupEnv: func(key string) (string, bool) {
		if key == "CLAUDE_CONFIG_DIR" {
			return dir, true
		}
		return c.lookupEnv(key)
	}}
	c.slotClients[slot.ID] = cli
	return cli
}

type SlotStatus = provideriface.AccountStatus

func (c *CLI) AccountStatuses(ctx context.Context) ([]SlotStatus, error) {
	slots := []pebblestore.ClaudeCodeSlot{{ID: "default", Legacy: true}}
	if c.slots != nil {
		var err error
		slots, err = c.slots.ClaudeCodeSlots()
		if err != nil {
			return nil, err
		}
	}
	out := make([]SlotStatus, 0, len(slots))
	for i, slot := range slots {
		status := c.clientForSlot(slot).Status(ctx)
		item := SlotStatus{ID: slot.ID, Order: i + 1, Ready: status.Ready, Email: status.Account.EmailAddress, Organization: status.Account.OrganizationName, OrganizationUUID: status.Account.OrganizationUUID, Plan: status.Auth.SubscriptionType, LimitState: "ok", Reason: status.Reason}
		if slot.LimitUntil.After(c.now()) {
			until := slot.LimitUntil
			item.LimitState = "exhausted"
			item.LimitUntil = &until
		}
		out = append(out, item)
	}
	return out, nil
}

func LimitSummary(slots []SlotStatus) string {
	count := 0
	var earliest time.Time
	for _, slot := range slots {
		if slot.LimitUntil != nil {
			count++
			if earliest.IsZero() || slot.LimitUntil.Before(earliest) {
				earliest = *slot.LimitUntil
			}
		}
	}
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("claudecode: %d of %d accounts in a limit window, next free at %s UTC", count, len(slots), earliest.UTC().Format("15:04"))
}

// AddAccount creates only an empty private config directory and routing metadata.
// The person executes LoginCommand themselves; Swarm never performs login.
func (c *CLI) AddAccount() (id, loginCommand string, err error) {
	if c.slots == nil || c.slotsRoot == "" {
		return "", "", errors.New("Claude Code account slots are not configured")
	}
	path, err := c.ResolvePath()
	if err != nil {
		return "", "", err
	}
	current, err := user.Current()
	if err != nil {
		return "", "", err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", "", err
	}
	id = hex.EncodeToString(random[:])
	// Each component is daemon-owned, private, and must not be a symlink.
	base := filepath.Dir(filepath.Dir(filepath.Dir(c.slotsRoot)))
	confined, err := os.OpenRoot(base)
	if err != nil {
		return "", "", err
	}
	defer confined.Close()
	relative := ""
	for _, part := range []string{"providers", ProviderID, "accounts", id} {
		relative = filepath.Join(relative, part)
		if err = confined.Mkdir(relative, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", "", err
		}
		info, statErr := confined.Lstat(relative)
		if statErr != nil {
			return "", "", statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", "", errors.New("Claude Code account directory must be private and not a symlink")
		}
	}
	if err = c.slots.AddClaudeCodeSlot(id); err != nil {
		return "", "", err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	return id, "sudo -u " + quote(current.Username) + " -- env CLAUDE_CONFIG_DIR=" + quote(filepath.Join(base, relative)) + " " + quote(path) + " auth login", nil
}

func (c *CLI) OrderAccounts(ids []string) error {
	if c.slots == nil {
		return errors.New("Claude Code account slots are not configured")
	}
	return c.slots.OrderClaudeCodeSlots(ids)
}

// AccountLimitWait is returned only when every configured account has a known
// limit window. Orchestration waits outside its provider-activity watchdog.
type AccountLimitWait struct{ Until time.Time }

func (e *AccountLimitWait) Error() string {
	return "claudecode session limit: all accounts exhausted; resets at " + e.Until.UTC().Format("15:04") + " (UTC)"
}
func (e *AccountLimitWait) ProviderRetryAt() time.Time { return e.Until }
