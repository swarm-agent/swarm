package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// swarmctl fleet: link Swarms one way. On a machine, `invite` makes a
// one-time code (and listens for homes on the address given); on the home,
// `link CODE` redeems it. The home then uses the machine's own API as its
// owner; the machine gets nothing that reaches the home. `homes` and `drop`
// are the machine's view and control of who controls it; `list`, `check`,
// `unlink` and `call` are the home's view of its machines.

const fleetUsage = `usage:
  swarmctl fleet invite [ADDRESS[:PORT]] [--name NAME]   # on a machine: a code for its home
  swarmctl fleet link CODE [--name NAME]                 # on the home: link to that machine
  swarmctl fleet list                                    # machines, homes, open codes
  swarmctl fleet check NAME                              # does the machine answer its home
  swarmctl fleet unlink NAME                             # the home lets the machine go
  swarmctl fleet drop HOME                               # on a machine: end a home's control
  swarmctl fleet cancel CODE_ID                          # on a machine: cancel an unused code
  swarmctl fleet call NAME METHOD PATH [JSON]            # the machine's API, as its home`

func cmdFleet(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", fleetUsage)
	}
	if err := machineRoot(append([]string{"fleet"}, args...)); err != nil {
		return err
	}
	name := func(rest []string) (string, []string, error) {
		var flagName string
		var plain []string
		for i := 0; i < len(rest); i++ {
			switch {
			case rest[i] == "--name":
				if i+1 >= len(rest) {
					return "", nil, fmt.Errorf("--name needs a name")
				}
				i++
				flagName = rest[i]
			case strings.HasPrefix(rest[i], "--"):
				return "", nil, fmt.Errorf("unknown flag %s", rest[i])
			default:
				plain = append(plain, rest[i])
			}
		}
		return flagName, plain, nil
	}
	switch args[0] {
	case "invite":
		n, plain, err := name(args[1:])
		if err != nil || len(plain) > 1 {
			return fmt.Errorf("%s", fleetUsage)
		}
		address := ""
		if len(plain) == 1 {
			address = plain[0]
		}
		var out struct {
			Code   string `json:"code"`
			Invite struct {
				ID      string `json:"id"`
				Address string `json:"address"`
				Name    string `json:"name"`
			} `json:"invite"`
		}
		if err := fleetRequest(http.MethodPost, "/v3/fleet/invites", map[string]string{"address": address, "name": n}, &out); err != nil {
			return err
		}
		fmt.Printf("This Swarm (%s) listens for its home on %s.\nOn the home, run:\n\n  sudo swarmctl fleet link %s\n\nThe code works once. Cancel it with: sudo swarmctl fleet cancel %s\n", out.Invite.Name, out.Invite.Address, out.Code, out.Invite.ID)
		return nil
	case "link":
		n, plain, err := name(args[1:])
		if err != nil || len(plain) != 1 {
			return fmt.Errorf("%s", fleetUsage)
		}
		var out struct {
			Machine struct {
				Name    string `json:"name"`
				Address string `json:"address"`
			} `json:"machine"`
		}
		if err := fleetRequest(http.MethodPost, "/v3/fleet/machines", map[string]string{"code": plain[0], "name": n}, &out); err != nil {
			return err
		}
		fmt.Printf("Linked: this Swarm is the home of %s (%s).\n", out.Machine.Name, out.Machine.Address)
		return nil
	case "list":
		return fleetPrint(http.MethodGet, "/v3/fleet", nil)
	case "check":
		if len(args) != 2 {
			return fmt.Errorf("%s", fleetUsage)
		}
		return fleetPrint(http.MethodGet, "/v3/fleet/machines/"+args[1], nil)
	case "unlink":
		if len(args) != 2 {
			return fmt.Errorf("%s", fleetUsage)
		}
		return fleetPrint(http.MethodDelete, "/v3/fleet/machines/"+args[1], nil)
	case "cancel":
		if len(args) != 2 {
			return fmt.Errorf("%s", fleetUsage)
		}
		return fleetPrint(http.MethodDelete, "/v3/fleet/invites/"+args[1], nil)
	case "drop":
		if len(args) != 2 {
			return fmt.Errorf("%s", fleetUsage)
		}
		var list struct {
			Homes []struct {
				Fingerprint string `json:"fingerprint"`
				Name        string `json:"name"`
			} `json:"homes"`
		}
		if err := fleetRequest(http.MethodGet, "/v3/fleet", nil, &list); err != nil {
			return err
		}
		which := ""
		for _, h := range list.Homes {
			if h.Name == args[1] || h.Fingerprint == args[1] || (len(args[1]) >= 8 && strings.HasPrefix(h.Fingerprint, args[1])) {
				if which != "" && which != h.Fingerprint {
					return fmt.Errorf("%s names more than one home; give its key (swarmctl fleet list)", args[1])
				}
				which = h.Fingerprint
			}
		}
		if which == "" {
			return fmt.Errorf("no home %q (swarmctl fleet list)", args[1])
		}
		return fleetPrint(http.MethodDelete, "/v3/fleet/homes/"+which, nil)
	case "call":
		if len(args) < 4 || len(args) > 5 {
			return fmt.Errorf("%s", fleetUsage)
		}
		var body any
		if len(args) == 5 {
			body = json.RawMessage(args[4])
		}
		path := "/" + strings.TrimLeft(args[3], "/")
		return fleetPrint(strings.ToUpper(args[2]), "/v3/fleet/machines/"+args[1]+"/api"+path, body)
	default:
		return fmt.Errorf("%s", fleetUsage)
	}
}

// fleetSend calls the daemon over the owner socket and returns its answer,
// with Swarm's own reason when it refuses.
func fleetSend(method, path string, payload any) ([]byte, error) {
	c, err := newSetupClient(machineSocket)
	if err != nil {
		return nil, err
	}
	// Linking and calls reach another machine: no deadline of our own; the
	// network's and the person's ctrl-c are the limits.
	c.Timeout = 0
	defer c.CloseIdleConnections()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://swarm"+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var refusal struct {
			Error   any    `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &refusal)
		reason := refusal.Message
		if text, ok := refusal.Error.(string); ok && text != "" {
			reason = text
		} else if object, ok := refusal.Error.(map[string]any); ok {
			reason, _ = object["message"].(string)
		}
		if reason == "" {
			reason = strings.TrimSpace(string(data))
		}
		return data, fmt.Errorf("HTTP %d: %s", resp.StatusCode, reason)
	}
	return data, nil
}

func fleetRequest(method, path string, payload, out any) error {
	data, err := fleetSend(method, path, payload)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// fleetPrint prints the daemon's answer, indented when it is JSON.
func fleetPrint(method, path string, payload any) error {
	data, err := fleetSend(method, path, payload)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		data = pretty.Bytes()
	}
	_, err = os.Stdout.Write(append(bytes.TrimSpace(data), '\n'))
	return err
}
