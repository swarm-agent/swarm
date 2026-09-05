package artifactv2

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Requirement: Artifact V2 writes are a new authority and may depend only on
// audited primitives. Threat: a convenient import, alias, wrapper, or string
// dispatch could restore V1 create/create_package or manage_artifact writes.
// AST/source inspection of the entire V2 package is the narrowest structural
// gate because those forbidden dependencies must not compile into this package.
func TestArtifactV2HasNoLegacyWriteDependency(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Clean(entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, forbidden := range []string{"internal/artifact\"", "runtime_manage_artifact", "manage_artifact", "create_package", "SessionArtifactVariant", "V3ArtifactMutation", "Authority.Create", "Authority.CreatePackage"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains forbidden V1 dependency %q", path, forbidden)
			}
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if importPath == "swarm/packages/swarmd/internal/artifact" || strings.HasPrefix(importPath, "swarm/packages/swarmd/internal/tool") {
				t.Errorf("%s imports forbidden V1 write package %q", path, importPath)
			}
			if spec.Name != nil && spec.Name.Name == "artifact" {
				t.Errorf("%s aliases an import as artifact", path)
			}
		}
	}
}

// Requirement: cutover registers one V2 managed Designer writer and retains V1
// only for explicitly non-Designer compatibility reads/image generation.
// Threat: orchestration, provider schema, or prompt drift could silently restore
// V1 create/create_package, placeholders, refinement loops, or manual video
// assembly without importing the artifact package into artifactv2 itself.
func TestArtifactV2CutoverHasNoLegacyDesignerWriteRegistration(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), ".."))
	checks := []struct {
		path      string
		forbidden []string
	}{
		{filepath.Join(root, "run", "designer_animation_guidance.go"), []string{"trusted manage_artifact publication call", "animation_inspection_references", "__SWARM_ANIMATION_BIND__", "swarm-player/v1 message listener"}},
		{filepath.Join(root, "run", "service_prompt.go"), []string{"one or more such parts atomically only through manage_video propose_html_iteration", "manage_artifact export_html_animation_fallback on one valid candidate"}},
	}
	for _, check := range checks {
		body, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range check.forbidden {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s retains forbidden managed Designer V1 contract %q", check.path, forbidden)
			}
		}
	}

	toolPath := filepath.Join(root, "tool", "runtime_manage_artifact.go")
	body, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), toolPath, body, 0)
	if err != nil {
		t.Fatal(err)
	}
	var actionEnum []string
	ast.Inspect(file, func(node ast.Node) bool {
		composite, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, element := range composite.Elts {
			kv, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.BasicLit)
			if !ok || strings.Trim(key.Value, `"`) != "enum" {
				continue
			}
			values, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				continue
			}
			candidate := make([]string, 0, len(values.Elts))
			for _, value := range values.Elts {
				literal, ok := value.(*ast.BasicLit)
				if !ok {
					continue
				}
				candidate = append(candidate, strings.Trim(literal.Value, `"`))
			}
			if containsString(candidate, "image_capabilities") && containsString(candidate, "read") {
				actionEnum = candidate
			}
		}
		return true
	})
	if len(actionEnum) == 0 {
		t.Fatal("manage_artifact action enum not found")
	}
	for _, retired := range []string{"create", "create_package"} {
		if containsString(actionEnum, retired) {
			t.Fatalf("manage_artifact still registers retired V1 action %q: %v", retired, actionEnum)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
