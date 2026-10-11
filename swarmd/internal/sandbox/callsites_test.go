package sandbox

// Purpose: every daemon Git invocation on a repository agents can write must
// pass through RouteGit (directly or via Prepare/Command/RoutedArgv), or a
// planted hook, fsmonitor or filter runs as the daemon. This guard parses the
// daemon's production sources and fails when a function builds a `git`
// command without routing it, so a new call site cannot silently bypass the
// sandbox. It complements, and does not replace, the Docker suite, which
// proves routed commands are confined. Allowed exceptions are listed with the
// reason they never touch an agent-writable repository.

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

var unroutedGitAllowed = map[string]string{
	// Reads the daemon user's own global identity; no repository involved.
	"sandbox/sandbox.go:configureGitIdentity": "global config only",
	// Builds the command that RoutedArgv itself routes.
	"sandbox/git.go:RoutedArgv": "is the router",
	"sandbox/git.go:Command":    "is the router",
}

func TestEveryDaemonGitCommandIsRouted(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			buildsGit, routes := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				if pkg == nil {
					return true
				}
				if pkg.Name == "sandbox" && (sel.Sel.Name == "Prepare" || sel.Sel.Name == "Command" || sel.Sel.Name == "RouteGit") {
					routes = true
				}
				if strings.HasPrefix(rel, "sandbox"+string(filepath.Separator)) && (sel.Sel.Name == "RouteGit" || sel.Sel.Name == "Prepare") {
					routes = true
				}
				if pkg.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
					for _, arg := range call.Args {
						if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, _ := strconv.Unquote(lit.Value); v == "git" {
								buildsGit = true
							}
						}
					}
				}
				return true
			})
			if buildsGit && !routes {
				key := filepath.ToSlash(rel) + ":" + fn.Name.Name
				if _, ok := unroutedGitAllowed[key]; !ok {
					violations = append(violations, key)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("git commands built without sandbox routing:\n  %s", strings.Join(violations, "\n  "))
	}
}
