package run

import "testing"

// Purpose: permissionRequirement must gate design mutations while bounded reads
// remain read-only, and taskDisabledTools must prevent recursive delegation.
// These policy functions are the narrowest production authority for these rules.
func TestDesignRequestPolicy(t *testing.T) {
	for _, action := range []string{"submit","select","cancel","unknown","status","history","read"} {
		_, required := permissionRequirement("auto","manage_design",`{"action":"`+action+`"}`)
		want := action != "status" && action != "history" && action != "read"
		if required != want { t.Fatalf("%s permission=%v want=%v",action,required,want) }
	}
	if !taskDisabledTools(false)["manage_design"] { t.Fatal("delegated child can recursively submit designs") }
}
