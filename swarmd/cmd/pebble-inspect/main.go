package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type result struct {
	Check                       string   `json:"check"`
	Passed                      bool     `json:"passed"`
	Users                       int      `json:"users,omitempty"`
	AccountScopes               int      `json:"accountScopes,omitempty"`
	AccountUsers                int      `json:"accountUsers,omitempty"`
	AuthSubjectIndexes          int      `json:"authSubjectIndexes,omitempty"`
	MissingAuthSubjectUsers     int      `json:"missingAuthSubjectUsers,omitempty"`
	MissingAccountUserUsers     int      `json:"missingAccountUserUsers,omitempty"`
	MissingAccountUserScopes    int      `json:"missingAccountUserScopes,omitempty"`
	MissingAccountScopeCreators int      `json:"missingAccountScopeCreators,omitempty"`
	TeamKeys                    int      `json:"teamKeys,omitempty"`
	TeamMembershipKeys          int      `json:"teamMembershipKeys,omitempty"`
	IAMKeys                     int      `json:"iamKeys,omitempty"`
	Errors                      []string `json:"errors,omitempty"`
}

func main() {
	dbPath := flag.String("db", "", "path to swarmd Pebble DB")
	check := flag.String("check", "identity-foundation", "check to run: identity-foundation or no-teams-no-iam")
	jsonOut := flag.Bool("json", false, "emit JSON")
	flag.Parse()
	if strings.TrimSpace(*dbPath) == "" {
		fmt.Fprintln(os.Stderr, "--db is required")
		os.Exit(2)
	}
	store, err := pebblestore.OpenReadOnly(strings.TrimSpace(*dbPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	identities := pebblestore.NewIdentityStore(store)
	res := runCheck(store, identities, strings.TrimSpace(*check))
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		fmt.Printf("%s passed=%v errors=%d\n", res.Check, res.Passed, len(res.Errors))
	}
	if !res.Passed {
		os.Exit(1)
	}
}

func runCheck(store *pebblestore.Store, identities *pebblestore.IdentityStore, check string) result {
	res := result{Check: check, Passed: true}
	if check == "no-teams-no-iam" {
		countForbidden(store, &res)
		res.Passed = len(res.Errors) == 0
		return res
	}
	if check != "identity-foundation" {
		res.Errors = append(res.Errors, "unknown check: "+check)
		res.Passed = false
		return res
	}
	users, err := identities.ListUsers(0)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	res.Users = len(users)
	scopes, err := identities.ListAccountScopes(0)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	res.AccountScopes = len(scopes)
	accountUsers, err := identities.ListAccountUsers(0)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	res.AccountUsers = len(accountUsers)
	if res.Users < 1 {
		res.Errors = append(res.Errors, "identity/user/ has no decodable users")
	}
	if res.AccountScopes < 1 {
		res.Errors = append(res.Errors, "account/scope/ has no decodable account scopes")
	}
	if res.AccountUsers < 1 {
		res.Errors = append(res.Errors, "account/user/ has no decodable account users")
	}
	_ = store.IteratePrefix(pebblestore.IdentityAuthSubjectPrefix(""), 0, func(key string, value []byte) error {
		res.AuthSubjectIndexes++
		if _, ok, err := identities.GetUser(string(value)); err != nil || !ok {
			res.MissingAuthSubjectUsers++
			res.Errors = append(res.Errors, "auth subject index points to missing user: "+key)
		}
		return nil
	})
	for _, au := range accountUsers {
		if _, ok, err := identities.GetUser(au.UserID); err != nil || !ok {
			res.MissingAccountUserUsers++
			res.Errors = append(res.Errors, "account user points to missing user: "+au.ID)
		}
		if _, ok, err := identities.GetAccountScope(au.AccountScopeID); err != nil || !ok {
			res.MissingAccountUserScopes++
			res.Errors = append(res.Errors, "account user points to missing account scope: "+au.ID)
		}
	}
	for _, scope := range scopes {
		if strings.TrimSpace(scope.CreatedByUserID) == "" && scope.Type == pebblestore.AccountScopeTypeSystem {
			continue
		}
		if _, ok, err := identities.GetUser(scope.CreatedByUserID); err != nil || !ok {
			res.MissingAccountScopeCreators++
			res.Errors = append(res.Errors, "account scope creator is missing: "+scope.ID)
		}
	}
	countForbidden(store, &res)
	res.Passed = len(res.Errors) == 0
	return res
}

func countForbidden(store *pebblestore.Store, res *result) {
	_ = store.IteratePrefix(pebblestore.IdentityTeamPrefix(), 0, func(key string, _ []byte) error {
		res.TeamKeys++
		res.Errors = append(res.Errors, "team key exists: "+key)
		return nil
	})
	_ = store.IteratePrefix(pebblestore.IdentityTeamMembershipPrefix(""), 0, func(key string, _ []byte) error {
		res.TeamMembershipKeys++
		res.Errors = append(res.Errors, "team membership key exists: "+key)
		return nil
	})
	for _, prefix := range []string{"iam/", "iam/grant/", "iam/role/", "iam/permission/"} {
		_ = store.IteratePrefix(prefix, 0, func(key string, _ []byte) error {
			res.IAMKeys++
			res.Errors = append(res.Errors, "iam key exists: "+key)
			return nil
		})
	}
}
