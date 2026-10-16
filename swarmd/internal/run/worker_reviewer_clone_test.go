package run

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Requirement: resolveReviewTarget must query ancestry in the authenticated job
// checkout, because the source folder does not contain unlanded clone commits.
// This call-site regression is the narrowest check of argument routing; it is
// not evidence of runtime authorization or successful end-to-end review.
func TestReviewerAncestryUsesJobClone(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "worker_reviewer.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || method.Sel.Name != "TaskCommitDescendsFrom" {
			return true
		}
		calls++
		if len(call.Args) != 3 {
			t.Fatal("unexpected ancestry signature")
		}
		path, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok || path.Sel.Name != "WorkspacePath" {
			t.Fatal("review ancestry must use task.WorkspacePath, not the source folder")
		}
		owner, ok := path.X.(*ast.Ident)
		if !ok || owner.Name != "task" {
			t.Fatal("review ancestry must use the authenticated task checkout")
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("expected one ancestry boundary, got %d", calls)
	}
}
