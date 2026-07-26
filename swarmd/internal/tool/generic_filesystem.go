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
	return &rootedWorkspacePath{root: root, relative: selectedRelative, absolutePath: candidate}, nil
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
	parent := filepath.Dir(p.relative)
	if parent == "." || parent == "" {
		return nil
	}
	return p.root.MkdirAll(parent, 0o755)
}

func (p *rootedWorkspacePath) openMutable(flags int, perm fs.FileMode) (*os.File, error) {
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
