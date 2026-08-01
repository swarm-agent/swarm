package workspace

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	maxRoutingWorkspaces          = 200
	maxRoutingWorkspaceIDBytes    = 256
	maxRoutingWorkspaceNameBytes  = 512
	maxRoutingDefinitionBytes     = 12 << 10
)

var (
	ErrNoRoutableWorkspaces              = errors.New("no routable workspaces are available")
	ErrRoutingWorkspaceSelectionRequired = errors.New("routing workspace selection is required")
	ErrInvalidRoutingWorkspaceSelection  = errors.New("invalid routing workspace selection")
)

// RoutingWorkspace is the bounded, host-private-path-free workspace description
// made available to Router when it must choose among multiple workspaces.
type RoutingWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Definition  string `json:"definition"`
}

// RoutingContext contains only the workspace information Router may inspect.
// A sole eligible workspace is retained as a server-side binding and omitted
// from Workspaces so Router is neither asked nor permitted to select it.
type RoutingContext struct {
	WorkspaceSelectionRequired bool               `json:"workspace_selection_required,omitempty"`
	Workspaces                 []RoutingWorkspace `json:"workspaces,omitempty"`

	accountScopeID string
	bound          *routingWorkspaceSnapshot
	offered        map[string]routingWorkspaceSnapshot
}

// RoutingWorkspaceSelection is an account-validated server-side routing result.
// WorkspacePath is intentionally available only after validation and is never
// included in RoutingContext or RoutingWorkspace.
type RoutingWorkspaceSelection struct {
	WorkspaceID          string
	WorkspaceGeneration  int64
	WorkspacePath        string
	WorkspaceName        string
	Definition           string
	DefinitionGeneration int64
}

type routingWorkspaceSnapshot struct {
	selection RoutingWorkspaceSelection
	offer     RoutingWorkspace
}

// BuildRoutingContextForPrincipal constructs a deterministic routing view from
// completed, non-empty workspace definitions owned by principal's account.
func (s *Service) BuildRoutingContextForPrincipal(principal identity.Principal) (RoutingContext, error) {
	if err := requirePrincipal(principal); err != nil {
		return RoutingContext{}, err
	}
	if s == nil || s.store == nil {
		return RoutingContext{}, fmt.Errorf("workspace service is not configured")
	}

	// ListForAccount materializes the account's records before applying limit.
	// Use the maximum int so incomplete records cannot hide later eligible ones;
	// the context itself remains strictly capped below.
	entries, err := s.store.ListForAccount(principal.AccountScopeID, int(^uint(0)>>1))
	if err != nil {
		return RoutingContext{}, fmt.Errorf("list routing workspaces: %w", err)
	}

	snapshots := make([]routingWorkspaceSnapshot, 0, len(entries))
	for _, entry := range entries {
		snapshot, ok := routingSnapshotForEntry(principal.AccountScopeID, entry)
		if !ok {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	if len(snapshots) > maxRoutingWorkspaces {
		return RoutingContext{}, fmt.Errorf("routable workspace count %d exceeds limit %d", len(snapshots), maxRoutingWorkspaces)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		left, right := snapshots[i].offer, snapshots[j].offer
		if foldedLeft, foldedRight := strings.ToLower(left.Name), strings.ToLower(right.Name); foldedLeft != foldedRight {
			return foldedLeft < foldedRight
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.WorkspaceID < right.WorkspaceID
	})

	context := RoutingContext{accountScopeID: strings.TrimSpace(principal.AccountScopeID)}
	switch len(snapshots) {
	case 0:
		return context, nil
	case 1:
		context.bound = &snapshots[0]
		return context, nil
	default:
		context.WorkspaceSelectionRequired = true
		context.Workspaces = make([]RoutingWorkspace, 0, len(snapshots))
		context.offered = make(map[string]routingWorkspaceSnapshot, len(snapshots))
		for _, snapshot := range snapshots {
			if _, duplicate := context.offered[snapshot.offer.WorkspaceID]; duplicate {
				return RoutingContext{}, fmt.Errorf("duplicate routable workspace id %q", snapshot.offer.WorkspaceID)
			}
			context.Workspaces = append(context.Workspaces, snapshot.offer)
			context.offered[snapshot.offer.WorkspaceID] = snapshot
		}
		return context, nil
	}
}

// ResolveRoutingWorkspaceForPrincipal resolves the server-bound workspace or
// validates Router's selected workspace against the exact offered snapshot and
// current account-owned completed definition.
func (s *Service) ResolveRoutingWorkspaceForPrincipal(principal identity.Principal, context RoutingContext, selectedWorkspaceID string) (RoutingWorkspaceSelection, error) {
	if err := requirePrincipal(principal); err != nil {
		return RoutingWorkspaceSelection{}, err
	}
	if s == nil || s.store == nil {
		return RoutingWorkspaceSelection{}, fmt.Errorf("workspace service is not configured")
	}
	if strings.TrimSpace(principal.AccountScopeID) != context.accountScopeID {
		return RoutingWorkspaceSelection{}, ErrInvalidRoutingWorkspaceSelection
	}

	selectedWorkspaceID = strings.TrimSpace(selectedWorkspaceID)
	var expected routingWorkspaceSnapshot
	if context.bound != nil {
		if selectedWorkspaceID != "" {
			return RoutingWorkspaceSelection{}, ErrInvalidRoutingWorkspaceSelection
		}
		expected = *context.bound
	} else {
		if len(context.offered) == 0 {
			return RoutingWorkspaceSelection{}, ErrNoRoutableWorkspaces
		}
		if selectedWorkspaceID == "" {
			return RoutingWorkspaceSelection{}, ErrRoutingWorkspaceSelectionRequired
		}
		var ok bool
		expected, ok = context.offered[selectedWorkspaceID]
		if !ok {
			return RoutingWorkspaceSelection{}, ErrInvalidRoutingWorkspaceSelection
		}
	}

	entry, ok, err := s.store.GetByWorkspaceIDForAccount(principal.AccountScopeID, expected.selection.WorkspaceID)
	if err != nil {
		return RoutingWorkspaceSelection{}, fmt.Errorf("get selected routing workspace: %w", err)
	}
	if !ok {
		return RoutingWorkspaceSelection{}, ErrInvalidRoutingWorkspaceSelection
	}
	current, eligible := routingSnapshotForEntry(principal.AccountScopeID, entry)
	if !eligible || current.selection != expected.selection || current.offer != expected.offer {
		return RoutingWorkspaceSelection{}, ErrInvalidRoutingWorkspaceSelection
	}
	return current.selection, nil
}

func routingSnapshotForEntry(accountScopeID string, entry pebblestore.WorkspaceEntry) (routingWorkspaceSnapshot, bool) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID := strings.TrimSpace(entry.WorkspaceID)
	definition := strings.TrimSpace(entry.Definition)
	path := strings.TrimSpace(entry.Path)
	if strings.TrimSpace(entry.AccountScopeID) != accountScopeID ||
		workspaceID == "" || len(workspaceID) > maxRoutingWorkspaceIDBytes || path == "" ||
		strings.TrimSpace(entry.DefinitionStatus) != pebblestore.WorkspaceDefinitionStatusCompleted ||
		definition == "" || len(definition) > maxRoutingDefinitionBytes {
		return routingWorkspaceSnapshot{}, false
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = "Workspace"
	}
	if len(name) > maxRoutingWorkspaceNameBytes {
		return routingWorkspaceSnapshot{}, false
	}
	selection := RoutingWorkspaceSelection{
		WorkspaceID:          workspaceID,
		WorkspaceGeneration:  entry.WorkspaceGeneration,
		WorkspacePath:        path,
		WorkspaceName:        name,
		Definition:           definition,
		DefinitionGeneration: entry.DefinitionGeneration,
	}
	return routingWorkspaceSnapshot{
		selection: selection,
		offer: RoutingWorkspace{WorkspaceID: workspaceID, Name: name, Definition: definition},
	}, true
}
