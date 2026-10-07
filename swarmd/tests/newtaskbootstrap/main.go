// Command newtaskbootstrap is an offline, test-only fixture initializer, never a
// production setup command. It uses BootstrapFirstIdentity and PutForAccount on
// an exclusively new run-owned database; it imports no credentials or auth state.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type input struct {
	Swarm        pebblestore.SwarmAgentModelAssignments  `json:"swarm"`
	SystemAgents pebblestore.SystemAgentModelAssignments `json:"system_agents"`
}

func decode(r io.Reader) (input, error) {
	var value input
	payload, err := io.ReadAll(io.LimitReader(r, 8193))
	if err != nil || len(payload) > 8192 {
		return value, errors.New("settings_size")
	}
	d := json.NewDecoder(strings.NewReader(string(payload)))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return value, errors.New("settings_json")
	}
	if d.Decode(new(any)) != io.EOF {
		return value, errors.New("settings_trailing")
	}
	for _, a := range []pebblestore.AgentModelAssignment{value.Swarm.Action, value.Swarm.Plan, value.SystemAgents.Compact, value.SystemAgents.Finder, value.SystemAgents.Coder, value.SystemAgents.Designer, value.SystemAgents.Router} {
		if err := pebblestore.ValidateAgentModelAssignment(a); err != nil {
			return value, errors.New("settings_assignment")
		}
		for _, field := range []string{a.Provider, a.Model, a.Thinking, a.ServiceTier, a.ContextMode} {
			if len(field) > 200 || strings.ContainsAny(field, "\r\n\x00") {
				return value, errors.New("settings_assignment")
			}
		}
	}
	return value, nil
}

func bootstrap(root, owner, tmp string, r io.Reader) error {
	value, err := decode(r) // All input validation precedes durable mutation.
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^new-task-smoke-[a-f0-9-]{36}$`).MatchString(owner) {
		return errors.New("owner_invalid")
	}
	root = filepath.Clean(root)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) || canonical != root {
		return errors.New("root_invalid")
	}
	tmpCanonical, err := filepath.EvalSymlinks(tmp)
	if err != nil || !filepath.IsAbs(tmp) {
		return errors.New("scratch_invalid")
	}
	rel, err := filepath.Rel(tmpCanonical, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !strings.HasPrefix(filepath.Base(root), "new-task-daemon-") {
		return errors.New("root_unowned")
	}
	marker := filepath.Join(root, "owner")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 100 {
		return errors.New("owner_missing")
	}
	bytes, err := os.ReadFile(marker)
	if err != nil || string(bytes) != owner {
		return errors.New("owner_mismatch")
	}
	db := filepath.Join(root, "db")
	// Exclusive mkdir rejects *all* existing state, including a symlink or empty
	// database. Pebble's own exclusive lock then protects the offline write.
	if err := os.Mkdir(db, 0700); err != nil {
		return errors.New("db_exists")
	}
	store, err := pebblestore.Open(db)
	if err != nil {
		return errors.New("db_open")
	}
	created, err := identity.NewService(pebblestore.NewIdentityStore(store)).BootstrapFirstIdentity(owner)
	if err == nil {
		_, err = pebblestore.NewAgentModelSettingsStore(store).PutForAccount(pebblestore.AgentModelSettingsRecord{
			AccountScopeID: created.AccountScope.ID, Swarm: value.Swarm, SystemAgents: value.SystemAgents,
		})
	}
	closeErr := store.Close()
	if err != nil {
		return errors.New("canonical_bootstrap")
	}
	if closeErr != nil {
		return errors.New("db_close")
	}
	return nil
}

func main() {
	flags := flag.NewFlagSet("newtaskbootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("state-root", "", "exclusive run-owned scratch root")
	owner := flags.String("owner", "", "exact run owner")
	err := flags.Parse(os.Args[1:])
	if err == nil && flags.NArg() != 0 {
		err = errors.New("arguments")
	}
	if err == nil {
		err = bootstrap(*root, *owner, os.Getenv("TMPDIR"), os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "SMOKE_BOOTSTRAP_FAILED")
		os.Exit(1)
	}
	fmt.Println("SMOKE_BOOTSTRAP_OK")
}
