package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: CreateTaskProgram/validateTaskProgramRecord must preserve job prompts
// through 200,000 Unicode characters (~50k tokens), reject excess without writes,
// and retain unrelated field limits. A temporary store is the narrowest layer
// proving admission and persisted postconditions rather than only an error.
func TestTaskProgramStoreMetaPromptCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		char   string
		count  int
		reject bool
	}{
		{"above_old_limit", "a", 4097, false},
		{"ascii_boundary", "a", 200_000, false},
		{"unicode_boundary", "界", 200_000, false},
		{"ascii_overflow", "a", 200_001, true},
		{"unicode_overflow", "界", 200_001, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := NewSessionStore(openTaskProgramTestStore(t))
			fixture := taskProgramStoreFixture("parent", "prompt-limit", "hash")
			prompt := strings.Repeat(tc.char, tc.count)
			fixture.Definition.Jobs[0].MetaPrompt = prompt
			_, fresh, err := sessions.CreateTaskProgram(fixture)
			if tc.reject {
				if err == nil || fresh || err.Error() != `task program job "api" meta_prompt exceeds 200000 Unicode characters (got 200001)` {
					t.Fatalf("oversized admission fresh=%v err=%v", fresh, err)
				}
				if _, found, err := sessions.GetTaskProgram("parent", "prompt-limit"); err != nil || found {
					t.Fatalf("rejected prompt persisted: found=%v err=%v", found, err)
				}
				return
			}
			if err != nil || !fresh {
				t.Fatalf("allowed prompt rejected: fresh=%v err=%v", fresh, err)
			}
			stored, found, err := sessions.GetTaskProgram("parent", "prompt-limit")
			if err != nil || !found || stored.Definition.Jobs[0].MetaPrompt != prompt {
				t.Fatalf("prompt did not round-trip unchanged: found=%v err=%v", found, err)
			}
		})
	}
}

// Purpose: raising MetaPrompt capacity must not enlarge workspace/deliverable
// storage allowances. Exercise CreateTaskProgram and verify rejection writes no
// record, using the same narrow temporary-store admission boundary.
func TestTaskProgramStorePromptLimitPreservesOtherTextBounds(t *testing.T) {
	for _, field := range []string{"workspace_path", "deliverable"} {
		t.Run(field, func(t *testing.T) {
			sessions := NewSessionStore(openTaskProgramTestStore(t))
			fixture := taskProgramStoreFixture("parent", "other-limit", "hash")
			fixture.Definition.Jobs[0].MetaPrompt = strings.Repeat("a", 200_000)
			if field == "workspace_path" {
				fixture.Definition.Jobs[0].WorkspacePath = strings.Repeat("a", 4097)
			} else {
				fixture.Definition.Jobs[0].Deliverable = strings.Repeat("a", 4097)
			}
			if _, fresh, err := sessions.CreateTaskProgram(fixture); err == nil || fresh || !strings.Contains(err.Error(), "bounded definition limits") {
				t.Fatalf("unrelated bound widened: fresh=%v err=%v", fresh, err)
			}
			if _, found, err := sessions.GetTaskProgram("parent", "other-limit"); err != nil || found {
				t.Fatalf("rejected record persisted: found=%v err=%v", found, err)
			}
		})
	}
}
