package discovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
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
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return Report{}, fmt.Errorf("resolve user home: %w", err)
	}
	swarmConfig := strings.TrimSpace(os.Getenv("SWARM_CONFIG"))
	if swarmConfig == "" {
		swarmConfig = filepath.Join(homeDir, ".config", "swarm")
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
	appendRule := func(path, scope, origin string, precedence int) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := ruleSeen[path]; ok {
			return
		}
		next := appendRuleIfPresent(nil, path, scope, origin, precedence)
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

	// Project walk-up chain for AGENTS.md and CLAUDE.md (nearest directory first).
	for _, root := range scopeRoots {
		for _, dir := range walkupDirs(root) {
			appendRule(filepath.Join(dir, "AGENTS.md"), "workspace-local", "project-chain", precedenceWorkspaceLocal)
			appendRule(filepath.Join(dir, "CLAUDE.md"), "workspace-local", "project-chain", precedenceWorkspaceLocal)
		}
	}

	// User-level defaults.
	appendRule(filepath.Join(homeDir, ".claude", "CLAUDE.md"), "user-local", "claude-user-default", precedenceUserLocal)
	appendRule(filepath.Join(swarmConfig, "AGENTS.md"), "user-local", "swarm-user-default", precedenceUserLocal)

	// Cursor rule files become explicit rule sources.
	for _, root := range scopeRoots {
		appendRules(scanCursorRules(filepath.Join(root, ".cursor", "rules")))
	}

	// Skill sources across ecosystems.
	candidates := make([]SkillSource, 0, 128)
	invalidSkills := make([]InvalidSkillSource, 0, 16)
	appendSkillScan := func(root, scope, origin string, precedence int) {
		valid, invalid := scanSkillDir(root, scope, origin, precedence)
		candidates = append(candidates, valid...)
		invalidSkills = append(invalidSkills, invalid...)
	}
	appendSkillScan(filepath.Join(swarmConfig, managedSkillsDirName), "managed", "swarm-managed-skills", precedenceGlobalCompatible)
	appendSkillScan(filepath.Join(swarmConfig, "skills"), "user-local", "swarm-user-skills", precedenceUserLocal)
	appendSkillScan(filepath.Join(homeDir, ".agents", "skills"), "global-compatible", "agents-global-skills", precedenceGlobalCompatible)
	appendSkillScan(filepath.Join(homeDir, ".claude", "skills"), "user-local", "claude-user-skills", precedenceUserLocal)
	for _, root := range scopeRoots {
		appendSkillScan(filepath.Join(root, ".agents", "skills"), "workspace-local", "agents-project-skills", precedenceWorkspaceLocal)
		appendSkillScan(filepath.Join(root, ".swarm", "skills"), "workspace-local", "swarm-project-skills", precedenceWorkspaceLocal)
		appendSkillScan(filepath.Join(root, ".claude", "skills"), "workspace-local", "claude-project-skills", precedenceWorkspaceLocal)
	}

	active, overrides := resolveSkillCandidates(candidates)
	report.Skills = active
	report.InvalidSkills = invalidSkills
	report.Overrides = append(report.Overrides, overrides...)

	return report, nil
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

func walkupDirs(start string) []string {
	out := make([]string, 0, 16)
	current := filepath.Clean(start)
	for {
		out = append(out, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return out
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

func appendRuleIfPresent(existing []RuleSource, path, scope, origin string, precedence int) []RuleSource {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return existing
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return existing
	}
	entry := RuleSource{
		Name:       filepath.Base(path),
		Path:       path,
		Scope:      scope,
		Origin:     origin,
		Hash:       hash,
		Precedence: precedence,
	}
	return append(existing, entry)
}

func scanCursorRules(root string) []RuleSource {
	out := make([]RuleSource, 0, 32)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		hash, hashErr := fileSHA256(path)
		if hashErr != nil {
			return nil
		}
		out = append(out, RuleSource{
			Name:       filepath.Base(path),
			Path:       path,
			Scope:      "workspace-local",
			Origin:     "cursor-rules",
			Hash:       hash,
			Precedence: precedenceWorkspaceLocal,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func scanSkillDir(root, scope, origin string, precedence int) ([]SkillSource, []InvalidSkillSource) {
	out := make([]SkillSource, 0, 32)
	invalid := make([]InvalidSkillSource, 0, 8)
	entries, err := os.ReadDir(root)
	if err != nil {
		return out, invalid
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := strings.TrimSpace(entry.Name())
		if dirName == "" {
			continue
		}
		skillPath := filepath.Join(root, dirName, "SKILL.md")
		info, err := os.Stat(skillPath)
		if err != nil || info.IsDir() {
			continue
		}
		raw, err := os.ReadFile(skillPath)
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
		hash, err := fileSHA256(skillPath)
		if err != nil {
			invalid = append(invalid, InvalidSkillSource{
				DirectoryName: dirName,
				DeclaredName:  declaredName,
				Path:          skillPath,
				Scope:         scope,
				Origin:        origin,
				Error:         fmt.Sprintf("hash skill: %v", err),
			})
			continue
		}
		out = append(out, SkillSource{
			Name:          declaredName,
			CanonicalName: normalizeName(frontmatter.Name),
			Description:   strings.TrimSpace(frontmatter.Description),
			Path:          skillPath,
			Scope:         scope,
			Origin:        origin,
			Hash:          hash,
			Precedence:    precedence,
			Metadata:      copyStringMap(frontmatter.Metadata),
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
