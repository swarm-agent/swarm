package run

import (
	"strings"
	"testing"
)

// Purpose: executeTaskTool must give program children their hydrated job only,
// not a conflicting parent imperative. This prompt-assembly boundary test
// preserves exact Finder evidence and excludes the instruction that caused a
// real read-only confirmation Finder to attempt parent program recovery.
func TestTaskProgramChildAssignmentExcludesParentOrchestration(t *testing.T) {
	assignment := "Quote the preceding report.\n<finder_handoff job_id=\"audit\">exact report and commit</finder_handoff>"
	parent := "Start a replacement program and commit recovered.txt."
	got := taskChildAssignmentPrompt(assignment, parent, "program-1")
	if !strings.Contains(got, assignment) {
		t.Fatal("hydrated dependency evidence was altered")
	}
	if strings.Contains(got, parent) || strings.Contains(got, "\nPrompt:\n") {
		t.Fatal("parent orchestration leaked as another child task")
	}
	if !strings.Contains(got, "only work assigned to this child") || !strings.Contains(got, "parent owns") {
		t.Fatal("job boundary missing")
	}
	if got := taskChildAssignmentPrompt(assignment, parent, ""); got != "Meta-prompt:\n"+assignment+"\n\nPrompt:\n"+parent {
		t.Fatal("regular launch changed outside program scope")
	}
}
