package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestIdentityKeysEncodeAndPrefixRelations(t *testing.T) {
	if got, want := KeyIdentityUser(" User/One "), "identity/user/user%2Fone"; got != want {
		t.Fatalf("user key = %q, want %q", got, want)
	}
	if got, want := KeyIdentityUserByUsername(" Alice Smith "), "identity/user_by_username/alice%20smith"; got != want {
		t.Fatalf("username key = %q, want %q", got, want)
	}
	if got, want := KeyIdentityTeam(" Default/Team "), "identity/team/default%2Fteam"; got != want {
		t.Fatalf("team key = %q, want %q", got, want)
	}
	if got, want := KeyIdentityTeamMembership(" Default/Team ", " User/One "), "identity/membership/default%2Fteam/user%2Fone"; got != want {
		t.Fatalf("membership key = %q, want %q", got, want)
	}
	if got, want := IdentityTeamMembershipPrefix(" Default/Team "), "identity/membership/default%2Fteam/"; got != want {
		t.Fatalf("membership prefix = %q, want %q", got, want)
	}
	if got, want := KeyIdentityCurrentSelection(), "identity/current_selection/default"; got != want {
		t.Fatalf("current selection key = %q, want %q", got, want)
	}
}

func TestIdentityStoreCreateListGetCountAndSelectionInvariants(t *testing.T) {
	identities := newTestIdentityStore(t)

	if _, err := identities.CreateUserIfAbsent(UserRecord{ID: " User-1 ", Username: " Alice "}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := identities.CreateTeamIfAbsent(TeamRecord{ID: " Team-Default ", Name: "Default backend team", Default: true}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	membership, err := identities.CreateTeamMembershipIfAbsent(TeamMembershipRecord{TeamID: " Team-Default ", UserID: " User-1 ", Role: " OWNER "})
	if err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if membership.TeamID != "team-default" || membership.UserID != "user-1" || membership.Role != TeamRoleOwner {
		t.Fatalf("normalized membership = %+v", membership)
	}
	selection, err := identities.PutCurrentSelection(CurrentSelectionRecord{UserID: " User-1 ", TeamID: " Team-Default ", WorkspaceID: " Workspace-1 "})
	if err != nil {
		t.Fatalf("put selection: %v", err)
	}
	if selection.UserID != "user-1" || selection.TeamID != "team-default" || selection.WorkspaceID != "workspace-1" {
		t.Fatalf("normalized selection = %+v", selection)
	}

	user, ok, err := identities.GetUser("USER-1")
	if err != nil || !ok || user.Username != "alice" {
		t.Fatalf("get user = %+v ok=%v err=%v", user, ok, err)
	}
	byUsername, ok, err := identities.GetUserByUsername(" ALICE ")
	if err != nil || !ok || byUsername.ID != user.ID {
		t.Fatalf("get user by username = %+v ok=%v err=%v", byUsername, ok, err)
	}
	team, ok, err := identities.GetTeam("TEAM-DEFAULT")
	if err != nil || !ok || !team.Default {
		t.Fatalf("get team = %+v ok=%v err=%v", team, ok, err)
	}
	current, ok, err := identities.GetCurrentSelection()
	if err != nil || !ok || current.UserID != "user-1" || current.TeamID != "team-default" {
		t.Fatalf("get current selection = %+v ok=%v err=%v", current, ok, err)
	}

	users, err := identities.ListUsers(10)
	if err != nil || len(users) != 1 || users[0].ID != "user-1" {
		t.Fatalf("list users = %+v err=%v", users, err)
	}
	teams, err := identities.ListTeams(10)
	if err != nil || len(teams) != 1 || teams[0].ID != "team-default" {
		t.Fatalf("list teams = %+v err=%v", teams, err)
	}
	memberships, err := identities.ListTeamMemberships(10)
	if err != nil || len(memberships) != 1 || memberships[0].UserID != "user-1" {
		t.Fatalf("list memberships = %+v err=%v", memberships, err)
	}
	teamMemberships, err := identities.ListTeamMembershipsForTeam("TEAM-DEFAULT", 10)
	if err != nil || len(teamMemberships) != 1 || teamMemberships[0].TeamID != "team-default" {
		t.Fatalf("list team memberships = %+v err=%v", teamMemberships, err)
	}
	counts, err := identities.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (IdentityCounts{Users: 1, Teams: 1, TeamMemberships: 1, CurrentSelections: 1}) {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestIdentityStoreRejectsInvalidAndTeamOnlyState(t *testing.T) {
	identities := newTestIdentityStore(t)

	if _, err := identities.CreateUserIfAbsent(UserRecord{ID: "user-1", Username: "   "}); err == nil {
		t.Fatal("expected empty username error")
	}
	if _, err := identities.CreateUserIfAbsent(UserRecord{ID: "user-1", Username: "Alice"}); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := identities.CreateUserIfAbsent(UserRecord{ID: "user-2", Username: " alice "}); !errors.Is(err, ErrIdentityRecordExists) {
		t.Fatalf("duplicate normalized username err=%v, want ErrIdentityRecordExists", err)
	}
	if _, err := identities.CreateTeamIfAbsent(TeamRecord{ID: "team-default", Name: "Default", Default: true}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	if _, err := identities.CreateTeamMembershipIfAbsent(TeamMembershipRecord{TeamID: "team-default", UserID: "missing-user", Role: TeamRoleOwner}); !errors.Is(err, ErrIdentityRecordNotFound) {
		t.Fatalf("membership without user err=%v, want ErrIdentityRecordNotFound", err)
	}
	if _, err := identities.CreateTeamMembershipIfAbsent(TeamMembershipRecord{TeamID: "missing-team", UserID: "user-1", Role: TeamRoleOwner}); !errors.Is(err, ErrIdentityRecordNotFound) {
		t.Fatalf("membership without team err=%v, want ErrIdentityRecordNotFound", err)
	}
	if _, err := identities.PutCurrentSelection(CurrentSelectionRecord{TeamID: "team-default"}); err == nil {
		t.Fatal("team-only selection succeeded; want failure")
	}
	if _, err := identities.PutCurrentSelection(CurrentSelectionRecord{UserID: "user-1", TeamID: "team-default"}); !errors.Is(err, ErrIdentityRecordNotFound) {
		t.Fatalf("selection without membership err=%v, want ErrIdentityRecordNotFound", err)
	}
	if _, err := identities.CreateTeamMembershipIfAbsent(TeamMembershipRecord{TeamID: "team-default", UserID: "user-1", Role: TeamRoleOwner}); err != nil {
		t.Fatalf("create valid membership: %v", err)
	}
	if _, err := identities.PutCurrentSelection(CurrentSelectionRecord{UserID: "user-1", TeamID: "team-default"}); err != nil {
		t.Fatalf("put valid selection: %v", err)
	}
}

func TestIdentityStoreBootstrapBatchCreatesCanonicalRecordsAtomically(t *testing.T) {
	identities := newTestIdentityStore(t)

	created, err := identities.CreateBootstrapIdentityRecords(BootstrapIdentityRecords{
		User:             UserRecord{ID: "User-1", Username: "Alice"},
		Team:             TeamRecord{ID: "Team-Default", Name: "Default backend team", Default: true},
		Membership:       TeamMembershipRecord{TeamID: "Team-Default", UserID: "User-1", Role: TeamRoleOwner},
		CurrentSelection: CurrentSelectionRecord{UserID: "User-1", TeamID: "Team-Default"},
	})
	if err != nil {
		t.Fatalf("bootstrap records: %v", err)
	}
	if created.User.ID != "user-1" || created.User.Username != "alice" || !created.Team.Default || created.Membership.Role != TeamRoleOwner {
		t.Fatalf("bootstrap normalized records = %+v", created)
	}
	counts, err := identities.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (IdentityCounts{Users: 1, Teams: 1, TeamMemberships: 1, CurrentSelections: 1}) {
		t.Fatalf("counts after bootstrap = %+v", counts)
	}

	_, err = identities.CreateBootstrapIdentityRecords(BootstrapIdentityRecords{
		User:             UserRecord{ID: "user-2", Username: "bob"},
		Team:             TeamRecord{ID: "team-2", Name: "Second", Default: true},
		Membership:       TeamMembershipRecord{TeamID: "team-2", UserID: "user-2", Role: TeamRoleOwner},
		CurrentSelection: CurrentSelectionRecord{UserID: "user-2", TeamID: "team-2"},
	})
	if !errors.Is(err, ErrIdentityRecordExists) {
		t.Fatalf("rebootstrap err=%v, want ErrIdentityRecordExists", err)
	}
}

func TestIdentityStoreFailedBootstrapLeavesNoPartialRecords(t *testing.T) {
	identities := newTestIdentityStore(t)

	_, err := identities.CreateBootstrapIdentityRecords(BootstrapIdentityRecords{
		User:             UserRecord{ID: "user-1", Username: "alice"},
		Team:             TeamRecord{ID: "team-default", Name: "Default backend team", Default: true},
		Membership:       TeamMembershipRecord{TeamID: "different-team", UserID: "user-1", Role: TeamRoleOwner},
		CurrentSelection: CurrentSelectionRecord{UserID: "user-1", TeamID: "team-default"},
	})
	if err == nil {
		t.Fatal("expected failed bootstrap")
	}
	counts, countErr := identities.IdentityCounts()
	if countErr != nil {
		t.Fatalf("identity counts: %v", countErr)
	}
	if counts != (IdentityCounts{}) {
		t.Fatalf("counts after failed bootstrap = %+v, want zero", counts)
	}
	if _, ok, getErr := identities.GetUser("user-1"); getErr != nil || ok {
		t.Fatalf("user after failed bootstrap ok=%v err=%v", ok, getErr)
	}
}

func newTestIdentityStore(t *testing.T) *IdentityStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "identity.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewIdentityStore(store)
}
