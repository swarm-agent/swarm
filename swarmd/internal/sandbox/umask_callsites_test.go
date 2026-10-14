package sandbox

// Purpose: Manager.execOnce and RouteGit must use the worker umask helper;
// only root image build steps may use the public image mask. Tool scripts must
// not reset their inherited mask. This AST guard is the
// narrowest registration-layer check against new raw engine exec paths. The
// tool's behavioral regression separately proves actual file modes.
import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func umaskViolations(file *ast.File, rel string) []string {
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		key := rel + ":" + fn.Name.Name
		// Root image setup/profile steps install tools for unrelated UIDs and
		// need 022. This exception cannot authorize worker or routed commands.
		imageBuild := key == "sandbox/environment.go:BuildEnvironment"
		rawExec, supervised, wrapped := false, false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					value, _ := strconv.Unquote(node.Value)
					if value == "exec" {
						rawExec = true
					}
				}
			case *ast.CallExpr:
				if id, ok := node.Fun.(*ast.Ident); ok {
					if id.Name == "withSandboxUmask" || (id.Name == "withImageBuildUmask" && imageBuild) {
						wrapped = true
					}
					if id.Name == "withImageBuildUmask" && !imageBuild {
						violations = append(violations, key+":image mask outside image build")
					}
				}
				if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ExecSupervised" {
					supervised = true
				}
			}
			return true
		})
		// Transport only carries provider supervisor/probe/cleanup argv; it cannot
		// construct worker payloads. initializeHome's exec is read-only stat; its
		// root helper has no project mount and only repairs volume ownership.
		allowed := key == "sandbox/sandbox.go:RunExec" || key == "sandbox/sandbox.go:RunExecCombined" || key == "sandbox/git_identity.go:initializeHome"
		if strings.HasPrefix(rel, "sandbox/") && (rawExec || supervised) && !wrapped && !allowed {
			violations = append(violations, key)
		}
		// Generic environment supervision does not execute project sandbox work.
		// Any new direct consumer elsewhere must use Manager.Exec instead.
		if supervised && !strings.HasPrefix(rel, "sandbox/") && key != "environments/provider/provider.go:executeSupervised" {
			violations = append(violations, key+":direct supervisor bypass")
		}
		// Check each direct engine exec separately: another wrapped call in the
		// same function cannot authorize a raw one.
		if strings.HasPrefix(rel, "sandbox/") && !allowed {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Run" && sel.Sel.Name != "RunCombined" && sel.Sel.Name != "RunWithIO") {
					return true
				}
				hasExec, hasWrapper := false, false
				ast.Inspect(call, func(child ast.Node) bool {
					if lit, ok := child.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						hasExec = hasExec || v == "exec"
					}
					if c, ok := child.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && (id.Name == "withSandboxUmask" || (id.Name == "withImageBuildUmask" && imageBuild)) {
							hasWrapper = true
						}
					}
					return true
				})
				if hasExec && !hasWrapper {
					violations = append(violations, key+":raw engine call")
				}
				return true
			})
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, _ := strconv.Unquote(lit.Value)
		if strings.Contains(value, "umask ") && rel != "sandbox/umask.go" {
			violations = append(violations, rel+":local umask override")
		}
		return true
	})
	return violations
}

func TestEverySandboxExecUsesUmaskHelper(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		violations = append(violations, umaskViolations(file, filepath.ToSlash(rel))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("sandbox umask bypasses:\n%s", strings.Join(violations, "\n"))
	}
}

// Purpose: the guard itself must reject new raw exec/supervised paths and inner
// mask resets, rather than passing vacuously; tiny parsed fixtures exercise its
// negative and accepted cases without modifying production sources.
func TestSandboxUmaskGuardRejectsBypasses(t *testing.T) {
	for _, tc := range []struct {
		src, rel string
		bad      bool
	}{
		{`package sandbox; func bypass(){ runner.Run(ctx, engine, "exec", name, "bash") }`, "sandbox/new.go", true},
		{`package sandbox; func bypass(){ provider.ExecSupervised(ctx, transport, name, req) }`, "sandbox/new.go", true},
		{"package tool; const script = `umask 022`", "tool/new.go", true},
		{`package sandbox; func mixed(){ runner.Run(ctx, engine, "exec", name, withSandboxUmask(command)); runner.Run(ctx, engine, "exec", name, "bash") }`, "sandbox/new.go", true},
		{`package sandbox; func safe(){ runner.Run(ctx, engine, "exec", name, withSandboxUmask(command)) }`, "sandbox/new.go", false},
		{`package sandbox; func BuildEnvironment(){ runner.Run(ctx, engine, "exec", name, withImageBuildUmask(command)) }`, "sandbox/environment.go", false},
		{`package sandbox; func BuildEnvironment(){ runner.Run(ctx, engine, "exec", name, withImageBuildUmask(command)); runner.Run(ctx, engine, "exec", name, "bash") }`, "sandbox/environment.go", true},
		{`package sandbox; func worker(){ runner.Run(ctx, engine, "exec", name, withImageBuildUmask(command)) }`, "sandbox/environment.go", true},
		{`package tool; func worker(){ withImageBuildUmask(command) }`, "tool/new.go", true},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", tc.src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(umaskViolations(file, tc.rel)) > 0; got != tc.bad {
			t.Errorf("guard=%v want=%v for %s", got, tc.bad, tc.src)
		}
	}
}
