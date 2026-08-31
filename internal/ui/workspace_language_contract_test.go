package ui

import (
	"os"
	"strings"
	"testing"
)

func TestWorkspacePresentationUsesFlatWorkspaceLanguage(t *testing.T) {
	files := []string{
		"chat_workspace_scope_modal.go",
		"home_workspace_modal.go",
		"v3chat/permission_specialized.go",
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(raw)
		for _, retired := range []string{
			"Add " + "To Workspace",
			"Workspace Manager · Link " + "Directory",
			"New Workspace + Link " + "Directory",
			"Create Current Dir Workspace + Link " + "Directory",
		} {
			if strings.Contains(text, retired) {
				t.Fatalf("%s retained retired workspace wording %q", path, retired)
			}
		}
	}

	permissionSource, err := os.ReadFile("chat_workspace_scope_modal.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Allow for This Chat Session",
		"temporarily for this chat session only",
		"add it as its own workspace from the workspace picker",
	} {
		if !strings.Contains(string(permissionSource), required) {
			t.Fatalf("workspace-scope modal missing canonical wording %q", required)
		}
	}

	managerSource, err := os.ReadFile("home_workspace_modal.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Add Folder as New Workspace",
		"Add the requested folder as its own new workspace",
		"Each saved workspace has one primary folder",
	} {
		if !strings.Contains(string(managerSource), required) {
			t.Fatalf("workspace manager missing canonical wording %q", required)
		}
	}
}
