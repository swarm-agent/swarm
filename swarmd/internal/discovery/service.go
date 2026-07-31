package discovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"swarm/packages/swarmd/internal/appstorage"
)

type Service struct {
	userHome string
}

func NewService() *Service {
	userHome, _ := os.UserHomeDir()
	return NewServiceWithUserHome(userHome)
}

// NewServiceWithUserHome creates a discovery service with an explicit host-user
// home. The home is a read-only skill source and is not added to workspace
// authorization roots.
func NewServiceWithUserHome(userHome string) *Service {
	return &Service{userHome: strings.TrimSpace(userHome)}
}

func NormalizeSkillName(input string) string {
	return normalizeName(input)
}

type RuleSource struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Scope      string `json:"scope"`
	Origin     string `json:"origin"`
	Hash       string `json:"hash"`
	Precedence int    `json:"precedence"`
	Content    []byte `json:"-"`
}

type SkillSource struct {
	Name          string            `json:"name"`
	CanonicalName string            `json:"canonical_name"`
	Description   string            `json:"description"`
	Path          string            `json:"path"`
	Scope         string            `json:"scope"`
	Origin        string            `json:"origin"`
	Hash          string            `json:"hash"`
	Precedence    int               `json:"precedence"`
	Active        bool              `json:"active"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Content       []byte            `json:"-"`
}

type InvalidSkillSource struct {
	DirectoryName string `json:"directory_name"`
	DeclaredName  string `json:"declared_name,omitempty"`
	Path          string `json:"path"`
	Scope         string `json:"scope"`
	Origin        string `json:"origin"`
	Error         string `json:"error"`
}

type Override struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	KeptPath    string `json:"kept_path"`
	DroppedPath string `json:"dropped_path"`
	Reason      string `json:"reason"`
}

type Report struct {
	RequestedPath string               `json:"requested_path"`
	ResolvedPath  string               `json:"resolved_path"`
	ScannedAt     int64                `json:"scanned_at"`
	Rules         []RuleSource         `json:"rules"`
	Skills        []SkillSource        `json:"skills"`
	InvalidSkills []InvalidSkillSource `json:"invalid_skills,omitempty"`
	Overrides     []Override           `json:"overrides"`
}

type SkillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

const (
	precedenceWorkspaceLocal   = 400
	precedenceUserLocal        = 300
	precedenceGlobalCompatible = 200

	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

func (s *Service) Scan(cwd string) (Report, error) {
	return s.ScanScope(cwd, nil)
}

func (s *Service) ScanScope(primaryPath string, roots []string) (Report, error) {
	resolved, err := resolvePath(primaryPath)
	if err != nil {
		return Report{}, err
	}
	scopeRoots, err := normalizeScopeRoots(resolved, roots)
	if err != nil {
		return Report{}, err
	}
	swarmConfig, err := swarmConfigDir()
	if err != nil {
		return Report{}, err
	}

	report := Report{
		RequestedPath: primaryPath,
		ResolvedPath:  resolved,
		ScannedAt:     time.Now().UnixMilli(),
		Rules:         make([]RuleSource, 0, 32),
		Skills:        make([]SkillSource, 0, 64),
		InvalidSkills: make([]InvalidSkillSource, 0, 16),
		Overrides:     make([]Override, 0, 16),
	}
	ruleSeen := make(map[string]struct{}, 64)
	appendRule := func(rootPath, relativePath, scope, origin string, precedence int) {
		path := filepath.Join(rootPath, relativePath)
		if _, ok := ruleSeen[path]; ok {
			return
		}
		next := appendRootedRuleIfPresent(nil, rootPath, relativePath, scope, origin, precedence)
		if len(next) == 0 {
			return
		}
		ruleSeen[path] = struct{}{}
		report.Rules = append(report.Rules, next[0])
	}
	appendRules := func(entries []RuleSource) {
		for _, entry := range entries {
			path := strings.TrimSpace(entry.Path)
			if path == "" {
				continue
			}
			if _, ok := ruleSeen[path]; ok {
				continue
			}
			ruleSeen[path] = struct{}{}
			report.Rules = append(report.Rules, entry)
		}
	}

	// Workspace instruction files are explicit per-root only. Do not walk parent
	// directories into broader scopes such as a user's home directory.
	for _, root := range scopeRoots {
		appendRule(root, "AGENTS.md", "workspace-local", "workspace-root", precedenceWorkspaceLocal)
	}

	// Swarm-managed daemon defaults.
	appendRule(swarmConfig, "AGENTS.md", "managed", "swarm-managed-default", precedenceGlobalCompatible)

	// Cursor rule files become explicit rule sources.
	for _, root := range scopeRoots {
		appendRules(scanCursorRules(root, filepath.Join(".cursor", "rules")))
	}

	// Skill sources across ecosystems. Skill roots are separate from workspace
	// authorization roots: shared user skills are read-only discovery inputs.
	candidates := make([]SkillSource, 0, 128)
	invalidSkills := make([]InvalidSkillSource, 0, 16)
	skillRootSeen := make(map[string]struct{}, 16)
	appendSkillScan := func(rootPath, relativePath, scope, origin string, precedence int) {
		skillRootPath := filepath.Clean(filepath.Join(rootPath, relativePath))
		if _, ok := skillRootSeen[skillRootPath]; ok {
			return
		}
		skillRootSeen[skillRootPath] = struct{}{}
		valid, invalid := scanSkillDir(rootPath, relativePath, scope, origin, precedence)
		candidates = append(candidates, valid...)
		invalidSkills = append(invalidSkills, invalid...)
	}
	workspaceRoots := workspaceSkillRoots(resolved, scopeRoots)
	for index := len(workspaceRoots) - 1; index >= 0; index-- {
		root := workspaceRoots[index]
		appendSkillScan(root, filepath.Join(".agents", "skills"), "workspace-local", "agents-project-skills", precedenceWorkspaceLocal+index)
		appendSkillScan(root, filepath.Join(".swarm", "skills"), "workspace-local", "swarm-project-skills", precedenceWorkspaceLocal+index)
	}
	if userHome := strings.TrimSpace(s.userHome); userHome != "" {
		resolvedHome, resolveErr := resolvePath(userHome)
		if resolveErr != nil {
			return Report{}, fmt.Errorf("resolve user home for skill discovery: %w", resolveErr)
		}
		appendSkillScan(resolvedHome, filepath.Join(".agents", "skills"), "user-local", "agents-user-skills", precedenceUserLocal)
	}
	appendSkillScan(swarmConfig, "skills", "managed", "swarm-managed-config-skills", precedenceGlobalCompatible)

	active, overrides := resolveSkillCandidates(candidates)
	report.Skills = active
	report.InvalidSkills = invalidSkills
	report.Overrides = append(report.Overrides, overrides...)

	return report, nil
}

func swarmConfigDir() (string, error) {
	return appstorage.DataDir("config")
}

func resolvePath(input string) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd: %w", err)
		}
		target = cwd
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path for %q: %w", target, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return resolved, nil
}

func workspaceSkillRoots(primary string, scopeRoots []string) []string {
	primary = filepath.Clean(primary)
	boundary := primary
	for _, root := range scopeRoots {
		root = filepath.Clean(root)
		if !pathWithinRoot(root, primary) {
			continue
		}
		if boundary == primary || pathWithinRoot(root, boundary) {
			boundary = root
		}
	}

	chain := make([]string, 0, 8)
	for current := primary; ; current = filepath.Dir(current) {
		chain = append(chain, current)
		if current == boundary {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}

	seen := make(map[string]struct{}, len(chain)+len(scopeRoots))
	out := make([]string, 0, len(chain)+len(scopeRoots))
	add := func(root string) {
		root = filepath.Clean(root)
		if root == "" || root == "." {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	for _, root := range scopeRoots {
		if !pathWithinRoot(root, primary) {
			add(root)
		}
	}
	for _, root := range chain {
		add(root)
	}
	return out
}

func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func normalizeScopeRoots(primary string, roots []string) ([]string, error) {
	seen := make(map[string]struct{}, len(roots)+1)
	out := make([]string, 0, len(roots)+1)
	add := func(path string) error {
		path = strings.TrimSpace(path)
		if path == "" {
			return nil
		}
		resolved, err := resolvePath(path)
		if err != nil {
			return err
		}
		if _, ok := seen[resolved]; ok {
			return nil
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
		return nil
	}
	if err := add(primary); err != nil {
		return nil, err
	}
	for _, root := range roots {
		if err := add(root); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func appendRootedRuleIfPresent(existing []RuleSource, rootPath, relativePath, scope, origin string, precedence int) []RuleSource {
	root, err := openDiscoveryRoot(rootPath)
	if err != nil {
		return existing
	}
	defer root.Close()
	info, raw, err := readRootedRegularFile(root, relativePath)
	if err != nil || info.IsDir() {
		return existing
	}
	hash := sha256.Sum256(raw)
	entry := RuleSource{
		Name:       filepath.Base(relativePath),
		Path:       filepath.Join(rootPath, relativePath),
		Scope:      scope,
		Origin:     origin,
		Hash:       hex.EncodeToString(hash[:]),
		Precedence: precedence,
		Content:    raw,
	}
	return append(existing, entry)
}

func scanCursorRules(rootPath, relativeRoot string) []RuleSource {
	out := make([]RuleSource, 0, 32)
	root, err := openDiscoveryRoot(rootPath)
	if err != nil {
		return out
	}
	defer root.Close()
	_ = fs.WalkDir(root.FS(), relativeRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		_, raw, readErr := readRootedRegularFile(root, path)
		if readErr != nil {
			return nil
		}
		hash := sha256.Sum256(raw)
		out = append(out, RuleSource{
			Name:       filepath.Base(path),
			Path:       filepath.Join(rootPath, path),
			Scope:      "workspace-local",
			Origin:     "cursor-rules",
			Hash:       hex.EncodeToString(hash[:]),
			Precedence: precedenceWorkspaceLocal,
			Content:    raw,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func scanSkillDir(rootPath, relativeRoot, scope, origin string, precedence int) ([]SkillSource, []InvalidSkillSource) {
	out := make([]SkillSource, 0, 32)
	invalid := make([]InvalidSkillSource, 0, 8)
	root, err := openDiscoveryRoot(rootPath)
	if err != nil {
		return out, invalid
	}
	defer root.Close()
	dir, err := root.Open(relativeRoot)
	if err != nil {
		return out, invalid
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return out, invalid
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dirName := strings.TrimSpace(entry.Name())
		if dirName == "" {
			continue
		}
		relativeSkillPath := filepath.Join(relativeRoot, dirName, "SKILL.md")
		skillPath := filepath.Join(rootPath, relativeSkillPath)
		_, raw, err := readRootedRegularFile(root, relativeSkillPath)
		if err != nil {
			invalid = append(invalid, InvalidSkillSource{
				DirectoryName: dirName,
				Path:          skillPath,
				Scope:         scope,
				Origin:        origin,
				Error:         fmt.Sprintf("read skill: %v", err),
			})
			continue
		}
		frontmatter, err := ParseSkillFrontmatter(raw)
		if err != nil {
			invalid = append(invalid, InvalidSkillSource{
				DirectoryName: dirName,
				Path:          skillPath,
				Scope:         scope,
				Origin:        origin,
				Error:         err.Error(),
			})
			continue
		}
		declaredName := strings.TrimSpace(frontmatter.Name)
		if err := ValidateSkillFrontmatter(frontmatter, dirName); err != nil {
			invalid = append(invalid, InvalidSkillSource{
				DirectoryName: dirName,
				DeclaredName:  declaredName,
				Path:          skillPath,
				Scope:         scope,
				Origin:        origin,
				Error:         err.Error(),
			})
			continue
		}
		hash := sha256.Sum256(raw)
		out = append(out, SkillSource{
			Name:          declaredName,
			CanonicalName: normalizeName(frontmatter.Name),
			Description:   strings.TrimSpace(frontmatter.Description),
			Path:          skillPath,
			Scope:         scope,
			Origin:        origin,
			Hash:          hex.EncodeToString(hash[:]),
			Precedence:    precedence,
			Metadata:      copyStringMap(frontmatter.Metadata),
			Content:       raw,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CanonicalName < out[j].CanonicalName
	})
	sort.Slice(invalid, func(i, j int) bool {
		return invalid[i].Path < invalid[j].Path
	})
	return out, invalid
}

func openDiscoveryRoot(path string) (*os.Root, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("discovery root must be an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(resolved) != path {
		return nil, fmt.Errorf("discovery root %q must be canonical", path)
	}
	return os.OpenRoot(path)
}

func readRootedRegularFile(root *os.Root, name string) (fs.FileInfo, []byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%q must be a regular file, not a symlink", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, nil, fmt.Errorf("%q changed during discovery", name)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	return opened, raw, nil
}

func ParseSkillFrontmatter(raw []byte) (SkillFrontmatter, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return SkillFrontmatter{}, fmt.Errorf("missing skill content")
	}
	if !bytes.HasPrefix(trimmed, []byte("---\n")) && !bytes.HasPrefix(trimmed, []byte("---\r\n")) {
		return SkillFrontmatter{}, fmt.Errorf("missing YAML frontmatter")
	}
	body := trimmed[4:]
	if len(trimmed) >= 5 && bytes.HasPrefix(trimmed, []byte("---\r\n")) {
		body = trimmed[5:]
	}
	end := bytes.Index(body, []byte("\n---"))
	endLen := 4
	if end < 0 {
		end = bytes.Index(body, []byte("\r\n---"))
		endLen = 5
	}
	if end < 0 {
		return SkillFrontmatter{}, fmt.Errorf("unterminated YAML frontmatter")
	}
	frontmatterBytes := body[:end]
	closing := body[end+endLen:]
	if len(closing) > 0 {
		switch {
		case bytes.HasPrefix(closing, []byte("\n")):
		case bytes.HasPrefix(closing, []byte("\r\n")):
		default:
			return SkillFrontmatter{}, fmt.Errorf("invalid frontmatter terminator")
		}
	}
	var frontmatter SkillFrontmatter
	if err := yaml.Unmarshal(frontmatterBytes, &frontmatter); err != nil {
		return SkillFrontmatter{}, fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	return frontmatter, nil
}

func ValidateSkillFrontmatter(frontmatter SkillFrontmatter, dirName string) error {
	name := strings.TrimSpace(frontmatter.Name)
	if name == "" {
		return fmt.Errorf("skill frontmatter requires name")
	}
	if !isValidSkillName(name) {
		return fmt.Errorf("skill name %q must use lowercase letters, numbers, and single hyphens, be at most %d characters, and not start or end with a hyphen", name, maxSkillNameLength)
	}
	description := strings.TrimSpace(frontmatter.Description)
	if description == "" {
		return fmt.Errorf("skill frontmatter requires description")
	}
	if len([]rune(description)) > maxSkillDescriptionLength {
		return fmt.Errorf("skill description exceeds %d characters", maxSkillDescriptionLength)
	}
	dirName = strings.TrimSpace(dirName)
	if dirName != "" && name != dirName {
		return fmt.Errorf("skill frontmatter name %q must match directory %q", name, dirName)
	}
	return nil
}

func resolveSkillCandidates(candidates []SkillSource) ([]SkillSource, []Override) {
	overrides := make([]Override, 0, 16)
	if len(candidates) == 0 {
		return make([]SkillSource, 0), overrides
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Precedence != candidates[j].Precedence {
			return candidates[i].Precedence > candidates[j].Precedence
		}
		if candidates[i].CanonicalName != candidates[j].CanonicalName {
			return candidates[i].CanonicalName < candidates[j].CanonicalName
		}
		return candidates[i].Path < candidates[j].Path
	})

	activeByName := make(map[string]SkillSource, len(candidates))
	seenByIdentity := make(map[string]SkillSource, len(candidates))
	for _, candidate := range candidates {
		identity := candidate.CanonicalName + ":" + candidate.Hash
		if existing, ok := seenByIdentity[identity]; ok {
			overrides = append(overrides, Override{
				Kind:        "skill",
				Name:        candidate.CanonicalName,
				KeptPath:    existing.Path,
				DroppedPath: candidate.Path,
				Reason:      "duplicate-content",
			})
			continue
		}

		if existing, ok := activeByName[candidate.CanonicalName]; ok {
			overrides = append(overrides, Override{
				Kind:        "skill",
				Name:        candidate.CanonicalName,
				KeptPath:    existing.Path,
				DroppedPath: candidate.Path,
				Reason:      "lower-precedence",
			})
			seenByIdentity[identity] = existing
			continue
		}

		candidate.Active = true
		activeByName[candidate.CanonicalName] = candidate
		seenByIdentity[identity] = candidate
	}

	active := make([]SkillSource, 0, len(activeByName))
	for _, skill := range activeByName {
		active = append(active, skill)
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].CanonicalName < active[j].CanonicalName
	})
	return active, overrides
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeName(input string) string {
	value := strings.ToLower(strings.TrimSpace(input))
	if value == "" {
		return "skill"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "skill"
	}
	return out
}

func isValidSkillName(name string) bool {
	if len(name) == 0 || len(name) > maxSkillNameLength {
		return false
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func copyStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		out[trimmedKey] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
