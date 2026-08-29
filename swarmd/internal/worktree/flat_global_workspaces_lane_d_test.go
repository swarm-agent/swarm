package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flatGlobalWorkspaceManifestPath = "../../../docs/checkpoints/flat-global-workspaces-scenario-manifest.json"

type flatGlobalWorkspaceManifest struct {
	Schema    string `json:"schema"`
	Candidate struct {
		CapturedBranch         string `json:"captured_branch"`
		ProductionCandidateSHA string `json:"production_candidate_sha"`
		TreeStateAtFreeze      string `json:"tree_state_at_freeze"`
	} `json:"candidate"`
	Requirements []struct {
		ID           string   `json:"id"`
		Cluster      string   `json:"cluster"`
		Outcome      string   `json:"outcome"`
		PlannedLanes []string `json:"planned_lanes"`
	} `json:"requirements"`
	Scenarios []struct {
		ID                             string   `json:"id"`
		SemanticKey                    string   `json:"semantic_key"`
		Title                          string   `json:"title"`
		SpecialCase                    bool     `json:"special_case"`
		SpecialCaseKind                string   `json:"special_case_kind"`
		Requirements                   []string `json:"requirements"`
		PrimaryLane                    string   `json:"primary_lane"`
		ObservableOutcome              string   `json:"observable_outcome"`
		WorkspaceAuthorizationExpected string   `json:"workspace_authorization_expected"`
		OperationPolicyExpected        string   `json:"operation_policy_expected"`
	} `json:"scenarios"`
}

// TestFlatGlobalWorkspacesLaneDManifest validates the integrity and internal
// requirement mapping of the complete frozen inventory. It does not claim that
// recounting manifest metadata executes all 59 scenarios. The executable tests
// below separately challenge the production Git lifecycle.
func TestFlatGlobalWorkspacesLaneDManifest(t *testing.T) {
	manifest, raw := loadFlatGlobalWorkspaceManifest(t)
	if manifest.Schema != "swarm.flat-global-workspaces.scenario-manifest/v1" {
		t.Fatalf("manifest schema = %q", manifest.Schema)
	}
	if manifest.Candidate.CapturedBranch == "" || len(manifest.Candidate.ProductionCandidateSHA) != 40 || manifest.Candidate.TreeStateAtFreeze != "clean" {
		t.Fatalf("incomplete frozen candidate: %#v", manifest.Candidate)
	}
	if manifest.Candidate.ProductionCandidateSHA != "41bd4671275f825d13fd31f008e71258fb22cf54" {
		t.Fatalf("unexpected frozen production candidate SHA %q", manifest.Candidate.ProductionCandidateSHA)
	}
	if len(manifest.Scenarios) != 59 {
		t.Fatalf("scenario count = %d, want 59", len(manifest.Scenarios))
	}

	requirementIDs := map[string]bool{}
	for _, requirement := range manifest.Requirements {
		if requirement.ID == "" || requirement.Cluster == "" || requirement.Outcome == "" || len(requirement.PlannedLanes) == 0 {
			t.Fatalf("incomplete requirement contract: %#v", requirement)
		}
		if requirementIDs[requirement.ID] {
			t.Fatalf("duplicate requirement identity %q", requirement.ID)
		}
		requirementIDs[requirement.ID] = true
		for _, lane := range requirement.PlannedLanes {
			if len(lane) != 1 || !strings.Contains("ABCDE", lane) {
				t.Fatalf("requirement %s has invalid planned lane %q", requirement.ID, lane)
			}
		}
	}
	if len(requirementIDs) == 0 {
		t.Fatal("manifest has no frozen requirements")
	}

	ids := map[string]bool{}
	keys := map[string]bool{}
	laneCounts := map[string]int{}
	specialKinds := map[string]bool{}
	requirementCoverage := map[string]int{}
	for i, scenario := range manifest.Scenarios {
		wantID := "E2E-" + leftPad3(i+1)
		if scenario.ID != wantID {
			t.Fatalf("scenario[%d].id = %q, want %q", i, scenario.ID, wantID)
		}
		if ids[scenario.ID] || keys[scenario.SemanticKey] {
			t.Fatalf("duplicate scenario identity: id=%q semantic_key=%q", scenario.ID, scenario.SemanticKey)
		}
		ids[scenario.ID], keys[scenario.SemanticKey] = true, true
		if scenario.SemanticKey == "" || scenario.Title == "" || len(scenario.Requirements) == 0 || scenario.ObservableOutcome == "" || scenario.WorkspaceAuthorizationExpected == "" || scenario.OperationPolicyExpected == "" {
			t.Fatalf("scenario %s has incomplete evidence contract: %#v", scenario.ID, scenario)
		}
		for _, requirementID := range scenario.Requirements {
			if !requirementIDs[requirementID] {
				t.Fatalf("scenario %s references unknown requirement %q", scenario.ID, requirementID)
			}
			requirementCoverage[requirementID]++
		}
		if !strings.Contains("ABCDE", scenario.PrimaryLane) || len(scenario.PrimaryLane) != 1 {
			t.Fatalf("scenario %s has invalid primary lane %q", scenario.ID, scenario.PrimaryLane)
		}
		laneCounts[scenario.PrimaryLane]++
		if scenario.SpecialCase {
			if scenario.SpecialCaseKind == "" || specialKinds[scenario.SpecialCaseKind] {
				t.Fatalf("scenario %s has invalid/duplicate special case kind %q", scenario.ID, scenario.SpecialCaseKind)
			}
			specialKinds[scenario.SpecialCaseKind] = true
		} else if scenario.SpecialCaseKind != "" {
			t.Fatalf("scenario %s declares a kind without special_case", scenario.ID)
		}
	}
	if len(specialKinds) != 13 {
		t.Fatalf("special case count = %d, want 13", len(specialKinds))
	}
	for _, lane := range []string{"A", "B", "C", "D", "E"} {
		if laneCounts[lane] == 0 {
			t.Fatalf("lane %s has no scenario", lane)
		}
	}
	for requirementID := range requirementIDs {
		if requirementCoverage[requirementID] == 0 {
			t.Fatalf("requirement %s has no scenario mapping", requirementID)
		}
	}
	verifyFlatGlobalWorkspaceManifestDigest(t, raw)
}

// TestFlatGlobalWorkspacesLaneDCapturedBranchesAndIsolation covers E2E-047,
// E2E-048, E2E-049, E2E-050, E2E-051, E2E-057, and E2E-058.
func TestFlatGlobalWorkspacesLaneDCapturedBranchesAndIsolation(t *testing.T) {
	for _, branch := range []string{"dev", "main", "trunk", "feature/manual-selection", "ordinary-nonstandard"} {
		t.Run(strings.ReplaceAll(branch, "/", "_"), func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			repo, base := initLaneDRepository(t, branch)
			sourceStateBefore := inspectLaneDWorkspace(t, repo)
			svc := &Service{}
			captured, err := svc.ResolveTaskBase(repo)
			if err != nil {
				t.Fatalf("capture source: %v", err)
			}
			if captured.ParentBranch != branch || captured.BaseCommit != base {
				t.Fatalf("captured base = %#v, want branch=%q HEAD=%s", captured, branch, base)
			}

			allocation, err := svc.AllocateTaskWorkspace(repo, captured, "lane-d-"+strings.NewReplacer("/", "-", "_", "-").Replace(branch))
			if err != nil {
				t.Fatalf("allocate managed child: %v", err)
			}
			if sameCleanPath(repo, allocation.WorkspacePath) || allocation.BaseCommit != base || allocation.BaseBranch != branch {
				t.Fatalf("child allocation did not preserve captured lineage: %#v", allocation)
			}
			writeAndCommitLaneD(t, allocation.WorkspacePath, "child.txt", branch+"\n", "lane D child")
			childState := inspectLaneDWorkspace(t, allocation.WorkspacePath)
			sourceStateAfterChild := inspectLaneDWorkspace(t, repo)
			if sourceStateAfterChild != sourceStateBefore {
				t.Fatalf("managed child switched or wrote source: before=%#v after=%#v", sourceStateBefore, sourceStateAfterChild)
			}

			plan, err := svc.PrepareTaskIntegration(repo, branch, base, []TaskIntegrationChild{{SessionID: "lane-d", BaseCommit: base, HeadCommit: childState.HeadCommit}})
			if err != nil {
				t.Fatalf("prepare captured integration: %v", err)
			}
			result, err := svc.ApplyTaskIntegration(repo, plan)
			if err != nil {
				t.Fatalf("apply captured integration: %v", err)
			}
			integrated := inspectLaneDWorkspace(t, repo)
			if integrated.BranchName != branch || integrated.HeadCommit != result.ResultingParentHead || integrated.HeadCommit == base || !integrated.Clean {
				t.Fatalf("integrated source state = %#v, result=%#v", integrated, result)
			}
			if ok, err := svc.TaskCommitRangeIntegratedInto(repo, base, childState.HeadCommit, integrated.HeadCommit); err != nil || !ok {
				t.Fatalf("verify integrated child range: integrated=%t err=%v", ok, err)
			}
		})
	}
}

// TestFlatGlobalWorkspacesLaneDRejectsInvalidCaptureWithoutMutation covers
// E2E-052, E2E-053, and E2E-054.
func TestFlatGlobalWorkspacesLaneDRejectsInvalidCaptureWithoutMutation(t *testing.T) {
	t.Run("detached HEAD", func(t *testing.T) {
		repo, base := initLaneDRepository(t, "dev")
		if _, err := runGit(repo, "checkout", "--detach", base); err != nil {
			t.Fatal(err)
		}
		before := inspectLaneDWorkspace(t, repo)
		_, err := (&Service{}).ResolveTaskBase(repo)
		if err == nil || !strings.Contains(err.Error(), "detached HEAD") {
			t.Fatalf("detached capture error = %v", err)
		}
		if after := inspectLaneDWorkspace(t, repo); after != before {
			t.Fatalf("detached rejection mutated source: before=%#v after=%#v", before, after)
		}
	})

	t.Run("non Git", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "preserve.txt")
		if err := os.WriteFile(marker, []byte("preserve\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := (&Service{}).ResolveTaskBase(dir)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "git") {
			t.Fatalf("non-Git capture error = %v", err)
		}
		if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "preserve\n" {
			t.Fatalf("non-Git rejection changed fixture: %q, %v", got, readErr)
		}
	})

	t.Run("dirty parent", func(t *testing.T) {
		repo, _ := initLaneDRepository(t, "dev")
		dirty := filepath.Join(repo, "uncommitted.txt")
		if err := os.WriteFile(dirty, []byte("user work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := (&Service{}).ResolveTaskBase(repo)
		if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
			t.Fatalf("dirty capture error = %v", err)
		}
		if got, readErr := os.ReadFile(dirty); readErr != nil || string(got) != "user work\n" {
			t.Fatalf("dirty work was not preserved: %q, %v", got, readErr)
		}
	})
}

func TestFlatGlobalWorkspacesLaneDRejectsCapturedSourceMovement(t *testing.T) {
	for _, movement := range []string{"branch", "head"} {
		t.Run(movement, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			repo, base := initLaneDRepository(t, "captured")
			svc := &Service{}
			captured, err := svc.ResolveTaskBase(repo)
			if err != nil {
				t.Fatal(err)
			}
			allocation, err := svc.AllocateTaskWorkspace(repo, captured, "movement-"+movement)
			if err != nil {
				t.Fatal(err)
			}
			writeAndCommitLaneD(t, allocation.WorkspacePath, "child.txt", "preserve child\n", "child")
			child := inspectLaneDWorkspace(t, allocation.WorkspacePath)

			if movement == "branch" {
				if _, err := runGit(repo, "switch", "-c", "moved"); err != nil {
					t.Fatal(err)
				}
			} else {
				writeAndCommitLaneD(t, repo, "parent.txt", "moved parent\n", "move parent HEAD")
			}
			parentBefore := inspectLaneDWorkspace(t, repo)
			_, err = svc.PrepareTaskIntegration(repo, "captured", base, []TaskIntegrationChild{{SessionID: "movement", BaseCommit: base, HeadCommit: child.HeadCommit}})
			want := "stale parent branch"
			if movement == "head" {
				want = "stale parent HEAD"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("movement error = %v, want %q", err, want)
			}
			if parentAfter := inspectLaneDWorkspace(t, repo); parentAfter != parentBefore {
				t.Fatalf("movement rejection mutated parent: before=%#v after=%#v", parentBefore, parentAfter)
			}
			if childAfter := inspectLaneDWorkspace(t, allocation.WorkspacePath); childAfter != child {
				t.Fatalf("movement rejection discarded child: before=%#v after=%#v", child, childAfter)
			}
		})
	}
}

func TestFlatGlobalWorkspacesLaneDRejectsCleanUnintegratedCleanup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo, base := initLaneDRepository(t, "captured-feature")
	svc := &Service{}
	captured, err := svc.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := svc.AllocateTaskWorkspace(repo, captured, "unintegrated-clean")
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitLaneD(t, allocation.WorkspacePath, "unintegrated.txt", "preserve me\n", "unintegrated child")
	child := inspectLaneDWorkspace(t, allocation.WorkspacePath)
	if !child.Clean {
		t.Fatalf("fixture child is dirty: %#v", child)
	}

	err = svc.RemoveIntegratedTaskWorkspace(repo, allocation.WorkspacePath, "unintegrated-clean", allocation.BranchName, base, child.HeadCommit)
	if err == nil || !strings.Contains(err.Error(), "not integrated") {
		t.Fatalf("clean unintegrated cleanup error = %v", err)
	}
	preserved := inspectLaneDWorkspace(t, allocation.WorkspacePath)
	if preserved != child {
		t.Fatalf("unintegrated child was mutated or removed: before=%#v after=%#v", child, preserved)
	}
	if got, readErr := os.ReadFile(filepath.Join(allocation.WorkspacePath, "unintegrated.txt")); readErr != nil || string(got) != "preserve me\n" {
		t.Fatalf("unintegrated child bytes were lost: %q, %v", got, readErr)
	}
}

func TestFlatGlobalWorkspacesLaneDE2E059ForbidsLegacyDefaultBranchInference(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo, base := initLaneDRepository(t, "feature/actually-selected")
	for _, branch := range []string{"main", "master", "trunk", "dev"} {
		if _, err := runGit(repo, "branch", branch, base); err != nil {
			t.Fatalf("create legacy branch %q: %v", branch, err)
		}
	}
	if _, err := runGit(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("seed legacy remote HEAD: %v", err)
	}

	svc := &Service{}
	captured, err := svc.ResolveTaskBase(repo)
	if err != nil {
		t.Fatalf("capture selected branch: %v", err)
	}
	if captured.ParentBranch != "feature/actually-selected" || captured.BaseCommit != base {
		t.Fatalf("legacy branch metadata overrode capture: %+v", captured)
	}
	allocation, err := svc.AllocateTaskWorkspace(repo, captured, "legacy-default-forbidden")
	if err != nil {
		t.Fatalf("allocate selected branch child: %v", err)
	}
	writeAndCommitLaneD(t, allocation.WorkspacePath, "selected.txt", "selected\n", "selected branch child")
	child := inspectLaneDWorkspace(t, allocation.WorkspacePath)
	plan, err := svc.PrepareTaskIntegration(repo, captured.ParentBranch, captured.BaseCommit, []TaskIntegrationChild{{SessionID: "legacy-default", BaseCommit: base, HeadCommit: child.HeadCommit}})
	if err != nil {
		t.Fatalf("prepare selected branch integration: %v", err)
	}
	result, err := svc.ApplyTaskIntegration(repo, plan)
	if err != nil {
		t.Fatalf("apply selected branch integration: %v", err)
	}
	state := inspectLaneDWorkspace(t, repo)
	if state.BranchName != "feature/actually-selected" || state.HeadCommit != result.ResultingParentHead {
		t.Fatalf("integration used legacy default branch: state=%+v result=%+v", state, result)
	}
}

func TestFlatGlobalWorkspacesLaneDIntegrationAndCleanupSafety(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo, base := initLaneDRepository(t, "captured-feature")
	svc := &Service{}
	captured, err := svc.ResolveTaskBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := svc.AllocateTaskWorkspace(repo, captured, "atomic-integration")
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitLaneD(t, allocation.WorkspacePath, "one.txt", "one\n", "one")
	writeAndCommitLaneD(t, allocation.WorkspacePath, "two.txt", "two\n", "two")
	child := inspectLaneDWorkspace(t, allocation.WorkspacePath)

	plan, err := svc.PrepareTaskIntegration(repo, "captured-feature", base, []TaskIntegrationChild{{SessionID: "atomic-integration", BaseCommit: base, HeadCommit: child.HeadCommit}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Commits) != 2 || len(plan.Entries) != 1 || len(plan.Entries[0].Files) != 2 {
		t.Fatalf("incomplete ordered integration plan: %#v", plan)
	}
	result, err := svc.ApplyTaskIntegration(repo, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResultingParentHead == base || !inspectLaneDWorkspace(t, repo).Clean {
		t.Fatalf("integration did not produce a new clean parent: %#v", result)
	}
	if err := svc.RemoveIntegratedTaskWorkspace(repo, allocation.WorkspacePath, "atomic-integration", allocation.BranchName, base, child.HeadCommit); err != nil {
		t.Fatalf("clean integrated cleanup: %v", err)
	}
	if _, err := os.Stat(allocation.WorkspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean integrated child remains: %v", err)
	}

	parentHead := inspectLaneDWorkspace(t, repo).HeadCommit
	dirty, err := svc.AllocateTaskWorkspace(repo, TaskBase{RepoRoot: captured.RepoRoot, ParentBranch: "captured-feature", BaseCommit: parentHead}, "dirty-recovery")
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitLaneD(t, dirty.WorkspacePath, "committed.txt", "committed\n", "dirty child commit")
	dirtyHead := inspectLaneDWorkspace(t, dirty.WorkspacePath).HeadCommit
	dirtyPlan, err := svc.PrepareTaskIntegration(repo, "captured-feature", parentHead, []TaskIntegrationChild{{SessionID: "dirty-recovery", BaseCommit: parentHead, HeadCommit: dirtyHead}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyTaskIntegration(repo, dirtyPlan); err != nil {
		t.Fatal(err)
	}
	dirtyFile := filepath.Join(dirty.WorkspacePath, "recover-me.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted recovery\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveIntegratedTaskWorkspace(repo, dirty.WorkspacePath, "dirty-recovery", dirty.BranchName, parentHead, dirtyHead); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty cleanup rejection = %v", err)
	}
	if got, err := os.ReadFile(dirtyFile); err != nil || string(got) != "uncommitted recovery\n" {
		t.Fatalf("dirty recovery bytes were lost: %q, %v", got, err)
	}
}

func loadFlatGlobalWorkspaceManifest(t *testing.T) (flatGlobalWorkspaceManifest, []byte) {
	t.Helper()
	raw, err := os.ReadFile(flatGlobalWorkspaceManifestPath)
	if err != nil {
		t.Fatalf("read scenario manifest: %v", err)
	}
	var manifest flatGlobalWorkspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode scenario manifest: %v", err)
	}
	return manifest, raw
}

func verifyFlatGlobalWorkspaceManifestDigest(t *testing.T, raw []byte) {
	t.Helper()
	digestFile := strings.TrimSuffix(flatGlobalWorkspaceManifestPath, ".json") + ".sha256"
	line, err := os.ReadFile(digestFile)
	if err != nil {
		t.Fatalf("read manifest digest: %v", err)
	}
	fields := strings.Fields(string(line))
	if len(fields) != 2 || fields[1] != "docs/checkpoints/flat-global-workspaces-scenario-manifest.json" {
		t.Fatalf("invalid manifest digest record %q", line)
	}
	want, err := hex.DecodeString(fields[0])
	if err != nil || len(want) != sha256.Size {
		t.Fatalf("invalid manifest digest %q: %v", fields[0], err)
	}
	got := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(got[:]), fields[0]) {
		t.Fatalf("manifest digest = %x, want %s", got, fields[0])
	}
}

func initLaneDRepository(t *testing.T, branch string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", branch); err != nil {
		t.Fatalf("init repository on %s: %v", branch, err)
	}
	_, _ = runGit(repo, "config", "user.email", "lane-d@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Lane D Diagnostic")
	writeAndCommitLaneD(t, repo, "base.txt", "base\n", "base")
	head, err := runGit(repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		t.Fatal(err)
	}
	return repo, head
}

func writeAndCommitLaneD(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "add", "--", name); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "commit", "-m", message); err != nil {
		t.Fatal(err)
	}
}

func inspectLaneDWorkspace(t *testing.T, path string) TaskWorkspaceState {
	t.Helper()
	state, err := (&Service{}).InspectTaskWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func leftPad3(value int) string {
	digits := []byte{byte('0' + value/100), byte('0' + (value/10)%10), byte('0' + value%10)}
	return string(digits)
}
