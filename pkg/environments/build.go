package environments

import (
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "path"
 "regexp"
 "strings"
)

const ManagedBuildImage = "managed-build"

// CommittedBuildSource names an account-authorized catalog repository, never a raw host path.
// Generation fences catalog replacement; Commit is an exact object, not a mutable ref.
type CommittedBuildSource struct {
 WorkspaceID string `json:"workspace_id"`
 WorkspaceGeneration int64 `json:"workspace_generation"`
 Commit string `json:"commit"`
}

type ImageBuildDefinition struct {
 Product CommittedBuildSource `json:"product"`
 Recipe CommittedBuildSource `json:"recipe"`
 RecipeFile string `json:"recipe_file"`
 // Recipe files are placed under .swarm-recipe; product files remain at context root.
 RecipeDirectory string `json:"recipe_directory"`
}

// ImageBuildResult is provider-observed provenance, stored only in operation/deployment records.
type ImageBuildResult struct {
 OperationID string `json:"operation_id"`
 ConnectionID string `json:"connection_id"`
 DefinitionDigest string `json:"definition_digest"`
 ImageID string `json:"image_id"`
 ContextDigest string `json:"context_digest"`
 Product CommittedBuildSource `json:"product"`
 Recipe CommittedBuildSource `json:"recipe"`
 RecipeFile string `json:"recipe_file"`
}

var exactCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var exactImagePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidBuildImageID(id string) bool { return exactImagePattern.MatchString(id) }

func ValidBuildPath(p string) bool {
 if p == "" || len(p) > 1024 || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00\r\n") || p == "." { return false }
 for _, part := range strings.Split(p, "/") { if part == ".." || part == ".git" || part == ".swarm-recipe" { return false } }
 return !strings.HasPrefix(p, "-")
}

func (b *ImageBuildDefinition) Validate() error {
 if b == nil { return errors.New("build definition is required") }
 for _, s := range []CommittedBuildSource{b.Product, b.Recipe} {
  if s.WorkspaceID == "" || len(s.WorkspaceID) > maxIDBytes || s.WorkspaceGeneration < 1 || !exactCommitPattern.MatchString(s.Commit) { return errors.New("build sources require workspace_id, current workspace_generation and full lowercase commit OID") }
 }
 if !ValidBuildPath(b.RecipeDirectory) || !ValidBuildPath(b.RecipeFile) || !strings.HasPrefix(b.RecipeFile, b.RecipeDirectory+"/") { return errors.New("recipe_file must be a regular file inside recipe_directory") }
 if strings.HasSuffix(b.RecipeFile, ".in") { return errors.New("preprocessed container recipes are not supported") }
 return nil
}

func (b *ImageBuildDefinition) Digest() string {
 raw, _ := json.Marshal(b)
 sum := sha256.Sum256(raw)
 return hex.EncodeToString(sum[:])
}
