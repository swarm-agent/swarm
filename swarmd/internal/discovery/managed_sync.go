package discovery

import (
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
)

const managedSkillsDirName = "managed/skills"

type ManagedSkillFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Mode    uint32 `json:"mode,omitempty"`
	Hash    string `json:"hash,omitempty"`
}

type ManagedSkill struct {
	Name    string             `json:"name"`
	Files   []ManagedSkillFile `json:"files"`
	Hash    string             `json:"hash,omitempty"`
	Updated int64              `json:"updated_at,omitempty"`
}

type ManagedSkillBundle struct {
	Skills       []ManagedSkill `json:"skills"`
	SnapshotHash string         `json:"snapshot_hash"`
	ExportedAt   int64          `json:"exported_at,omitempty"`
}

func ManagedSkillsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	swarmConfig := strings.TrimSpace(os.Getenv("SWARM_CONFIG"))
	if swarmConfig == "" {
		swarmConfig = filepath.Join(homeDir, ".config", "swarm")
	}
	return filepath.Join(swarmConfig, managedSkillsDirName), nil
}

func (s *Service) ExportManagedSkillBundle() (ManagedSkillBundle, error) {
	if s == nil {
		return ManagedSkillBundle{}, fmt.Errorf("discovery service is not configured")
	}
	report, err := s.Scan(".")
	if err != nil {
		return ManagedSkillBundle{}, err
	}
	skills := make([]ManagedSkill, 0, len(report.Skills))
	for _, source := range report.Skills {
		root := filepath.Dir(source.Path)
		name := NormalizeSkillName(source.CanonicalName)
		if name == "" {
			name = NormalizeSkillName(source.Name)
		}
		if name == "" {
			continue
		}
		files, updated, err := exportSkillFiles(root)
		if err != nil {
			return ManagedSkillBundle{}, err
		}
		if len(files) == 0 {
			continue
		}
		skills = append(skills, ManagedSkill{Name: name, Files: files, Updated: updated})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	for i := range skills {
		skills[i].Hash = hashManagedSkill(skills[i])
	}
	bundle := ManagedSkillBundle{Skills: skills, ExportedAt: time.Now().UnixMilli()}
	bundle.SnapshotHash = hashManagedSkillBundle(bundle)
	return bundle, nil
}

func (s *Service) ApplyManagedSkillBundle(bundle ManagedSkillBundle) error {
	if s == nil {
		return fmt.Errorf("discovery service is not configured")
	}
	root, err := ManagedSkillsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(bundle.Skills))
	for _, skill := range bundle.Skills {
		name := NormalizeSkillName(skill.Name)
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
		skillRoot := filepath.Join(root, name)
		if err := os.RemoveAll(skillRoot); err != nil {
			return err
		}
		if err := os.MkdirAll(skillRoot, 0o755); err != nil {
			return err
		}
		for _, file := range skill.Files {
			rel := filepath.Clean(strings.TrimSpace(file.Path))
			if rel == "." || rel == "" || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
				return fmt.Errorf("invalid managed skill file path %q", file.Path)
			}
			mode := fs.FileMode(file.Mode)
			if mode == 0 {
				mode = 0o644
			}
			target := filepath.Join(skillRoot, rel)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, file.Content, mode.Perm()); err != nil {
				return err
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := NormalizeSkillName(entry.Name())
		if _, ok := seen[name]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func exportSkillFiles(root string) ([]ManagedSkillFile, int64, error) {
	files := make([]ManagedSkillFile, 0, 8)
	var updated int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if mod := info.ModTime().UnixMilli(); mod > updated {
			updated = mod
		}
		sum := sha256.Sum256(content)
		files = append(files, ManagedSkillFile{Path: filepath.ToSlash(rel), Content: content, Mode: uint32(info.Mode().Perm()), Hash: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, updated, nil
}

func hashManagedSkill(skill ManagedSkill) string {
	h := sha256.New()
	_, _ = io.WriteString(h, skill.Name)
	for _, file := range skill.Files {
		_, _ = io.WriteString(h, "\n")
		_, _ = io.WriteString(h, file.Path)
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, file.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashManagedSkillBundle(bundle ManagedSkillBundle) string {
	h := sha256.New()
	for _, skill := range bundle.Skills {
		_, _ = io.WriteString(h, skill.Name)
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, skill.Hash)
		_, _ = io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
