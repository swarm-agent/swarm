package videosource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	BrowseMaxEntries = 200
	BrowseMaxDepth   = 16
)

var acceptedVideoExtensions = map[string]struct{}{
	".avi": {}, ".m4v": {}, ".mkv": {}, ".mov": {}, ".mp4": {}, ".mpeg": {}, ".mpg": {}, ".webm": {},
}

var acceptedAudioExtensions = map[string]struct{}{
	".aac": {}, ".flac": {}, ".m4a": {}, ".mp3": {}, ".oga": {}, ".ogg": {}, ".opus": {}, ".wav": {},
}

const (
	MediaKindVideo = "video"
	MediaKindAudio = "audio"
)

type WorkspaceAuthority interface {
	ListSourceMediaDirectoriesForPrincipal(identity.Principal, string) (workspaceruntime.Resolution, error)
}

type WorkspaceDirectoryRegistrar interface {
	AddSourceMediaDirectoryForPrincipal(identity.Principal, string, string) (workspaceruntime.Resolution, error)
}

type Service struct {
	workspace WorkspaceAuthority
	store     *pebblestore.SessionStore
}

type Root struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type Directory struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path"`
}

type Clip struct {
	Ref                string `json:"ref"`
	Name               string `json:"name"`
	MediaKind          string `json:"media_kind"`
	Extension          string `json:"extension"`
	MIMEType           string `json:"mime_type"`
	SizeBytes          int64  `json:"size_bytes"`
	ModifiedAt         int64  `json:"modified_at"`
	SourceFingerprint  string `json:"source_fingerprint"`
	FingerprintVersion string `json:"fingerprint_version,omitempty"`
	TranscriptRef      string `json:"transcript_ref,omitempty"`
}

type AudioClip struct {
	Ref                string `json:"ref"`
	Name               string `json:"name"`
	MediaKind          string `json:"media_kind"`
	Extension          string `json:"extension"`
	MIMEType           string `json:"mime_type"`
	SizeBytes          int64  `json:"size_bytes"`
	ModifiedAt         int64  `json:"modified_at"`
	SourceFingerprint  string `json:"source_fingerprint"`
	FingerprintVersion string `json:"fingerprint_version"`
	TranscriptRef      string `json:"transcript_ref,omitempty"`
	AnalysisRef        string `json:"analysis_ref,omitempty"`
}

type BrowseResult struct {
	WorkspaceID  string
	RootPath     string
	RootRef      string
	RelativePath string
	Directories  []Directory
	Clips        []Clip
	AudioClips   []AudioClip
}

func NewService(workspace WorkspaceAuthority, store *pebblestore.SessionStore) *Service {
	return &Service{workspace: workspace, store: store}
}

func (s *Service) Store() *pebblestore.SessionStore {
	if s == nil {
		return nil
	}
	return s.store
}

// EnsureRoot resolves or registers a canonical source-media root directory.
// If the workspace already has registered roots, the first registered root or
// preferredDir (if specified and matching/registerable) is returned.
// If no roots exist, it creates and registers a default source_media directory.
func (s *Service) EnsureRoot(principal identity.Principal, workspacePath, preferredDir string) (string, string, error) {
	if s == nil {
		return "", "", errors.New("source service is not configured")
	}
	if s.workspace != nil && workspacePath != "" {
		res, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, workspacePath)
		if err == nil {
			workspaceID := res.WorkspaceID
			preferredDir = strings.TrimSpace(preferredDir)
			if preferredDir != "" {
				cleanPreferred, resolveErr := ResolveRootPath(preferredDir)
				if resolveErr == nil {
					for _, d := range res.SourceMediaDirectories {
						if filepath.Clean(d) == cleanPreferred {
							return cleanPreferred, workspaceID, nil
						}
					}
					if registrar, ok := s.workspace.(WorkspaceDirectoryRegistrar); ok {
						addRes, addErr := registrar.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, cleanPreferred)
						if addErr == nil {
							return cleanPreferred, addRes.WorkspaceID, nil
						}
					}
				}
			}
			if len(res.SourceMediaDirectories) > 0 {
				return filepath.Clean(res.SourceMediaDirectories[0]), workspaceID, nil
			}
			defaultDir := filepath.Join(workspacePath, "source_media")
			if err := os.MkdirAll(defaultDir, 0o755); err != nil {
				return "", "", fmt.Errorf("create source media directory: %w", err)
			}
			if registrar, ok := s.workspace.(WorkspaceDirectoryRegistrar); ok {
				addRes, addErr := registrar.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, defaultDir)
				if addErr == nil {
					return defaultDir, addRes.WorkspaceID, nil
				}
			}
			return defaultDir, workspaceID, nil
		}
	}
	targetDir := strings.TrimSpace(preferredDir)
	if targetDir == "" {
		if workspacePath != "" {
			targetDir = filepath.Join(workspacePath, "source_media")
		} else {
			targetDir = filepath.Join(os.TempDir(), "swarm_source_media", principal.AccountScopeID)
		}
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create media directory: %w", err)
	}
	return targetDir, "", nil
}

// ImportAudio writes audio bytes to an authenticated source media directory,
// validates the file against supported audio formats, and persists an authenticated
// AudioSourceRecord in the session store.
func (s *Service) ImportAudio(principal identity.Principal, workspacePath, workspaceID, displayName, mimeType string, data []byte, preferredDir string) (pebblestore.AudioSourceRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.AudioSourceRecord{}, errors.New("source service is not configured")
	}
	if len(data) == 0 {
		return pebblestore.AudioSourceRecord{}, errors.New("audio data is empty")
	}
	rootPath, resolvedWorkspaceID, err := s.EnsureRoot(principal, workspacePath, preferredDir)
	if err != nil {
		return pebblestore.AudioSourceRecord{}, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		workspaceID = resolvedWorkspaceID
	}
	if strings.TrimSpace(workspaceID) == "" {
		workspaceID = "workspace"
	}

	ext := strings.ToLower(filepath.Ext(displayName))
	if ext == "" {
		switch strings.ToLower(strings.TrimSpace(mimeType)) {
		case "audio/mpeg", "audio/mp3":
			ext = ".mp3"
		case "audio/wav", "audio/x-wav", "audio/wave":
			ext = ".wav"
		case "audio/mp4", "audio/m4a", "audio/x-m4a":
			ext = ".m4a"
		case "audio/aac":
			ext = ".aac"
		case "audio/flac", "audio/x-flac":
			ext = ".flac"
		case "audio/ogg", "audio/opus":
			ext = ".ogg"
		default:
			ext = ".mp3"
		}
	}
	canonicalMIME, supported := pebblestore.SupportedAudioMIMEForExtension(ext)
	if !supported {
		ext = ".mp3"
		canonicalMIME, _ = pebblestore.SupportedAudioMIMEForExtension(ext)
	}

	cleanBase := sanitizeFilename(displayName)
	if cleanBase == "" || cleanBase == ext {
		cleanBase = "audio"
	}
	if !strings.HasSuffix(strings.ToLower(cleanBase), ext) {
		cleanBase += ext
	}

	destPath := filepath.Join(rootPath, cleanBase)
	if existing, readErr := os.ReadFile(destPath); readErr == nil {
		if !bytes.Equal(existing, data) {
			hash := sha256.Sum256(data)
			prefix := hex.EncodeToString(hash[:])[:8]
			stem := strings.TrimSuffix(cleanBase, ext)
			cleanBase = fmt.Sprintf("%s_%s%s", stem, prefix, ext)
			destPath = filepath.Join(rootPath, cleanBase)
		}
	}

	if err := os.WriteFile(destPath, data, 0o600); err != nil {
		return pebblestore.AudioSourceRecord{}, fmt.Errorf("write imported audio file: %w", err)
	}
	stat, err := os.Stat(destPath)
	if err != nil {
		return pebblestore.AudioSourceRecord{}, fmt.Errorf("stat imported audio file: %w", err)
	}

	name := displayName
	if strings.TrimSpace(name) == "" {
		name = cleanBase
	}

	record := pebblestore.AudioSourceRecord{
		AccountScopeID: principal.AccountScopeID,
		WorkspaceID:    workspaceID,
		RootPath:       rootPath,
		RelativePath:   cleanBase,
		DisplayName:    name,
		MIMEType:       canonicalMIME,
		SizeBytes:      stat.Size(),
		ModifiedAt:     stat.ModTime().UnixMilli(),
	}

	saved, err := s.store.PutAudioSourceRecord(record)
	if err != nil {
		return pebblestore.AudioSourceRecord{}, fmt.Errorf("save audio source record: %w", err)
	}
	if err := pebblestore.ValidateAudioSourceRecord(saved); err != nil {
		return pebblestore.AudioSourceRecord{}, fmt.Errorf("validate imported audio source record: %w", err)
	}
	return saved, nil
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == ".." || name == "/" {
		return "audio"
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, " ")
	for strings.HasPrefix(s, ".") {
		s = strings.TrimPrefix(s, ".")
	}
	if s == "" {
		return "audio"
	}
	return s
}

func (s *Service) ListRoots(principal identity.Principal, workspacePath string) (string, []Root, error) {
	resolution, roots, err := s.registeredRoots(principal, workspacePath)
	if err != nil {
		return "", nil, err
	}
	out := make([]Root, 0, len(roots))
	for path, ref := range roots {
		out = append(out, Root{Ref: ref, Name: filepath.Base(path)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Name, out[j].Name) {
			return out[i].Ref < out[j].Ref
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return resolution.WorkspaceID, out, nil
}

func (s *Service) Browse(principal identity.Principal, workspacePath, rootRef, relativePath string) (BrowseResult, error) {
	resolution, roots, err := s.registeredRoots(principal, workspacePath)
	if err != nil {
		return BrowseResult{}, err
	}
	rootRef = strings.TrimSpace(rootRef)
	var rootPath string
	for candidate, ref := range roots {
		if ref == rootRef {
			rootPath = candidate
			break
		}
	}
	if rootPath == "" {
		return BrowseResult{}, errors.New("source root reference is not registered in the authenticated workspace")
	}
	return s.browseResolved(principal, resolution.WorkspaceID, rootPath, rootRef, relativePath)
}

// BrowsePath is the authenticated Desktop API bridge. AI tools use Browse and
// receive only opaque root references, never host paths.
func (s *Service) BrowsePath(principal identity.Principal, workspacePath, requestedRoot, relativePath string) (BrowseResult, error) {
	resolution, roots, err := s.registeredRoots(principal, workspacePath)
	if err != nil {
		return BrowseResult{}, err
	}
	requestedRoot, err = ResolveRootPath(requestedRoot)
	if err != nil {
		return BrowseResult{}, err
	}
	rootRef, ok := roots[requestedRoot]
	if !ok {
		return BrowseResult{}, errors.New("media folder is not a registered source-media root for this workspace")
	}
	return s.browseResolved(principal, resolution.WorkspaceID, requestedRoot, rootRef, relativePath)
}

func (s *Service) ResolveClips(principal identity.Principal, workspacePath string, refs []string) (string, []pebblestore.VideoSourceRecord, error) {
	if len(refs) == 0 || len(refs) > pebblestore.SessionVideoAttachmentMaxCount {
		return "", nil, fmt.Errorf("video_refs requires between 1 and %d exact references", pebblestore.SessionVideoAttachmentMaxCount)
	}
	resolution, roots, err := s.registeredRoots(principal, workspacePath)
	if err != nil {
		return "", nil, err
	}
	registered := make(map[string]struct{}, len(roots))
	for path := range roots {
		registered[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(refs))
	records := make([]pebblestore.VideoSourceRecord, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		record, found, readErr := s.store.GetVideoSourceRecord(principal.AccountScopeID, resolution.WorkspaceID, ref)
		if readErr != nil || !found {
			if readErr == nil {
				readErr = errors.New("video reference is not registered in the authenticated workspace")
			}
			return "", nil, readErr
		}
		if _, ok := registered[filepath.Clean(record.RootPath)]; !ok {
			return "", nil, errors.New("video reference no longer belongs to a registered source-media root")
		}
		if err := pebblestore.ValidateVideoSourceRecord(record); err != nil {
			return "", nil, fmt.Errorf("video source is stale or unavailable: %w", err)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return "", nil, errors.New("video_refs contains no unique references")
	}
	return resolution.WorkspaceID, records, nil
}

// ResolveAudioClips resolves exact opaque audio references while revalidating
// their registered-root membership and private source fingerprint.
func (s *Service) ResolveAudioClips(principal identity.Principal, workspacePath string, refs []string) (string, []pebblestore.AudioSourceRecord, error) {
	if len(refs) == 0 || len(refs) > pebblestore.SessionVideoAttachmentMaxCount {
		return "", nil, fmt.Errorf("audio_refs requires between 1 and %d exact references", pebblestore.SessionVideoAttachmentMaxCount)
	}
	resolution, roots, err := s.registeredRoots(principal, workspacePath)
	if err != nil {
		return "", nil, err
	}
	registered := make(map[string]struct{}, len(roots))
	for path := range roots {
		registered[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(refs))
	records := make([]pebblestore.AudioSourceRecord, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		record, found, readErr := s.store.GetAudioSourceRecord(principal.AccountScopeID, resolution.WorkspaceID, ref)
		if readErr != nil || !found {
			if readErr == nil {
				readErr = errors.New("audio reference is not registered in the authenticated workspace")
			}
			return "", nil, readErr
		}
		if _, ok := registered[filepath.Clean(record.RootPath)]; !ok {
			return "", nil, errors.New("audio reference no longer belongs to a registered source-media root")
		}
		if err := pebblestore.ValidateAudioSourceRecord(record); err != nil {
			return "", nil, fmt.Errorf("audio source is stale or unavailable: %w", err)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return "", nil, errors.New("audio_refs contains no unique references")
	}
	return resolution.WorkspaceID, records, nil
}

func (s *Service) registeredRoots(principal identity.Principal, workspacePath string) (workspaceruntime.Resolution, map[string]string, error) {
	if s == nil || s.workspace == nil || s.store == nil || !principal.Valid() {
		return workspaceruntime.Resolution{}, nil, errors.New("source-media service requires authenticated workspace authority")
	}
	resolution, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, workspacePath)
	if err != nil {
		return workspaceruntime.Resolution{}, nil, err
	}
	roots := make(map[string]string, len(resolution.SourceMediaDirectories))
	for _, candidate := range resolution.SourceMediaDirectories {
		path, resolveErr := ResolveRootPath(candidate)
		if resolveErr != nil {
			continue
		}
		roots[path] = rootReference(principal.AccountScopeID, resolution.WorkspaceID, path)
	}
	return resolution, roots, nil
}

func (s *Service) browseResolved(principal identity.Principal, workspaceID, rootPath, rootRef, relativePath string) (BrowseResult, error) {
	relativePath, err := CleanRelativePath(relativePath)
	if err != nil {
		return BrowseResult{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return BrowseResult{}, err
	}
	defer root.Close()
	directory, err := root.Open(relativePath)
	if err != nil {
		return BrowseResult{}, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return BrowseResult{}, errors.New("media browse target is not a directory")
	}
	entries, err := directory.ReadDir(BrowseMaxEntries + 1)
	if err != nil {
		return BrowseResult{}, err
	}
	if len(entries) > BrowseMaxEntries {
		return BrowseResult{}, fmt.Errorf("media folder contains more than %d entries; choose a narrower folder", BrowseMaxEntries)
	}
	directories := make([]Directory, 0)
	clips := make([]Clip, 0)
	audioClips := make([]AudioClip, 0)
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		entryRelative := filepath.Join(relativePath, name)
		if entry.IsDir() {
			directories = append(directories, Directory{Name: name, RelativePath: entryRelative})
			continue
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil || !entryInfo.Mode().IsRegular() || entryInfo.Size() <= 0 || entryInfo.Size() > pebblestore.SessionVideoAttachmentMaxBytes {
			continue
		}
		extension := strings.ToLower(filepath.Ext(name))
		_, isVideo := acceptedVideoExtensions[extension]
		_, isAudio := acceptedAudioExtensions[extension]
		if !isVideo && !isAudio {
			continue
		}
		file, openErr := root.Open(entryRelative)
		if openErr != nil {
			continue
		}
		if isAudio {
			mimeType, mimeErr := pebblestore.DetectSupportedAudioMIME(file, extension)
			_ = file.Close()
			if mimeErr != nil {
				continue
			}
			record, putErr := s.store.PutAudioSourceRecord(pebblestore.AudioSourceRecord{
				AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID, RootPath: rootPath,
				RelativePath: entryRelative, DisplayName: name, MIMEType: mimeType,
				SizeBytes: entryInfo.Size(), ModifiedAt: entryInfo.ModTime().UnixMilli(),
			})
			if putErr != nil {
				return BrowseResult{}, putErr
			}
			audioClip := AudioClip{
				Ref: record.Ref, Name: record.DisplayName, MediaKind: MediaKindAudio, Extension: extension,
				MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, ModifiedAt: record.ModifiedAt,
				SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion,
			}
			if transcript, found, lookupErr := s.store.FindNormalizedTranscriptBySourceFingerprint(principal.AccountScopeID, principal.UserID, workspaceID, record.SourceFingerprint); lookupErr != nil {
				return BrowseResult{}, lookupErr
			} else if found {
				audioClip.TranscriptRef = transcript.Ref
			}
			if analysis, found, lookupErr := s.store.FindAudioAnalysisSnapshot(principal.AccountScopeID, workspaceID, "", record.SourceFingerprint); lookupErr != nil {
				return BrowseResult{}, lookupErr
			} else if found {
				audioClip.AnalysisRef = analysis.Ref
			}
			audioClips = append(audioClips, audioClip)
			continue
		}
		mimeType, mimeErr := videoMIMEForFile(file, extension)
		_ = file.Close()
		if mimeErr != nil {
			continue
		}
		record, putErr := s.store.PutVideoSourceRecord(pebblestore.VideoSourceRecord{
			AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID, RootPath: rootPath,
			RelativePath: entryRelative, DisplayName: name, MIMEType: mimeType,
			SizeBytes: entryInfo.Size(), ModifiedAt: entryInfo.ModTime().UnixMilli(),
		})
		if putErr != nil {
			return BrowseResult{}, putErr
		}
		clip := Clip{Ref: record.Ref, Name: record.DisplayName, MediaKind: MediaKindVideo, Extension: extension, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, ModifiedAt: record.ModifiedAt, SourceFingerprint: record.SourceFingerprint}
		if transcript, found, lookupErr := s.store.FindNormalizedTranscriptBySourceFingerprint(principal.AccountScopeID, principal.UserID, workspaceID, record.SourceFingerprint); lookupErr != nil {
			return BrowseResult{}, lookupErr
		} else if found {
			clip.TranscriptRef = transcript.Ref
		}
		clips = append(clips, clip)
	}
	sort.SliceStable(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})
	sort.SliceStable(clips, func(i, j int) bool { return strings.ToLower(clips[i].Name) < strings.ToLower(clips[j].Name) })
	sort.SliceStable(audioClips, func(i, j int) bool { return strings.ToLower(audioClips[i].Name) < strings.ToLower(audioClips[j].Name) })
	return BrowseResult{WorkspaceID: workspaceID, RootPath: rootPath, RootRef: rootRef, RelativePath: relativePath, Directories: directories, Clips: clips, AudioClips: audioClips}, nil
}

func ResolveRootPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("folder path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	value = filepath.Clean(absolute)
	info, err := os.Lstat(value)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("folder path must be a non-symlink directory")
	}
	return value, nil
}

func IsAcceptedVideoExtension(extension string) bool {
	_, ok := acceptedVideoExtensions[strings.ToLower(strings.TrimSpace(extension))]
	return ok
}

func IsAcceptedAudioExtension(extension string) bool {
	_, ok := acceptedAudioExtensions[strings.ToLower(strings.TrimSpace(extension))]
	return ok
}

func CleanRelativePath(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "" || value == "." {
		return ".", nil
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", errors.New("media browse path escapes the registered root")
	}
	if depth := len(strings.Split(value, string(filepath.Separator))); depth > BrowseMaxDepth {
		return "", fmt.Errorf("media browse depth exceeds %d", BrowseMaxDepth)
	}
	return value, nil
}

func rootReference(accountScopeID, workspaceID, rootPath string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID), filepath.Clean(rootPath)}, "\x00")))
	return "videosource_root_" + hex.EncodeToString(sum[:])
}

func videoMIMEForFile(file *os.File, extension string) (string, error) {
	var header [512]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(header[:n])))
	if strings.HasPrefix(detected, "video/") {
		return detected, nil
	}
	fallback := strings.ToLower(strings.TrimSpace(mime.TypeByExtension(extension)))
	if strings.HasPrefix(fallback, "video/") {
		return fallback, nil
	}
	return "", errors.New("file content is not a supported video MIME type")
}
