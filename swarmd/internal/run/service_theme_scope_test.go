package run

import (
	"fmt"

	"swarm/packages/swarmd/internal/identity"

	"swarm/packages/swarmd/internal/uisettings"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

type themeDispatchSettings struct{ saves int }

func (s *themeDispatchSettings) Get() (uisettings.UISettings, error) {
	return uisettings.UISettings{Theme: uisettings.ThemeSettings{ActiveID: "tide"}}, nil
}
func (s *themeDispatchSettings) GetForAccount(string) (uisettings.UISettings, error) { return s.Get() }

type themeDispatchWorkspace struct {
	source  string
	writes  []string
	missing bool
}

func (s *themeDispatchWorkspace) ScopeForPathForPrincipal(p identity.Principal, path string) (workspaceruntime.Scope, error) {
	if p.AccountScopeID != "account" {
		return workspaceruntime.Scope{}, fmt.Errorf("wrong account")
	}
	return workspaceruntime.Scope{RequestedPath: path, ResolvedPath: path, WorkspacePath: s.source, Matched: !s.missing}, nil
}

func (s *themeDispatchWorkspace) ListKnownForPrincipal(identity.Principal, int) ([]workspaceruntime.Entry, error) {
	return nil, nil
}
