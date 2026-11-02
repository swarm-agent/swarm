package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// This file derives what an HTTP route ACCEPTS from the route's own source:
// the query parameters its handler (and the readers it hands the request or
// query to) reads, and the JSON tags of the struct it decodes the body into
// (or the keys it reads from a decoded map). The parity test then compares
// those fields with the MCP action's declared inputs, so a route field the
// action lacks, or an action input the route ignores, fails even when the
// forwarder never sends it.
//
// The route a linked action reaches is observed on the wire (the forwarder's
// own dispatch); which handler branch serves that route shape is named in
// mcpRouteSpecs, the one hand-written binding. A route shape an action
// reaches with no entry fails the test.

// mcpRouteSpec names the source that serves one route shape.
type mcpRouteSpec struct {
	// Catalog: the route runs the catalog action's own handler
	// (serveCatalogLinked or ManageWorkflows on the decoded body), which
	// takes exactly the action's declared inputs. Reason says which.
	Catalog string
	// Func is the handler (a method or function of package api); Anchors
	// narrow it to the branch that serves the shape: each is matched
	// against an if condition or case expression nested in the previous
	// match (a leading "=" asks for the whole condition).
	Func    string
	Anchors []string
}

// mcpRouteSpecs bind each route shape a linked MCP action reaches to its
// handler branch. Path segments sent from an input ({id}) are normalized.
var mcpRouteSpecs = map[string]mcpRouteSpec{
	"GET /v3/projects":                             {Func: "handleProjects", Anchors: []string{`=trimmed == ""`, `=r.Method == http.MethodGet`}},
	"GET /v3/projects/{id}":                        {Func: "handleProjects", Anchors: []string{`=len(segments) == 1`, `=r.Method == http.MethodGet`}},
	"GET /v3/projects/{id}/tasks":                  {Func: "handleProjects", Anchors: []string{`=len(segments) == 2 && segments[1] == "tasks"`, `=r.Method == http.MethodGet`}},
	"POST /v3/projects/{id}/tasks":                 {Func: "handleProjects", Anchors: []string{`=len(segments) == 2 && segments[1] == "tasks"`, `=r.Method == http.MethodPost`}},
	"GET /v3/projects/{id}/tasks/{id}":             {Func: "handleProjects", Anchors: []string{`=len(segments) == 3 && segments[1] == "tasks"`, `=r.Method == http.MethodGet`}},
	"POST /v3/projects/{id}/tasks/{id}/accept":     {Func: "handleProjects", Anchors: []string{`segments[3] == "accept"`}},
	"POST /v3/projects/{id}/tasks/{id}/approve":    {Func: "handleProjects", Anchors: []string{`segments[3] == "approve"`}},
	"POST /v3/projects/{id}/tasks/{id}/archive":    {Func: "handleProjects", Anchors: []string{`segments[3] == "archive"`}},
	"POST /v3/projects/{id}/tasks/{id}/reopen":     {Func: "handleProjects", Anchors: []string{`segments[3] == "reopen"`}},
	"POST /v3/projects/{id}/tasks/{id}/message":    {Func: "handleProjectTaskControl"},
	"POST /v3/projects/{id}/tasks/{id}/stop":       {Func: "handleProjectTaskControl", Anchors: []string{`=op == "stop"`}},
	"POST /v3/projects/{id}/tasks/{id}/refine":     {Func: "handleProjects", Anchors: []string{`segments[3] == "refine"`}},
	"GET /v3/projects/{id}/sources":                {Func: "handleProjectSources", Anchors: []string{`=len(rest) == 0 && r.Method == http.MethodGet`}},
	"GET /v3/projects/{id}/sources/inspect":        {Func: "handleProjectSources", Anchors: []string{`=len(rest) == 1 && rest[0] == projectSourceInspectSegment && r.Method == http.MethodGet`}},
	"POST /v3/projects/{id}/sources/inspect-files": {Func: "handleProjectSources", Anchors: []string{`=len(rest) == 1 && rest[0] == projectSourceInspectFilesSegmt && r.Method == http.MethodPost`}},
	"GET /v3/workflows":                            {Func: "handleJobAutomations", Anchors: []string{`=rest == "" && r.Method == http.MethodGet`}},
	"GET /v3/workflows/{id}":                       {Func: "handleJobAutomations", Anchors: []string{`=rest != "" && !strings.Contains(rest, "/") && r.Method == http.MethodGet`}},
	"DELETE /v3/workflows/{id}":                    {Func: "handleJobAutomations", Anchors: []string{`=!strings.Contains(rest, "/") && r.Method == http.MethodDelete`}},
	"GET /v3/workflows/history":                    {Func: "handleWorkflowHistory", Anchors: []string{`=action == "history"`}},
	"GET /v3/job-deliveries":                       {Func: "handleJobDeliveries", Anchors: []string{`=rest == "" && r.Method == http.MethodGet`}},
	"POST /v3/workflows/stop":                      {Func: "handleJobAutomations", Anchors: []string{`=rest == "stop" && r.Method == http.MethodPost`}},
	"POST /v3/workflows/export":                    {Func: "handleWorkflowTransfer"},
	"POST /v3/workflows/propose":                   {Catalog: "the route decodes the body as the action's arguments and runs ManageWorkflows, the action's own handler"},
	"POST /v3/workflows/agent/import":              {Catalog: "the route decodes the body as the action's arguments and runs ManageWorkflows, the action's own handler"},
	"POST /v3/workflows/agent/restore":             {Catalog: "the route decodes the body as the action's arguments and runs ManageWorkflows, the action's own handler"},
	"POST /v3/workflows/{id}/enable":               {Catalog: "the route decodes the body as the action's arguments and runs ManageWorkflows, the action's own handler"},
	"POST /v3/workflows/{id}/disable":              {Catalog: "the route decodes the body as the action's arguments and runs ManageWorkflows, the action's own handler"},
}

// mcpCatalogRoutePrefixes are routes that serve a whole catalog tool through
// serveCatalogLinked: the route takes the action's declared inputs and
// refuses others ("does not take"), so it accepts exactly what the action
// declares.
var mcpCatalogRoutePrefixes = []string{CatalogActionsPath + "/", CatalogLinkedPath + "/", SetupActionsPath + "/", WorkerActionsPath + "/", JobsPath + "/"}

// mcpRouteShape normalizes a request to its route shape.
var mcpRouteIDSegment = regexp.MustCompile(`/pf-[^/]*`)

func mcpRouteShape(method, path string) string {
	return method + " " + mcpRouteIDSegment.ReplaceAllString(path, "/{id}")
}

// mcpRouteSource is package api's parsed non-test source.
type mcpRouteSource struct {
	fset  *token.FileSet
	funcs map[string][]*ast.FuncDecl
	types map[string]ast.Expr
	vars  map[string][]string
}

var (
	mcpRouteSourceOnce sync.Once
	mcpRouteSourceVal  *mcpRouteSource
	mcpRouteSourceErr  error
)

func loadMCPRouteSource() (*mcpRouteSource, error) {
	mcpRouteSourceOnce.Do(func() {
		src := &mcpRouteSource{fset: token.NewFileSet(), funcs: map[string][]*ast.FuncDecl{}, types: map[string]ast.Expr{}, vars: map[string][]string{}}
		files, err := filepath.Glob("*.go")
		if err != nil {
			mcpRouteSourceErr = err
			return
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(src.fset, name, nil, parser.SkipObjectResolution)
			if err != nil {
				mcpRouteSourceErr = err
				return
			}
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					src.funcs[d.Name.Name] = append(src.funcs[d.Name.Name], d)
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							src.types[s.Name.Name] = s.Type
						case *ast.ValueSpec:
							for i, ident := range s.Names {
								if i < len(s.Values) {
									if lit, ok := s.Values[i].(*ast.CompositeLit); ok {
										src.vars[ident.Name] = mcpStringLits(lit.Elts)
									}
								}
							}
						}
					}
				}
			}
		}
		mcpRouteSourceVal = src
	})
	return mcpRouteSourceVal, mcpRouteSourceErr
}

func mcpStringLits(exprs []ast.Expr) []string {
	var out []string
	for _, e := range exprs {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, v)
			}
		}
	}
	return out
}

func (src *mcpRouteSource) text(node ast.Node) string {
	var b strings.Builder
	_ = printer.Fprint(&b, src.fset, node)
	return b.String()
}

// mcpRouteFields are the fields a route accepts, with what could not be
// resolved (a decode target of another package).
type mcpRouteFields struct {
	Query, Body map[string]bool
	Unresolved  []string
}

// mcpRouteRoles are the parameters of a scanned function that carry the
// request, its query, a query getter or a decoded map.
type mcpRouteRoles struct {
	request, query, getter, bodyMap map[string]bool
}

func newMCPRouteRoles() mcpRouteRoles {
	return mcpRouteRoles{request: map[string]bool{}, query: map[string]bool{}, getter: map[string]bool{}, bodyMap: map[string]bool{}}
}

// accepts derives the fields of a spec.
func (src *mcpRouteSource) accepts(spec mcpRouteSpec) (mcpRouteFields, error) {
	decls := src.funcs[spec.Func]
	if len(decls) != 1 {
		return mcpRouteFields{}, fmt.Errorf("handler %s: %d declarations", spec.Func, len(decls))
	}
	decl := decls[0]
	var scope ast.Node = decl.Body
	for _, anchor := range spec.Anchors {
		found := src.anchor(scope, anchor)
		if found == nil {
			return mcpRouteFields{}, fmt.Errorf("handler %s: no branch %q", spec.Func, anchor)
		}
		scope = found
	}
	out := mcpRouteFields{Query: map[string]bool{}, Body: map[string]bool{}}
	roles := src.paramRoles(decl, nil)
	src.scan(decl, scope, roles, &out, map[string]bool{spec.Func: true}, 0)
	return out, nil
}

// anchor is the first if body or case clause under scope whose condition
// matches (contains, or with "=" equals) the anchor; else blocks count.
func (src *mcpRouteSource) anchor(scope ast.Node, anchor string) ast.Node {
	exact := strings.HasPrefix(anchor, "=")
	want := strings.TrimPrefix(anchor, "=")
	match := func(cond ast.Expr) bool {
		got := src.text(cond)
		if exact {
			return got == want
		}
		return strings.Contains(got, want)
	}
	var found ast.Node
	ast.Inspect(scope, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		switch s := n.(type) {
		case *ast.IfStmt:
			if match(s.Cond) {
				found = s.Body
				return false
			}
		case *ast.CaseClause:
			for _, e := range s.List {
				if match(e) {
					found = s
					return false
				}
			}
		}
		return true
	})
	return found
}

// paramRoles maps a function's parameters to roles: by type, or by the role
// of the argument the caller passed in that position.
func (src *mcpRouteSource) paramRoles(decl *ast.FuncDecl, argRoles []string) mcpRouteRoles {
	roles := newMCPRouteRoles()
	index := 0
	for _, field := range decl.Type.Params.List {
		typ := src.text(field.Type)
		names := field.Names
		if len(names) == 0 {
			index++
			continue
		}
		for _, name := range names {
			role := ""
			if index < len(argRoles) {
				role = argRoles[index]
			}
			switch {
			case typ == "*http.Request":
				roles.request[name.Name] = true
			case typ == "url.Values" || role == "query":
				roles.query[name.Name] = true
			case role == "getter":
				roles.getter[name.Name] = true
			case role == "bodyMap":
				roles.bodyMap[name.Name] = true
			}
			index++
		}
	}
	return roles
}

// role is what an expression carries: the request, its query, a query
// getter or a decoded body map.
func (src *mcpRouteSource) role(e ast.Expr, roles mcpRouteRoles) string {
	switch x := e.(type) {
	case *ast.Ident:
		switch {
		case roles.request[x.Name]:
			return "request"
		case roles.query[x.Name]:
			return "query"
		case roles.getter[x.Name]:
			return "getter"
		case roles.bodyMap[x.Name]:
			return "bodyMap"
		}
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Query" && src.role(mcpRouteBase(sel.X), roles) == "request" {
			return "query"
		}
	case *ast.SelectorExpr:
		if x.Sel.Name == "Get" && src.role(x.X, roles) == "query" {
			return "getter"
		}
	}
	return ""
}

// mcpRouteBase is the root identifier of r.URL (r).
func mcpRouteBase(e ast.Expr) ast.Expr {
	for {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return e
		}
		e = sel.X
	}
}

// scan collects the fields read in scope (a node of decl) and follows calls
// that are handed the request, its query, a getter or a decoded map.
func (src *mcpRouteSource) scan(decl *ast.FuncDecl, scope ast.Node, roles mcpRouteRoles, out *mcpRouteFields, seen map[string]bool, depth int) {
	// Local query idents and getter idents (q := r.URL.Query()), and raw
	// bodies (body, ok := readProjectMediaRequest(w, r)).
	raw := map[string]bool{}
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(assign.Rhs) == 1 {
			if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && src.role(call, roles) == "" {
				for _, arg := range call.Args {
					if src.carriesBody(arg, roles, nil) {
						if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
							raw[ident.Name] = true
						}
					}
				}
			}
		}
		if len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for i, lhs := range assign.Lhs {
			if ident, ok := lhs.(*ast.Ident); ok {
				switch src.role(assign.Rhs[i], roles) {
				case "query":
					roles.query[ident.Name] = true
				case "getter":
					roles.getter[ident.Name] = true
				}
			}
		}
		return true
	})
	// Decode targets: &x passed to a decoder.
	decoded := map[string]bool{}
	ast.Inspect(scope, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// A decoder of the request: handed the request (decodeJSON(r,
		// &x), a reader helper) or its raw body (json.Unmarshal(body, &x)).
		// A re-encoded value (json.Unmarshal(data, &args)) is not the body.
		handed := false
		name := ""
		switch f := call.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			name = f.Sel.Name
		}
		lower := strings.ToLower(name)
		reader := strings.Contains(lower, "decode") || strings.Contains(lower, "read") || strings.Contains(lower, "json") || name == "Unmarshal"
		for _, arg := range call.Args {
			if reader && src.carriesBody(arg, roles, raw) {
				handed = true
			}
		}
		// json.NewDecoder(r.Body).Decode(&x), decoder.Decode(&x).
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Decode" {
			switch x := sel.X.(type) {
			case *ast.Ident:
				handed = handed || raw[x.Name]
			case *ast.CallExpr:
				for _, arg := range x.Args {
					handed = handed || src.carriesBody(arg, roles, raw)
				}
			}
		}
		if !handed {
			return true
		}
		for _, arg := range call.Args {
			if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
				if ident, ok := unary.X.(*ast.Ident); ok {
					decoded[ident.Name] = true
				}
			}
		}
		return true
	})
	for name := range decoded {
		typ := src.localType(scope, name)
		if typ == nil {
			typ = src.localType(decl.Body, name)
		}
		if typ == nil {
			// A parameter: a helper's destination, decoded by its caller's type.
			for _, field := range decl.Type.Params.List {
				for _, ident := range field.Names {
					if ident.Name == name {
						typ = field.Type
					}
				}
			}
		}
		if text := src.text(typ); typ != nil && (text == "any" || text == "interface{}") {
			// A helper's own destination (a trailing-data check, a
			// caller-typed dst): the caller's &x names the fields.
			continue
		}
		switch t := typ.(type) {
		case nil:
			out.Unresolved = append(out.Unresolved, decl.Name.Name+": decode target "+name+" has no local type")
		case *ast.MapType:
			roles.bodyMap[name] = true
		default:
			if local := src.localTypeDecl(decl.Body, t); local != nil {
				t = local
			}
			if foreign, ok := mcpRouteForeignTypes[src.text(t)]; ok {
				mcpReflectTags(foreign, out.Body)
				continue
			}
			if !src.structTags(t, out.Body, 0) {
				out.Unresolved = append(out.Unresolved, decl.Name.Name+": decode target "+name+" is "+src.text(t))
			}
		}
	}
	ast.Inspect(scope, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			// q.Get("k"), q.Has("k"), get("k"), r.FormValue("k").
			if len(x.Args) == 1 {
				if keys := mcpStringLits(x.Args); len(keys) == 1 {
					switch f := x.Fun.(type) {
					case *ast.SelectorExpr:
						if (f.Sel.Name == "Get" || f.Sel.Name == "Has") && src.role(f.X, roles) == "query" {
							out.Query[keys[0]] = true
						}
						if f.Sel.Name == "FormValue" && src.role(f.X, roles) == "request" {
							out.Query[keys[0]] = true
						}
					case *ast.Ident:
						if roles.getter[f.Name] {
							out.Query[keys[0]] = true
						}
					}
				}
			}
			src.follow(x, roles, out, seen, depth)
		case *ast.IndexExpr:
			if keys := mcpStringLits([]ast.Expr{x.Index}); len(keys) == 1 {
				switch src.role(x.X, roles) {
				case "bodyMap":
					out.Body[keys[0]] = true
				case "query":
					out.Query[keys[0]] = true
				}
			}
		case *ast.RangeStmt:
			// for _, key := range projectTaskListParams { q.Has(key) } and
			// for _, key := range []string{"id", "name"} { r.URL.Query().Get(key) }
			var keys []string
			switch over := x.X.(type) {
			case *ast.Ident:
				keys = src.vars[over.Name]
			case *ast.CompositeLit:
				keys = mcpStringLits(over.Elts)
			}
			value, _ := x.Value.(*ast.Ident)
			if len(keys) == 0 || value == nil {
				return true
			}
			ast.Inspect(x.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				arg, ok := call.Args[0].(*ast.Ident)
				if !ok || arg.Name != value.Name {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Get" || sel.Sel.Name == "Has") && src.role(sel.X, roles) == "query" {
					for _, key := range keys {
						out.Query[key] = true
					}
				}
				return true
			})
		}
		return true
	})
}

// follow scans a same-package function the call hands a role to.
func (src *mcpRouteSource) follow(call *ast.CallExpr, roles mcpRouteRoles, out *mcpRouteFields, seen map[string]bool, depth int) {
	if depth >= 4 {
		return
	}
	argRoles := make([]string, len(call.Args))
	any := false
	for i, arg := range call.Args {
		if argRoles[i] = src.role(arg, roles); argRoles[i] != "" {
			any = true
		}
	}
	if !any {
		return
	}
	name := ""
	switch f := call.Fun.(type) {
	case *ast.Ident:
		name = f.Name
	case *ast.SelectorExpr:
		if _, ok := f.X.(*ast.Ident); ok {
			name = f.Sel.Name
		}
	}
	decls := src.funcs[name]
	if name == "" || seen[name] || len(decls) != 1 || mcpRouteResponders[name] {
		return
	}
	seen[name] = true
	callee := decls[0]
	src.scan(callee, callee.Body, src.paramRoles(callee, argRoles), out, seen, depth+1)
}

// mcpRouteResponders are helpers handed the request that read no input
// field: auth and scope checks and answer writers.
var mcpRouteResponders = map[string]bool{"requireScope": true, "requireScopeAny": true, "writeError": true, "writeJSON": true}

// carriesBody reports whether the expression is the request, its body or a
// raw body read from it.
func (src *mcpRouteSource) carriesBody(e ast.Expr, roles mcpRouteRoles, raw map[string]bool) bool {
	if ident, ok := e.(*ast.Ident); ok && raw[ident.Name] {
		return true
	}
	if src.role(e, roles) == "request" {
		return true
	}
	// A size-limited body (http.MaxBytesReader(w, r.Body, n),
	// io.LimitReader(r.Body, n)) is still the body.
	if call, ok := e.(*ast.CallExpr); ok {
		if fn, ok := call.Fun.(*ast.SelectorExpr); ok && (fn.Sel.Name == "MaxBytesReader" || fn.Sel.Name == "LimitReader") {
			for _, arg := range call.Args {
				if src.carriesBody(arg, roles, raw) {
					return true
				}
			}
		}
	}
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Body" && src.role(sel.X, roles) == "request"
}

// localType is the declared type of a local variable in body.
func (src *mcpRouteSource) localType(body ast.Node, name string) ast.Expr {
	var typ ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		if typ != nil {
			return false
		}
		switch x := n.(type) {
		case *ast.ValueSpec:
			for _, ident := range x.Names {
				if ident.Name == name && x.Type != nil {
					typ = x.Type
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == name && i < len(x.Rhs) && x.Tok == token.DEFINE {
					if lit, ok := x.Rhs[i].(*ast.CompositeLit); ok {
						typ = lit.Type
					}
				}
			}
		}
		return true
	})
	return typ
}

// structTags adds the JSON names of a struct type (named in this package or
// inline, with embedded structs) and reports whether it was resolved.
func (src *mcpRouteSource) structTags(typ ast.Expr, into map[string]bool, depth int) bool {
	if depth > 4 {
		return false
	}
	switch t := typ.(type) {
	case *ast.Ident:
		named, ok := src.types[t.Name]
		if !ok {
			return false
		}
		return src.structTags(named, into, depth+1)
	case *ast.StarExpr:
		return src.structTags(t.X, into, depth+1)
	case *ast.StructType:
		for _, field := range t.Fields.List {
			tag := ""
			if field.Tag != nil {
				raw, _ := strconv.Unquote(field.Tag.Value)
				tag = reflect.StructTag(raw).Get("json")
			}
			name := strings.Split(tag, ",")[0]
			if name == "-" {
				continue
			}
			if len(field.Names) == 0 && name == "" {
				if !src.structTags(field.Type, into, depth+1) {
					return false
				}
				continue
			}
			if name == "" {
				for _, n := range field.Names {
					into[n.Name] = true
				}
				continue
			}
			into[name] = true
		}
		return true
	}
	return false
}

// localTypeDecl is the struct a type declared inside the function names.
func (src *mcpRouteSource) localTypeDecl(body ast.Node, typ ast.Expr) ast.Expr {
	ident, ok := typ.(*ast.Ident)
	if !ok {
		return nil
	}
	var found ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		if spec, ok := n.(*ast.TypeSpec); ok && spec.Name.Name == ident.Name {
			found = spec.Type
		}
		return found == nil
	})
	return found
}

// mcpRouteForeignTypes are decode targets declared in another package, read
// by reflection (their JSON tags are the route's body fields).
var mcpRouteForeignTypes = map[string]reflect.Type{
	"tool.ProjectTaskApprovalGuards": reflect.TypeOf(tool.ProjectTaskApprovalGuards{}),
	"pebblestore.ProjectRecord":      reflect.TypeOf(pebblestore.ProjectRecord{}),
}

// mcpReflectTags adds a struct type's JSON names, with embedded structs.
func mcpReflectTags(typ reflect.Type, into map[string]bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		switch {
		case name == "-":
		case field.Anonymous && name == "":
			mcpReflectTags(field.Type, into)
		case name == "":
			into[field.Name] = true
		default:
			into[name] = true
		}
	}
}

// mcpRouteAcceptsCache holds derived fields by route shape.
var mcpRouteAcceptsCache sync.Map

func mcpRouteAccepts(shape string) (mcpRouteSpec, mcpRouteFields, bool, error) {
	spec, ok := mcpRouteSpecs[shape]
	if !ok {
		spec, ok = mcpHomeRouteSpecs[shape]
	}
	if !ok {
		spec, ok = mcpControlRouteSpecs[shape]
	}
	if !ok {
		return spec, mcpRouteFields{}, false, nil
	}
	if spec.Catalog != "" {
		return spec, mcpRouteFields{}, true, nil
	}
	if cached, ok := mcpRouteAcceptsCache.Load(shape); ok {
		return spec, cached.(mcpRouteFields), true, nil
	}
	src, err := loadMCPRouteSource()
	if err != nil {
		return spec, mcpRouteFields{}, true, err
	}
	fields, err := src.accepts(spec)
	if err == nil {
		mcpRouteAcceptsCache.Store(shape, fields)
	}
	return spec, fields, true, err
}

// mcpIsCatalogRoute reports whether the path is a catalog tool's route.
func mcpIsCatalogRoute(path string) bool {
	for _, prefix := range mcpCatalogRoutePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Purpose:
//   - Requirement: the route-field derivation the parity test relies on reads
//     the real handler source: the task-list route's accepted query fields
//     include every name in projectTaskListParams plus view, worker_only
//     and worker_id, and each spec's branch is still found.
//   - Threat/regression: an anchor that no longer matches (a handler
//     refactor) or a reader the walker cannot follow would make the parity
//     comparison vacuous: an empty accepted set reports nothing.
//   - Boundary/authority: the handlers named in mcpRouteSpecs.
//   - Narrowest layer: the source the route runs.
func TestMCPRouteParityRouteAcceptsDerivation(t *testing.T) {
	var problems []string
	shapes := append(mcpParitySortedKeys(mcpRouteSpecs), mcpParitySortedKeys(mcpHomeRouteSpecs)...)
	shapes = append(shapes, mcpParitySortedKeys(mcpControlRouteSpecs)...)
	for _, shape := range shapes {
		spec, fields, _, err := mcpRouteAccepts(shape)
		if err != nil {
			problems = append(problems, shape+": "+err.Error())
			continue
		}
		if spec.Catalog == "" && len(fields.Unresolved) > 0 {
			problems = append(problems, shape+": "+strings.Join(fields.Unresolved, "; "))
		}
	}
	_, list, _, err := mcpRouteAccepts("GET /v3/projects/{id}/tasks")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range append(append([]string(nil), projectTaskListParams...), "view", "worker_only", "worker_id") {
		if !list.Query[key] {
			problems = append(problems, "GET /v3/projects/{id}/tasks: derived query fields miss "+key+": "+strings.Join(mcpParitySortedKeys(list.Query), ","))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("route field derivation:\n%s", strings.Join(problems, "\n"))
	}
}

// mcpRouteRegistration is one mux registration: its pattern and handler as
// written.
type mcpRouteRegistration struct{ Pattern, Handler string }

// mcpRouteRegistrations are every route package api registers (mux.Handle
// and mux.HandleFunc in its non-test source).
func mcpRouteRegistrations(t *testing.T) []mcpRouteRegistration {
	t.Helper()
	src, err := loadMCPRouteSource()
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("*.go")
	var out []mcpRouteRegistration
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "mux.Handle") {
			continue
		}
		file, err := parser.ParseFile(src.fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") || src.text(sel.X) != "mux" {
				return true
			}
			out = append(out, mcpRouteRegistration{Pattern: src.text(call.Args[0]), Handler: src.text(call.Args[1])})
			return true
		})
	}
	return out
}

// mcpRouteOnlyGroup is a set of route fields no MCP action sends, with why.
type mcpRouteOnlyGroup struct{ Fields, Reason string }

// mcpRouteParityRouteOnly are route fields (by route shape) that the MCP
// actions reaching the route deliberately do not declare. A listed field
// the route no longer reads, or no action meets, fails the test.
var mcpRouteParityRouteOnly = map[string][]mcpRouteOnlyGroup{
	"POST /v3/projects/{id}/tasks": {
		{Fields: "worker_id worker_name worker_run_id automation_id", Reason: "server-managed receipts: the route refuses them from any client"},
		{Fields: "id status revision git_status unintegrated_commits diff_summary is_dirty action_needed what_did_do what_not_done pipeline_stages current_stage_index workspaces_involved plan_summary full_plan_markdown last_error worktree_branch deploy_session task_program task_program_id document plan_document coder_assignments deliverables tier outcome_type intent auto_approve", Reason: "task-record fields the Desktop board and plan flows write when they create a task (state, plan, program, delivery facts); an MCP caller creates a task from its declared inputs and the daemon fills these"},
		{Fields: "speech operation aspect_ratio resolution duration_seconds video_type enhance_prompt variant_count deliverable_count scenes scenes_count soundtrack", Reason: "media-task settings of the Desktop's media create form; MCP media requests are manage_media's"},
		{Fields: "model provider thinking service_tier context_mode", Reason: "TODO: a per-task model override the Desktop sends; create_task declares none (propose_task declares model), so a linked create_task always runs on the account's agent-model settings"},
	},
	"POST /v3/projects/{id}/tasks/{id}/reopen": {
		{Fields: "definition_revision plan_id session_id", Reason: "TODO: plan guards the Desktop's reopen sends; reopen_task declares none"},
		{Fields: "cursor limit", Reason: "the attempt-history page of the history route, which the same handler branch serves (segments[3] == \"history\"); reopen takes no page"},
	},
	"POST /v3/projects/{id}/tasks/{id}/refine": {
		{Fields: "agent error_summary feature_size model outcome_type tier", Reason: "the Desktop re-plan form's re-plan settings; refine_task sends feedback, expected_revision and the plan guards, and a plan document is refused on a linked machine"},
	},
	"GET /v3/projects": {
		{Fields: "limit", Reason: "TODO: list declares no limit; the linked list is bounded by compactFleetList"},
	},
	"GET /v3/projects/{id}/tasks": {
		{Fields: "origin_session_id", Reason: "the Desktop's filter for the tasks one session created; an MCP caller lists a project, and a linked caller has no session on that machine"},
		{Fields: "worker_only", Reason: "the Desktop worker board's any-worker filter; worker_id selects one worker's tasks"},
		{Fields: "view", Reason: "view=archived selects archived tasks only; include_archived is the action's input (its linked mapping is the include_archived TODO in mcpRouteParityAllow)"},
	},
	"DELETE /v3/workflows/{id}": {
		{Fields: "expected_revision", Reason: "TODO: deleting one exact revision is the person's proposal discard in the Desktop; manage_workflows delete declares no expected_revision"},
	},
	"GET /v3/workflows": {
		{Fields: "detail", Reason: "the workflow list's detail; deliveries with delivery_id reads one delivery through this route and declares no detail"},
	},
	"GET /v3/job-deliveries": {
		{Fields: "limit", Reason: "the forwarder asks for one page of 50 (mcpRouteParityAllow deliveries limit)"},
		{Fields: "state", Reason: "TODO: the deliveries route filters by state; manage_workflows deliveries declares no state"},
	},
	"POST /v3/workflows/export": {
		{Fields: "definition project with workspace", Reason: "handleWorkflowTransfer decodes export and import with one body; these are import's fields (a linked import reaches the agent import route)"},
	},
}

func mcpRouteOnlyField(shape, field string) string {
	for _, group := range mcpRouteParityRouteOnly[shape] {
		for _, f := range strings.Fields(group.Fields) {
			if f == field {
				return group.Reason
			}
		}
	}
	return ""
}
