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
	if got, want := KeyIdentityAuthSubject(" Local ", "Subject/One"), "identity/auth-subject/local/subject%2Fone"; got != want {
		t.Fatalf("auth subject key = %q, want %q", got, want)
	}
	if got, want := KeyAccountScope(" Account/One "), "account/scope/account%2Fone"; got != want {
		t.Fatalf("account scope key = %q, want %q", got, want)
	}
	if got, want := KeyAccountUser(" Account/One ", " User/One "), "account/user/account%2Fone/user%2Fone"; got != want {
		t.Fatalf("account user key = %q, want %q", got, want)
	}
	if got, want := AccountUserPrefix(" Account/One "), "account/user/account%2Fone/"; got != want {
		t.Fatalf("account user prefix = %q, want %q", got, want)
	}
	if got, want := KeyIdentityCurrentSelection(), "identity/current_selection/default"; got != want {
		t.Fatalf("current selection key = %q, want %q", got, want)
	}
}

func TestIdentityStoreCreateListGetCountAndSelectionInvariants(t *testing.T) {
	identities := newTestIdentityStore(t)

	if _, err := identities.CreateUserIfAbsent(UserRecord{ID: " User-1 ", Username: " Alice ", AccountScopeID: " Account-1 ", AuthProvider: " Local ", AuthSubject: " Subject-1 "}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := identities.CreateAccountScopeIfAbsent(AccountScopeRecord{ID: " Account-1 ", Type: AccountScopeTypePersonal, CreatedByUserID: " User-1 "}); err != nil {
		t.Fatalf("create account scope: %v", err)
	}
	accountUser, err := identities.CreateAccountUserIfAbsent(AccountUserRecord{AccountScopeID: " Account-1 ", UserID: " User-1 "})
	if err != nil {
		t.Fatalf("create account user: %v", err)
	}
	if accountUser.ID != "account-1:user-1" || accountUser.AccountScopeID != "account-1" || accountUser.UserID != "user-1" || accountUser.Status != AccountUserStatusActive {
		t.Fatalf("normalized account user = %+v", accountUser)
	}
	selection, err := identities.PutCurrentSelection(CurrentSelectionRecord{UserID: " User-1 ", WorkspaceID: " Workspace-1 "})
	if err != nil {
		t.Fatalf("put selection: %v", err)
	}
	if selection.UserID != "user-1" || selection.TeamID != "" || selection.WorkspaceID != "workspace-1" {
		t.Fatalf("normalized selection = %+v", selection)
	}

	user, ok, err := identities.GetUser("USER-1")
	if err != nil || !ok || user.Username != "alice" || user.AccountScopeID != "account-1" {
		t.Fatalf("get user = %+v ok=%v err=%v", user, ok, err)
	}
	byUsername, ok, err := identities.GetUserByUsername(" ALICE ")
	if err != nil || !ok || byUsername.ID != user.ID {
		t.Fatalf("get user by username = %+v ok=%v err=%v", byUsername, ok, err)
	}
	byAuthSubject, ok, err := identities.GetUserByAuthSubject(" LOCAL ", "Subject-1")
	if err != nil || !ok || byAuthSubject.ID != user.ID {
		t.Fatalf("get user by auth subject = %+v ok=%v err=%v", byAuthSubject, ok, err)
	}
	accountScope, ok, err := identities.GetAccountScope("ACCOUNT-1")
	if err != nil || !ok || accountScope.CreatedByUserID != "user-1" {
		t.Fatalf("get account scope = %+v ok=%v err=%v", accountScope, ok, err)
	}
	current, ok, err := identities.GetCurrentSelection()
	if err != nil || !ok || current.UserID != "user-1" || current.TeamID != "" {
		t.Fatalf("get current selection = %+v ok=%v err=%v", current, ok, err)
	}

	users, err := identities.ListUsers(10)
	if err != nil || len(users) != 1 || users[0].ID != "user-1" {
		t.Fatalf("list users = %+v err=%v", users, err)
	}
	accountScopes, err := identities.ListAccountScopes(10)
	if err != nil || len(accountScopes) != 1 || accountScopes[0].ID != "account-1" {
		t.Fatalf("list account scopes = %+v err=%v", accountScopes, err)
	}
	accountUsers, err := identities.ListAccountUsers(10)
	if err != nil || len(accountUsers) != 1 || accountUsers[0].UserID != "user-1" {
		t.Fatalf("list account users = %+v err=%v", accountUsers, err)
	}
	userAccountUsers, err := identities.ListAccountUsersForUser("USER-1", 10)
	if err != nil || len(userAccountUsers) != 1 || userAccountUsers[0].AccountScopeID != "account-1" {
		t.Fatalf("list account users for user = %+v err=%v", userAccountUsers, err)
	}
	counts, err := identities.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (IdentityCounts{Users: 1, AccountScopes: 1, AccountUsers: 1, CurrentSelections: 1}) {
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
	if _, err := identities.CreateAccountScopeIfAbsent(AccountScopeRecord{ID: "acct-1", Type: AccountScopeTypePersonal, CreatedByUserID: "missing-user"}); !errors.Is(err, ErrIdentityRecordNotFound) {
		t.Fatalf("account scope without creator err=%v, want ErrIdentityRecordNotFound", err)
	}
	if _, err := identities.CreateAccountScopeIfAbsent(AccountScopeRecord{ID: "acct-1", Type: AccountScopeTypePersonal, CreatedByUserID: "user-1"}); err != nil {
		t.Fatalf("create account scope: %v", err)
	}
	if _, err := identities.CreateAccountUserIfAbsent(AccountUserRecord{AccountScopeID: "acct-1", UserID: "missing-user"}); !errors.Is(err, ErrIdentityRecordNotFound) {
		t.Fatalf("account user without user err=%v, want ErrIdentityRecordNotFound", err)
	}
	if _, err := identities.CreateAccountUserIfAbsent(AccountUserRecord{AccountScopeID: "missing-acct", UserID: "user-1"}); !errors.Is(err, ErrIdentityRecordNotFound) {
		t.Fatalf("account user without account scope err=%v, want ErrIdentityRecordNotFound", err)
	}
	if _, err := identities.PutCurrentSelection(CurrentSelectionRecord{}); err == nil {
		t.Fatal("empty selection succeeded; want failure")
	}
	if _, err := identities.PutCurrentSelection(CurrentSelectionRecord{UserID: "user-1"}); err != nil {
		t.Fatalf("put valid selection: %v", err)
	}
}

func TestIdentityStoreBootstrapBatchCreatesCanonicalRecordsAtomically(t *testing.T) {
	identities := newTestIdentityStore(t)

	created, err := identities.CreateBootstrapIdentityRecords(BootstrapIdentityRecords{
		User:             UserRecord{ID: "User-1", Username: "Alice", AccountScopeID: "Acct-1", AuthProvider: "local", AuthSubject: "User-1"},
		AccountScope:     AccountScopeRecord{ID: "Acct-1", Type: AccountScopeTypePersonal, CreatedByUserID: "User-1"},
		AccountUser:      AccountUserRecord{AccountScopeID: "Acct-1", UserID: "User-1"},
		CurrentSelection: CurrentSelectionRecord{UserID: "User-1"},
	})
	if err != nil {
		t.Fatalf("bootstrap records: %v", err)
	}
	if created.User.ID != "user-1" || created.User.Username != "alice" || created.AccountScope.ID != "acct-1" || created.AccountUser.UserID != "user-1" {
		t.Fatalf("bootstrap normalized records = %+v", created)
	}
	if _, ok, err := identities.GetUserByAuthSubject("local", "User-1"); err != nil || !ok {
		t.Fatalf("auth subject index missing ok=%v err=%v", ok, err)
	}
	counts, err := identities.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (IdentityCounts{Users: 1, AccountScopes: 1, AccountUsers: 1, CurrentSelections: 1}) {
		t.Fatalf("counts after bootstrap = %+v", counts)
	}

	_, err = identities.CreateBootstrapIdentityRecords(BootstrapIdentityRecords{
		User:             UserRecord{ID: "user-2", Username: "bob", AccountScopeID: "acct-2"},
		AccountScope:     AccountScopeRecord{ID: "acct-2", Type: AccountScopeTypePersonal, CreatedByUserID: "user-2"},
		AccountUser:      AccountUserRecord{AccountScopeID: "acct-2", UserID: "user-2"},
		CurrentSelection: CurrentSelectionRecord{UserID: "user-2"},
	})
	if !errors.Is(err, ErrIdentityRecordExists) {
		t.Fatalf("rebootstrap err=%v, want ErrIdentityRecordExists", err)
	}
}

func TestIdentityStoreFailedBootstrapLeavesNoPartialRecords(t *testing.T) {
	identities := newTestIdentityStore(t)

	_, err := identities.CreateBootstrapIdentityRecords(BootstrapIdentityRecords{
		User:             UserRecord{ID: "user-1", Username: "alice", AccountScopeID: "acct-1"},
		AccountScope:     AccountScopeRecord{ID: "acct-1", Type: AccountScopeTypePersonal, CreatedByUserID: "user-1"},
		AccountUser:      AccountUserRecord{AccountScopeID: "different-acct", UserID: "user-1"},
		CurrentSelection: CurrentSelectionRecord{UserID: "user-1"},
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
