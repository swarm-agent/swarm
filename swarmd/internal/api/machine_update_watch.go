package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/pkg/buildinfo"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// ReadMachineUpdate is what the Orchestrator's machine watch (manage_projects
// wait_machine) reads: a machine's running commit, the installer's update
// state and the update drain, from the same machine object the machine
// summary (GET /v3/box/summary, machine_status) carries. Here it is built
// from this daemon's own state; on a linked machine it is that machine's
// summary read over the link, after the linked work rule check. What a
// linked machine says is untrusted (tool.ParseMachineUpdateView clips it).
func (s *Server) ReadMachineUpdate(ctx context.Context, p identity.Principal, machine string) (tool.MachineUpdateView, error) {
	if s == nil {
		return tool.MachineUpdateView{}, errors.New("machine update service unavailable")
	}
	if !p.Valid() {
		return tool.MachineUpdateView{}, identity.ErrPrincipalRequired
	}
	machine = strings.TrimSpace(machine)
	if machine == "" {
		return tool.ParseMachineUpdateView(s.localMachineUpdateObject(p.AccountScopeID))
	}
	status, err := s.ReadFleetMachineStatusView(ctx, p, machine)
	if err != nil {
		return tool.MachineUpdateView{}, err
	}
	raw, ok := status["machine"].(json.RawMessage)
	if !ok {
		return tool.MachineUpdateView{}, fmt.Errorf("%s returned a status without its machine object", machine)
	}
	return tool.ParseMachineUpdateView(raw)
}

// localMachineUpdateObject is this machine's machine object as its summary
// shows it to account (commit, update, drain).
func (s *Server) localMachineUpdateObject(account string) json.RawMessage {
	machine := map[string]any{"commit": strings.TrimSpace(buildinfo.Commit)}
	if update := boxUpdateState(); update != nil {
		machine["update"] = update
	}
	if manager := s.ExecutionCapacity(); manager != nil {
		machine["drain"] = boxDrainSummary(manager.DrainStatus(), account)
	}
	raw, _ := json.Marshal(machine)
	return raw
}

var _ tool.MachineUpdateReader = (*Server)(nil)
