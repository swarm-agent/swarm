package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// swarmctl client: owner sessions for a client app the person runs (their
// console). `connect` mints one over the owner socket, as root, and leaves
// the token in the person's own config, readable by them alone; the app
// sends it as X-Swarm-Token to the loopback API (or through an SSH tunnel to
// it from their PC). `list` and `revoke` are the person's view and control
// of what was handed out.

const clientTokenDirName = "clients"

func cmdClient(args []string) error {
	usage := fmt.Errorf("usage: swarmctl client connect NAME [--for-user USER] [--print] | list | revoke SESSION_ID|NAME")
	if len(args) < 1 {
		return usage
	}
	switch args[0] {
	case "connect":
		return clientConnect(args[1:])
	case "list":
		return clientList()
	case "revoke":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return usage
		}
		return clientRevoke(args[1])
	default:
		return usage
	}
}

type clientSessionRow struct {
	SessionID  string `json:"session_id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	RevokedAt  int64  `json:"revoked_at"`
}

func clientConnect(args []string) error {
	name, who, print := "", "", false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--for-user":
			if i+1 >= len(args) {
				return fmt.Errorf("--for-user needs a user name")
			}
			i++
			who = args[i]
		case a == "--print":
			print = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s", a)
		case name == "":
			name = a
		default:
			return fmt.Errorf("one name only")
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(name) {
		return fmt.Errorf("the client's name is letters, digits, dot, dash or underscore (1-64)")
	}
	if err := machineRoot(append([]string{"client", "connect"}, args...)); err != nil {
		return err
	}
	var person *user.User
	if !print {
		var err error
		if person, err = clientPerson(who); err != nil {
			return err
		}
	}
	var minted struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := machineRequest(http.MethodPost, "/v1/auth/clients", map[string]string{"name": name}, &minted); err != nil {
		return err
	}
	if minted.Token == "" {
		return fmt.Errorf("the daemon returned no token")
	}
	if print {
		// For a client on another machine, which reads this over SSH and
		// keeps the token itself. Nothing is written here.
		fmt.Println(minted.Token)
		return nil
	}
	path, err := clientTokenWrite(person, name, minted.Token)
	if err != nil {
		return err
	}
	fmt.Printf("%s is connected as %s (session %s, until %s).\nToken: %s\n", name, person.Username, minted.SessionID, time.UnixMilli(minted.ExpiresAt).Format("2006-01-02"), path)
	return nil
}

// clientPerson is the person the token is for: --for-user, else whoever ran
// sudo or pkexec. Never root, never the daemon's user.
func clientPerson(who string) (*user.User, error) {
	who = strings.TrimSpace(who)
	if who == "" {
		who = machineInvokingUser()
	}
	if who == "" {
		return nil, fmt.Errorf("cannot tell who this is for: run with sudo (or pkexec) from your own account, or pass --for-user NAME")
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`).MatchString(who) {
		return nil, fmt.Errorf("--for-user must be a user name")
	}
	person, err := user.Lookup(who)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.Atoi(person.Uid)
	if err != nil || uid < 1000 || uid == 65534 || who == "swarm" {
		return nil, fmt.Errorf("%s is not a person's account; pass --for-user NAME", who)
	}
	if home, err := os.Stat(person.HomeDir); err != nil || !filepath.IsAbs(person.HomeDir) || !home.IsDir() {
		return nil, fmt.Errorf("%s has no home folder", who)
	}
	return person, nil
}

// clientTokenWrite puts the token at ~/.config/swarm/clients/NAME.token,
// owned by the person, readable by them alone.
func clientTokenWrite(person *user.User, name, token string) (string, error) {
	uid, _ := strconv.Atoi(person.Uid)
	gid, _ := strconv.Atoi(person.Gid)
	dir := filepath.Join(person.HomeDir, ".config", "swarm", clientTokenDirName)
	for _, d := range []string{filepath.Join(person.HomeDir, ".config"), filepath.Join(person.HomeDir, ".config", "swarm"), dir} {
		if err := os.Mkdir(d, 0o700); err == nil {
			if err := os.Chown(d, uid, gid); err != nil {
				return "", err
			}
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	path := filepath.Join(dir, name+".token")
	f, err := os.CreateTemp(dir, ".swarmctl-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err = f.Chown(uid, gid); err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		_, err = f.WriteString(token + "\n")
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func clientList() error {
	if err := machineRoot([]string{"client", "list"}); err != nil {
		return err
	}
	var out struct {
		Clients []clientSessionRow `json:"clients"`
	}
	if err := machineRequest(http.MethodGet, "/v1/auth/clients", nil, &out); err != nil {
		return err
	}
	if len(out.Clients) == 0 {
		fmt.Println("No client sessions.")
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out.Clients)
}

func clientRevoke(which string) error {
	if err := machineRoot([]string{"client", "revoke", which}); err != nil {
		return err
	}
	var out struct {
		Clients []clientSessionRow `json:"clients"`
	}
	if err := machineRequest(http.MethodGet, "/v1/auth/clients", nil, &out); err != nil {
		return err
	}
	id := ""
	for _, c := range out.Clients {
		if c.SessionID == which || (c.Name == which && c.RevokedAt == 0) {
			if id != "" && c.SessionID != id {
				return fmt.Errorf("%s names more than one session; give the session id (swarmctl client list)", which)
			}
			id = c.SessionID
		}
	}
	if id == "" {
		return fmt.Errorf("no client session %q (swarmctl client list)", which)
	}
	if err := machineRequest(http.MethodDelete, "/v1/auth/clients/"+id, nil, nil); err != nil {
		return err
	}
	fmt.Printf("Revoked %s.\n", id)
	return nil
}
