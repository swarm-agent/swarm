package api

import (
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
)

const (
	videoSourceBrowseMaxEntries = 200
	videoSourceBrowseMaxDepth   = 16
)

var acceptedVideoExtensions = map[string]struct{}{
	".avi": {}, ".m4v": {}, ".mkv": {}, ".mov": {}, ".mp4": {}, ".mpeg": {}, ".mpg": {}, ".webm": {},
}

type videoScanClip struct {
	Ref               string `json:"ref"`
	Name              string `json:"name"`
	Extension         string `json:"extension"`
	MIMEType          string `json:"mime_type"`
	SizeBytes         int64  `json:"size_bytes"`
	ModifiedAt        int64  `json:"modified_at"`
	SourceFingerprint string `json:"source_fingerprint"`
}

type videoScanDirectory struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path"`
}

func resolveVideoFolderPath(folderPath string) (string, error) {
	folderPath = strings.TrimSpace(folderPath)
	if folderPath == "" {
		return "", errors.New("folder path is required")
	}
	absFolderPath, err := filepath.Abs(folderPath)
	if err != nil {
		return "", err
	}
	folderPath = filepath.Clean(absFolderPath)
	info, err := os.Lstat(folderPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("folder path must be a non-symlink directory")
	}
	return folderPath, nil
}

func cleanVideoBrowseRelativePath(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "" || value == "." {
		return ".", nil
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", errors.New("video browse path escapes the registered root")
	}
	if depth := len(strings.Split(value, string(filepath.Separator))); depth > videoSourceBrowseMaxDepth {
		return "", fmt.Errorf("video browse depth exceeds %d", videoSourceBrowseMaxDepth)
	}
	return value, nil
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

func scanRegisteredVideoDirectory(store *pebblestore.SessionStore, accountScopeID, workspaceID, rootPath, relativePath string) ([]videoScanDirectory, []videoScanClip, error) {
	rootPath, err := resolveVideoFolderPath(rootPath)
	if err != nil {
		return nil, nil, err
	}
	relativePath, err = cleanVideoBrowseRelativePath(relativePath)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	directory, err := root.Open(relativePath)
	if err != nil {
		return nil, nil, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return nil, nil, errors.New("video browse target is not a directory")
	}
	entries, err := directory.ReadDir(videoSourceBrowseMaxEntries + 1)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) > videoSourceBrowseMaxEntries {
		return nil, nil, fmt.Errorf("video folder contains more than %d entries; choose a narrower folder", videoSourceBrowseMaxEntries)
	}
	directories := make([]videoScanDirectory, 0)
	clips := make([]videoScanClip, 0)
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		entryRelative := filepath.Join(relativePath, name)
		if entry.IsDir() {
			directories = append(directories, videoScanDirectory{Name: name, RelativePath: entryRelative})
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() || entryInfo.Size() <= 0 || entryInfo.Size() > pebblestore.SessionVideoAttachmentMaxBytes {
			continue
		}
		extension := strings.ToLower(filepath.Ext(name))
		if _, ok := acceptedVideoExtensions[extension]; !ok {
			continue
		}
		file, err := root.Open(entryRelative)
		if err != nil {
			continue
		}
		mimeType, mimeErr := videoMIMEForFile(file, extension)
		file.Close()
		if mimeErr != nil {
			continue
		}
		record, err := store.PutVideoSourceRecord(pebblestore.VideoSourceRecord{
			AccountScopeID: accountScopeID, WorkspaceID: workspaceID, RootPath: rootPath,
			RelativePath: entryRelative, DisplayName: name, MIMEType: mimeType,
			SizeBytes: entryInfo.Size(), ModifiedAt: entryInfo.ModTime().UnixMilli(),
		})
		if err != nil {
			return nil, nil, err
		}
		clips = append(clips, videoScanClip{Ref: record.Ref, Name: record.DisplayName, Extension: extension, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, ModifiedAt: record.ModifiedAt, SourceFingerprint: record.SourceFingerprint})
	}
	sort.SliceStable(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})
	sort.SliceStable(clips, func(i, j int) bool { return strings.ToLower(clips[i].Name) < strings.ToLower(clips[j].Name) })
	return directories, clips, nil
}

func (s *Server) handleWorkspaceVideoScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if s.workspace == nil || s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace video source services are not configured"))
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		RootPath      string `json:"root_path"`
		RelativePath  string `json:"relative_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, req.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requestedRoot, err := resolveVideoFolderPath(req.RootPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	registered := false
	for _, candidate := range resolution.SourceMediaDirectories {
		if filepath.Clean(candidate) == requestedRoot {
			registered = true
			break
		}
	}
	if !registered {
		writeError(w, http.StatusBadRequest, errors.New("video folder is not a registered source-media root for this workspace"))
		return
	}
	directories, clips, err := scanRegisteredVideoDirectory(s.sessions.Store(), principal.AccountScopeID, resolution.WorkspaceID, requestedRoot, req.RelativePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	relativePath, _ := cleanVideoBrowseRelativePath(req.RelativePath)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "workspace_id": resolution.WorkspaceID, "root_path": requestedRoot,
		"relative_path": relativePath, "directories": directories, "clips": clips,
	})
}
