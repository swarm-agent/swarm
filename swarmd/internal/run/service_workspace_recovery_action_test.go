package run

import (
 "encoding/json"
 "strings"
 "testing"
)

// Purpose: parseManageWorkspaceArguments is the narrow tool-input boundary.
// Missing or mistyped recovery evidence and unsupported imports must fail before
// any reservation or Git access, while exact copy selections survive unchanged.
func TestRecoveryActionArguments(t *testing.T) {
 valid := map[string]any{"action":"copy_worktree", "workspace_id":"workspace", "workspace_generation":1, "worktree_path":"/fixture/lane", "owner_session_id":"owner", "ownership_revision":1, "head":"head", "fingerprint":strings.Repeat("a",64), "operation_id":"copy-one", "files":[]string{"file.txt"}}
 encode := func(m map[string]any) string { data, err := json.Marshal(m); if err != nil { t.Fatal(err) }; return string(data) }
 args, err := parseManageWorkspaceArguments(encode(valid))
 if err != nil || len(args.Recovery.Files) != 1 || args.Recovery.Files[0] != "file.txt" || args.Recovery.Revision != 1 { t.Fatalf("exact copy: %+v %v", args, err) }
 for _, key := range []string{"owner_session_id", "ownership_revision", "head", "fingerprint", "operation_id", "files"} {
  t.Run(key, func(t *testing.T) {
   m := cloneGenericMap(valid); delete(m,key)
   if _, err := parseManageWorkspaceArguments(encode(m)); err == nil { t.Fatal("accepted missing evidence") }
  })
 }
 for _, patch := range []map[string]any{{"commits":[]string{"head"}}, {"patch":"diff"}, {"ownership_revision":1.5}, {"files":[]any{1}}, {"action":"discover_worktrees"}, {"action":"reclaim_worktree"}} {
  m := cloneGenericMap(valid); for k,v := range patch { m[k] = v }
  if _, err := parseManageWorkspaceArguments(encode(m)); err == nil { t.Fatalf("accepted invalid selection: %v", patch) }
 }
}
