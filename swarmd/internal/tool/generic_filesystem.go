package tool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// rootedWorkspacePath binds a requested path to an opened allowed root. All
// filesystem operations must use root and relative together; absolutePath is
// only for display and for integrations that require a pathname.
type rootedWorkspacePath struct {
	root         *os.Root
	relative     string
	absolutePath string
	mutable      bool
}

func openRootedWorkspacePath(scope WorkspaceScope, requested string) (*rootedWorkspacePath, error) {
	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is empty")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil, errors.New("path is required")
	}

	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspacePath, candidate)
	}
	candidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return nil, fmt.Errorf("resolve target path: %w", err)
	}

	var selectedRoot, selectedRelative string
	for _, allowedRoot := range resolveAllowedRoots(scope) {
		allowedRoot, err = filepath.Abs(filepath.Clean(strings.TrimSpace(allowedRoot)))
		if err != nil || allowedRoot == "" {
			continue
		}
		relative, relErr := filepath.Rel(allowedRoot, candidate)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if selectedRoot == "" || len(allowedRoot) > len(selectedRoot) {
			selectedRoot = allowedRoot
			selectedRelative = relative
		}
	}
	if selectedRoot == "" {
		return nil, fmt.Errorf("path %q escapes workspace scope", requested)
	}
	root, err := os.OpenRoot(selectedRoot)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	return &rootedWorkspacePath{
		root:         root,
		relative:     selectedRelative,
		absolutePath: candidate,
		mutable:      workspaceMutationAllowed(scope, candidate),
	}, nil
}

func (p *rootedWorkspacePath) Close() error {
	if p == nil || p.root == nil {
		return nil
	}
	return p.root.Close()
}

func (p *rootedWorkspacePath) open() (*os.File, error) {
	return p.root.Open(p.relative)
}

func (p *rootedWorkspacePath) stat() (fs.FileInfo, error) {
	return p.root.Stat(p.relative)
}

func (p *rootedWorkspacePath) mkdirParent() error {
	if !p.mutable {
		return errors.New("mutation rejected: path is outside the Coder owned scope")
	}
	parent := filepath.Dir(p.relative)
	if parent == "." || parent == "" {
		return nil
	}
	return p.root.MkdirAll(parent, 0o755)
}

func (p *rootedWorkspacePath) writeFile(data []byte, perm fs.FileMode) error {
	if err := p.mkdirParent(); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	file, err := p.openMutable(os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		file.Close()
		return fmt.Errorf("truncate file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (p *rootedWorkspacePath) openMutable(flags int, perm fs.FileMode) (*os.File, error) {
	if !p.mutable {
		return nil, errors.New("mutation rejected: path is outside the Coder owned scope")
	}
	file, err := p.root.OpenFile(p.relative, flags, perm)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("mutation rejected: target is not a regular file")
	}
	links, known := fileLinkCount(info)
	if !known {
		file.Close()
		return nil, errors.New("mutation rejected: filesystem link count is unavailable")
	}
	if links != 1 {
		file.Close()
		return nil, fmt.Errorf("mutation rejected: target has %d hard links", links)
	}
	return file, nil
}

func workspaceMutationAllowed(scope WorkspaceScope, candidate string) bool {
	if len(scope.MutationScopes) == 0 {
		return true
	}
	workspaceRoot := normalizeScopePath(scope.PrimaryPath)
	candidate = normalizeScopePath(candidate)
	if workspaceRoot == "" || candidate == "" || !pathWithinAllowedRoots([]string{workspaceRoot}, candidate) {
		return false
	}
	for _, raw := range scope.MutationScopes {
		raw = strings.TrimSpace(filepath.ToSlash(raw))
		if raw == "" {
			continue
		}
		if raw == "." || raw == "*" || raw == "**" || raw == "./**" {
			return true
		}
		raw = strings.TrimPrefix(raw, "./")
		raw = strings.TrimSuffix(strings.TrimSuffix(raw, "/**"), "/*")
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(raw)) || clean != raw || strings.ContainsAny(raw, "*?[]!\\") {
			continue
		}
		ownedRoot := filepath.Join(workspaceRoot, filepath.FromSlash(clean))
		if pathWithinAllowedRoots([]string{ownedRoot}, candidate) {
			return true
		}
	}
	return false
}

// FileInfo.Sys is platform-specific. Reflection keeps the generic tool build
// portable while failing closed on filesystems that cannot report link count.
func fileLinkCount(info fs.FileInfo) (uint64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() < 0 {
			return 0, false
		}
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}
