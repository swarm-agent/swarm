package tool

import (
	"sort"
	"strings"
	"testing"
)

// mcpFleetLinkedReadAllow are manage_fleet status and activity inputs a
// linked read does not carry, by "action field", with the reason.
var mcpFleetLinkedReadAllow = map[string]string{
	"status timezone":       "TODO: a linked status reads the machine status view (manage_projects machine_status), which takes no timezone; the home formats nothing for it",
	"activity min_severity": "TODO: the linked activity read (Server.ReadFleetActivity) takes no severity filter; a linked activity call returns every severity",
	"activity wait_seconds": "TODO: Swarm Control's held activity wait is not held on a linked machine; a linked activity call answers at once",
}

// Purpose:
//   - Requirement: manage_fleet status and activity with machine= run the
//     linked read with the inputs the action declares; an input a caller
//     passes is carried, or its gap is recorded with a reason here.
//   - Threat/regression: an input added to manage_fleet status or activity
//     (or removed from the linked read) that a linked machine silently never
//     sees, with nothing failing.
//   - Boundary/authority: the catalog declaration (manageFleetCatalog) and
//     the forwarded list Runtime.executeManageFleet copies to the linked read
//     (manageFleetLinkedReadInputs).
//   - Narrowest layer: the declaration and the list the forwarder iterates;
//     the linked read itself is held by the api fleet activity tests. Its
//     pair for the HTTP forwarders is api's TestMCPRouteParityLinkedForwarding.
func TestMCPRouteParityManageFleetLinkedReads(t *testing.T) {
	catalog := manageFleetCatalog()
	forwarded := map[string]bool{}
	for _, key := range manageFleetLinkedReadInputs {
		forwarded[key] = true
	}
	declaredByRead := map[string]bool{}
	matched := map[string]bool{}
	var drift []string
	for _, name := range []string{"status", "activity"} {
		action, ok := catalog.Action(name)
		if !ok {
			t.Fatalf("manage_fleet %s is not declared", name)
		}
		for _, input := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
			declaredByRead[input] = true
			if input == "machine" || forwarded[input] {
				continue
			}
			key := name + " " + input
			if _, ok := mcpFleetLinkedReadAllow[key]; ok {
				matched[key] = true
				continue
			}
			drift = append(drift, "manage_fleet "+key+": declared but not carried to the linked read (manageFleetLinkedReadInputs)")
		}
	}
	for key := range forwarded {
		if !declaredByRead[key] {
			drift = append(drift, "manage_fleet: the linked read carries "+key+", which neither status nor activity declares")
		}
	}
	for key := range mcpFleetLinkedReadAllow {
		if !matched[key] {
			drift = append(drift, "mcpFleetLinkedReadAllow entry "+key+" no longer matches: remove it")
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("manage_fleet linked read drift:\n%s", strings.Join(drift, "\n"))
	}
}
