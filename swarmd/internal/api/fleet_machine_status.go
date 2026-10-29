package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// A linked machine's status, read over the link from the machine's own
// GET /v3/box/summary: the same data MCP swarm_status gives a client holding
// that machine's key. The home keeps none of it; what the machine says is
// untrusted.

const fleetMachineStatusTimeout = 10 * time.Second

// fleetMachineStatusKeys are the parts of the box summary swarm_status shows.
var fleetMachineStatusKeys = []string{"status", "reasons", "machine", "sandbox", "secrets_gateway", "permissions", "attention", "workers", "runners", "usage", "keys", "running_now"}

// ReadFleetMachineStatus reads machine's status through the link. The
// caller has already checked that its linked work rule is not off.
func (s *Server) ReadFleetMachineStatus(ctx context.Context, p identity.Principal, machine string) (map[string]any, error) {
	if s == nil || s.fleet == nil || s.fleetTokens == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "fleet service unavailable")
	}
	if !p.Valid() {
		return nil, linkedTaskFail(http.StatusUnauthorized, "%s", identity.ErrPrincipalRequired.Error())
	}
	client, linked, status, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, linkedTaskFail(status, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, fleetMachineStatusTimeout)
	defer cancel()
	var summary map[string]json.RawMessage
	err = client.JSON(ctx, http.MethodGet, "/v3/box/summary", nil, &summary)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, linkedFleetCallError(machine, linked, "the status read", err)
	}
	out := map[string]any{"machine_name": machine, "untrusted_content": true}
	for _, key := range fleetMachineStatusKeys {
		if raw, ok := summary[key]; ok && len(raw) > 0 && len(raw) <= 256<<10 {
			out[key] = raw
		}
	}
	return out, nil
}

// fleetMachineStatusLine is a one-line view of a machine's status for
// list_machines: what runs, whether it drains for an update, the update
// state and how many sessions wait on a person.
func fleetMachineStatusLine(status map[string]any) *tool.FleetMachineStatusLine {
	var view struct {
		Status     string `json:"status"`
		RunningNow int    `json:"running_now"`
		Machine    struct {
			Drain *struct {
				Draining bool  `json:"draining"`
				SinceMS  int64 `json:"since_ms"`
				Deadline int64 `json:"deadline_ms"`
			} `json:"drain"`
			Update *struct {
				State  string `json:"state"`
				Reason string `json:"reason"`
			} `json:"update"`
		} `json:"machine"`
		Attention struct {
			Blocked int `json:"blocked_sessions"`
		} `json:"attention"`
	}
	raw, err := json.Marshal(status)
	if err != nil || json.Unmarshal(raw, &view) != nil {
		return nil
	}
	line := &tool.FleetMachineStatusLine{Status: clipFleetTaskText(view.Status, 32), Running: max(view.RunningNow, 0), Blocked: max(view.Attention.Blocked, 0)}
	parts := []string{fmt.Sprintf("running %d", line.Running)}
	if d := view.Machine.Drain; d != nil && d.Draining {
		line.Draining = true
		if d.SinceMS > 0 {
			line.DrainingSince = time.UnixMilli(d.SinceMS).UTC().Format(time.RFC3339)
			parts = append(parts, "draining since "+line.DrainingSince)
		} else {
			parts = append(parts, "draining")
		}
	} else {
		parts = append(parts, "not draining")
	}
	if u := view.Machine.Update; u != nil && strings.TrimSpace(u.State) != "" {
		line.Update = clipFleetTaskText(u.State, 32)
		parts = append(parts, "update "+line.Update)
	}
	parts = append(parts, fmt.Sprintf("%d waiting on a person", line.Blocked))
	line.Summary = strings.Join(parts, ", ")
	return line
}

// FleetMachineStatusLine reads machine's status and gives its one line.
func (s *Server) FleetMachineStatusLine(ctx context.Context, p identity.Principal, machine string) (*tool.FleetMachineStatusLine, error) {
	status, err := s.ReadFleetMachineStatus(ctx, p, machine)
	if err != nil {
		return nil, err
	}
	line := fleetMachineStatusLine(status)
	if line == nil {
		return nil, fmt.Errorf("%s returned an unreadable status", machine)
	}
	return line, nil
}

var _ tool.FleetMachineStatusReader = (*Server)(nil)
