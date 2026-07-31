package tool

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/discovery"
)

const manageSkillMissingRevision = "missing"

type manageSkillStore struct {
	rootPath string
	root     *os.Root
}

func openManageSkillStore(scope WorkspaceScope, create bool) (*manageSkillStore, error) {
	workspacePath := filepath.Clean(strings.TrimSpace(scope.PrimaryPath))
	if workspacePath == "" || workspacePath == "." || !filepath.IsAbs(workspacePath) {
		return nil, errors.New("manage-skill requires an absolute workspace root")
	}
	resolved, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("manage-skill resolve workspace root failed: %w", err)
	}
	if filepath.Clean(resolved) != workspacePath {
		return nil, errors.New("manage-skill workspace root must be canonical and must not be a symlink")
	}
	workspace, err := os.OpenRoot(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("manage-skill open workspace root failed: %w", err)
	}
	defer workspace.Close()

	const relativeRoot = ".agents/skills"
	if create {
		if err := workspace.MkdirAll(relativeRoot, 0o755); err != nil {
			return nil, fmt.Errorf("manage-skill create skill root failed: %w", err)
		}
	}
	for _, name := range []string{".agents", relativeRoot} {
		info, err := workspace.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && !create {
			return &manageSkillStore{rootPath: filepath.Join(workspacePath, relativeRoot)}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("manage-skill inspect skill root failed: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("manage-skill path %q must be a real directory, not a symlink", name)
		}
	}
	skillRoot, err := workspace.OpenRoot(relativeRoot)
	if err != nil {
		return nil, fmt.Errorf("manage-skill open skill root failed: %w", err)
	}
	return &manageSkillStore{rootPath: filepath.Join(workspacePath, relativeRoot), root: skillRoot}, nil
}

func (s *manageSkillStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *manageSkillStore) skillPath(canonical string) string {
	return filepath.Join(s.rootPath, canonical, "SKILL.md")
}

func (s *manageSkillStore) read(canonical string) ([]byte, error) {
	if s == nil || s.root == nil {
		return nil, fs.ErrNotExist
	}
	dirInfo, err := s.root.Lstat(canonical)
	if err != nil {
		return nil, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, fmt.Errorf("skill directory %q must be a real directory, not a symlink", canonical)
	}
	name := filepath.Join(canonical, "SKILL.md")
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill file %q must be a regular file, not a symlink", name)
	}
	return s.root.ReadFile(name)
}

func (s *manageSkillStore) revision(canonical string) (string, []byte, error) {
	raw, err := s.read(canonical)
	if errors.Is(err, fs.ErrNotExist) {
		return manageSkillMissingRevision, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), raw, nil
}

func (s *manageSkillStore) discover() ([]discovery.SkillSource, []discovery.InvalidSkillSource, error) {
	if s == nil || s.root == nil {
		return []discovery.SkillSource{}, []discovery.InvalidSkillSource{}, nil
	}
	file, err := s.root.Open(".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := file.ReadDir(-1)
	closeErr := file.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	skills := make([]discovery.SkillSource, 0, len(entries))
	invalid := make([]discovery.InvalidSkillSource, 0)
	for _, entry := range entries {
		canonical := strings.TrimSpace(entry.Name())
		if canonical == "" || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		raw, readErr := s.read(canonical)
		if readErr != nil {
			invalid = append(invalid, manageSkillInvalid(s, canonical, readErr))
			continue
		}
		frontmatter, parseErr := discovery.ParseSkillFrontmatter(raw)
		if parseErr == nil {
			parseErr = discovery.ValidateSkillFrontmatter(frontmatter, canonical)
		}
		if parseErr != nil {
			invalid = append(invalid, manageSkillInvalid(s, canonical, parseErr))
			continue
		}
		sum := sha256.Sum256(raw)
		skills = append(skills, discovery.SkillSource{
			Name: strings.TrimSpace(frontmatter.Name), CanonicalName: canonical,
			Description: strings.TrimSpace(frontmatter.Description), Path: s.skillPath(canonical),
			Scope: "workspace-local", Origin: "agents-project-skills", Hash: hex.EncodeToString(sum[:]),
			Active: true, Metadata: frontmatter.Metadata,
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].CanonicalName < skills[j].CanonicalName })
	sort.Slice(invalid, func(i, j int) bool { return invalid[i].Path < invalid[j].Path })
	return skills, invalid, nil
}

func manageSkillInvalid(s *manageSkillStore, canonical string, err error) discovery.InvalidSkillSource {
	return discovery.InvalidSkillSource{DirectoryName: canonical, Path: s.skillPath(canonical), Scope: "workspace-local", Origin: "agents-project-skills", Error: err.Error()}
}

func (s *manageSkillStore) write(canonical string, content []byte, mustExist bool, expectedRevision string) error {
	current, _, err := s.revision(canonical)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedRevision) == "" {
		return errors.New("manage-skill confirm requires expected_revision from the approved proposal")
	}
	if expectedRevision != current {
		return fmt.Errorf("manage-skill proposal is stale: skill revision changed from %q to %q", expectedRevision, current)
	}
	if mustExist {
		if current == manageSkillMissingRevision {
			return fmt.Errorf("skill %q does not exist", canonical)
		}
		file, err := s.root.OpenFile(filepath.Join(canonical, "SKILL.md"), os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() {
			return errors.New("manage-skill opened skill is not a regular file")
		}
		openedRaw, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		openedSum := sha256.Sum256(openedRaw)
		if hex.EncodeToString(openedSum[:]) != expectedRevision {
			return errors.New("manage-skill proposal is stale: skill changed before update")
		}
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, 0); err != nil {
			return err
		}
		if _, err := file.Write(content); err != nil {
			return err
		}
		return file.Sync()
	}
	if current != manageSkillMissingRevision {
		return fmt.Errorf("skill %q already exists; use update", canonical)
	}
	if err := s.root.Mkdir(canonical, 0o755); err != nil {
		return err
	}
	file, err := s.root.OpenFile(filepath.Join(canonical, "SKILL.md"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = s.root.Remove(canonical)
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// DeleteWorkspaceSkill applies the canonical manage-skill deletion semantics to
// a workspace-local .agents/skills definition after verifying its revision.
func DeleteWorkspaceSkill(scope WorkspaceScope, canonical, expectedRevision string) error {
	canonical = strings.TrimSpace(canonical)
	if canonical == "" || discovery.NormalizeSkillName(canonical) != canonical {
		return errors.New("manage-skill delete requires a canonical skill name")
	}
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.delete(canonical, expectedRevision)
}

func (s *manageSkillStore) delete(canonical, expectedRevision string) error {
	current, _, err := s.revision(canonical)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedRevision) == "" {
		return errors.New("manage-skill confirm requires expected_revision from the approved proposal")
	}
	if expectedRevision != current {
		return fmt.Errorf("manage-skill proposal is stale: skill revision changed from %q to %q", expectedRevision, current)
	}
	if current == manageSkillMissingRevision {
		return fmt.Errorf("skill %q does not exist", canonical)
	}
	name := filepath.Join(canonical, "SKILL.md")
	tombstone := filepath.Join(canonical, manageSkillTombstoneName())
	if err := s.root.Rename(name, tombstone); err != nil {
		return err
	}
	restore := true
	defer func() {
		if restore {
			_ = s.root.Rename(tombstone, name)
		}
	}()
	raw, err := s.root.ReadFile(tombstone)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expectedRevision {
		return errors.New("manage-skill proposal is stale: skill changed before delete")
	}
	if err := s.root.Remove(tombstone); err != nil {
		return err
	}
	restore = false
	_ = s.root.Remove(canonical)
	return nil
}

func manageSkillTombstoneName() string {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return ".SKILL.md.delete-pending"
	}
	return ".SKILL.md.delete-" + hex.EncodeToString(suffix[:])
}
