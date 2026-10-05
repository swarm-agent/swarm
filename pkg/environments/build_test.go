package environments

import (
	"strings"
	"testing"
)

// Purpose: ImageBuildDefinition is the canonical closed source/path boundary.
// Domain tests prove mutable refs, traversal, raw paths and missing generation
// cannot enter build definitions; digest tests fence idempotent input changes.
func TestManagedBuildDefinitionValidation(t *testing.T) {
	good := ImageBuildDefinition{Product: CommittedBuildSource{WorkspaceID: "product", WorkspaceGeneration: 1, Commit: strings.Repeat("a", 40)}, Recipe: CommittedBuildSource{WorkspaceID: "recipe", WorkspaceGeneration: 1, Commit: strings.Repeat("b", 40)}, RecipeDirectory: "recipe", RecipeFile: "recipe/Containerfile"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ImageBuildDefinition){
		func(b *ImageBuildDefinition) { b.Product.Commit = "dev" },
		func(b *ImageBuildDefinition) { b.Product.WorkspaceGeneration = 0 },
		func(b *ImageBuildDefinition) { b.Recipe.WorkspaceID = "" },
		func(b *ImageBuildDefinition) { b.RecipeDirectory = "../recipe" },
		func(b *ImageBuildDefinition) { b.RecipeFile = "/host/Containerfile" },
		func(b *ImageBuildDefinition) { b.RecipeFile = "recipe/../Containerfile" },
		func(b *ImageBuildDefinition) { b.RecipeFile = "recipe/Containerfile.in" },
		func(b *ImageBuildDefinition) { b.RecipeFile = ".swarm-recipe/inject" },
	} {
		bad := good
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid definition accepted: %+v", bad)
		}
	}
	other := good
	other.Product.Commit = strings.Repeat("c", 40)
	if good.Digest() == other.Digest() {
		t.Fatal("changed source reused digest")
	}
	a := ComputeOperationRequestHash(OperationRequestHashInput{Action: OperationActionBuild, BuildDigest: good.Digest()})
	b := ComputeOperationRequestHash(OperationRequestHashInput{Action: OperationActionBuild, BuildDigest: other.Digest()})
	if a == b {
		t.Fatal("changed source reused request hash")
	}
}
