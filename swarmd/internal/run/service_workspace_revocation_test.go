package run

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: revoking an additional catalog attachment must remove its tool
// authority without disabling a session whose primary/source remains valid.
// Threat: stale captured grants either poison the whole run or restore deleted
// or foreign roots. ResolveRuntimeWorkspaceScope plus the real catalog deletion
// boundary is the narrowest hermetic layer proving scope and nonmutation; real
// Git lanes additionally exercise managed repository identity validation.
func TestRuntimeWorkspaceSecondaryRevocation(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, scenario := range []string{"deleted-secondary", "foreign-secondary", "unrelated-deletion", "deleted-primary", "foreign-primary", "stale-primary", "stale-secondary", "stale-secondary-path", "missing-primary-grant"} {
			t.Run(fmt.Sprintf("managed=%t/%s", managed, scenario), func(t *testing.T) {
				principal := testRunPrincipal()
				workspaceSvc, _, _, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
				defer cleanup()
				source, secondary := programFixtureRepo(t), programFixtureRepo(t)
				primary, err := workspaceSvc.AddForPrincipal(principal, source, "primary", "", true)
				if err != nil {
					t.Fatal(err)
				}
				owner := principal
				if scenario == "foreign-secondary" {
					owner.AccountScopeID = "foreign-account"
					owner.UserID = "foreign-user"
				}
				additional, err := workspaceSvc.AddForPrincipal(owner, secondary, "secondary", "", false)
				if err != nil {
					t.Fatal(err)
				}
				session := pebblestore.SessionSnapshot{
					ID: "revocation-session", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: source,
					Metadata:        map[string]any{"swarm_v3_source_workspace_id": primary.WorkspaceID, "swarm_v3_source_workspace_generation": fmt.Sprint(primary.WorkspaceGeneration), "swarm_v3_source_workspace_path": source},
					WorkspaceGrants: []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: additional.WorkspaceID, WorkspaceGeneration: additional.WorkspaceGeneration, Path: secondary}},
				}
				expectedRoot := source
				if managed {
					expectedRoot = filepath.Join(t.TempDir(), "lane")
					runTestGit(t, source, "worktree", "add", "-b", "agent/revocation", expectedRoot)
					session.WorktreeEnabled, session.WorktreeRootPath, session.WorktreeBranch = true, expectedRoot, "agent/revocation"
					session.Metadata["base_commit"] = strings.TrimSpace(runTestGit(t, source, "rev-parse", "HEAD"))
					session.Metadata["swarm_v3_runtime_workspace_path"] = expectedRoot
					session.Metadata["swarm_v3_worktree_owner_session_id"] = session.ID
				}
				switch scenario {
				case "deleted-secondary":
					if _, err := workspaceSvc.DeleteCatalogEntryForPrincipal(principal, additional.WorkspaceID, additional.WorkspaceGeneration); err != nil {
						t.Fatal(err)
					}
				case "deleted-primary":
					if _, err := workspaceSvc.DeleteCatalogEntryForPrincipal(principal, primary.WorkspaceID, primary.WorkspaceGeneration); err != nil {
						t.Fatal(err)
					}
				case "unrelated-deletion":
					unrelated, err := workspaceSvc.AddForPrincipal(principal, programFixtureRepo(t), "unrelated", "", false)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := workspaceSvc.DeleteCatalogEntryForPrincipal(principal, unrelated.WorkspaceID, unrelated.WorkspaceGeneration); err != nil {
						t.Fatal(err)
					}
				case "foreign-primary":
					principal.AccountScopeID = "foreign-account"
					principal.UserID = "foreign-user"
				case "stale-primary":
					session.Metadata["swarm_v3_source_workspace_generation"] = fmt.Sprint(primary.WorkspaceGeneration + 1)
				case "stale-secondary":
					session.WorkspaceGrants[0].WorkspaceGeneration++
				case "stale-secondary-path":
					session.WorkspaceGrants[0].Path = source
				case "missing-primary-grant":
					session.WorkspaceGrants[0].Kind = pebblestore.WorkspaceGrantPrimary
					if _, err := workspaceSvc.DeleteCatalogEntryForPrincipal(principal, additional.WorkspaceID, additional.WorkspaceGeneration); err != nil {
						t.Fatal(err)
					}
				}
				before := mustJSON(t, session)
				svc := &Service{workspace: workspaceSvc}
				scope, err := svc.ResolveRuntimeWorkspaceScope(session, principal)
				wantFailure := strings.HasPrefix(scenario, "stale-") || scenario == "deleted-primary" || scenario == "foreign-primary" || scenario == "missing-primary-grant"
				if wantFailure {
					if err == nil || len(scope.Roots) != 0 || len(scope.ReadOnlyRoots) != 0 || scope.PrimaryPath != "" {
						t.Fatalf("required identity failure returned authority: %+v err=%v", scope, err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					wantRoots := []string{expectedRoot}
					if scenario == "unrelated-deletion" {
						wantRoots = append(wantRoots, secondary)
					}
					if scope.PrimaryPath != expectedRoot || !reflect.DeepEqual(scope.Roots, wantRoots) || len(scope.ReadOnlyRoots) != 0 {
						t.Fatalf("scope=%+v want roots=%v", scope, wantRoots)
					}
					_, needsApproval, gateErr := tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(secondary, "file.txt")})})
					if gateErr != nil || needsApproval != (scenario != "unrelated-deletion") {
						t.Fatalf("secondary access: approval=%t err=%v", needsApproval, gateErr)
					}
				}
				if after := mustJSON(t, session); after != before {
					t.Fatal("runtime resolution rewrote session grants/history")
				}
				if scenario == "deleted-secondary" || scenario == "missing-primary-grant" {
					if _, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(principal, additional.WorkspaceID); err != nil || ok {
						t.Fatalf("deleted entry restored: ok=%t err=%v", ok, err)
					}
				}
				if scenario == "foreign-secondary" {
					if _, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(owner, additional.WorkspaceID); err != nil || !ok {
						t.Fatalf("foreign catalog mutated: ok=%t err=%v", ok, err)
					}
				}
			})
		}
	}
}
