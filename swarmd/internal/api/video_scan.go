package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var acceptedVideoExtensions = map[string]struct{}{
	".avi":  {},
	".m4v":  {},
	".mkv":  {},
	".mov":  {},
	".mp4":  {},
	".mpeg": {},
	".mpg":  {},
	".webm": {},
}

type videoScanClip struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Extension  string `json:"extension"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt int64  `json:"modified_at"`
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
	info, err := os.Stat(folderPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("folder path must be a directory")
	}
	return folderPath, nil
}

func scanAcceptedVideoClips(folderPath string) (string, []videoScanClip, error) {
	folderPath, err := resolveVideoFolderPath(folderPath)
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return "", nil, err
	}
	clips := make([]videoScanClip, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := acceptedVideoExtensions[ext]; !ok {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return "", nil, err
		}
		clipPath := filepath.Join(folderPath, name)
		clips = append(clips, videoScanClip{
			ID:         clipPath,
			Name:       name,
			Path:       clipPath,
			Extension:  ext,
			SizeBytes:  entryInfo.Size(),
			ModifiedAt: entryInfo.ModTime().UnixMilli(),
		})
	}
	sort.SliceStable(clips, func(i, j int) bool {
		leftName := strings.ToLower(clips[i].Name)
		rightName := strings.ToLower(clips[j].Name)
		if leftName == rightName {
			return clips[i].Path < clips[j].Path
		}
		return leftName < rightName
	})
	return folderPath, clips, nil
}

func (s *Server) handleWorkspaceVideoScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		FolderPath    string `json:"folder_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolvedFolderPath, clips, err := scanAcceptedVideoClips(req.FolderPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"workspace_path": strings.TrimSpace(req.WorkspacePath),
		"folder_path":    resolvedFolderPath,
		"clips":          clips,
	})
}
