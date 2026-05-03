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

func scanAcceptedVideoClips(folderPath string) ([]videoScanClip, error) {
	folderPath = strings.TrimSpace(folderPath)
	if folderPath == "" {
		return nil, errors.New("folder path is required")
	}
	absFolderPath, err := filepath.Abs(folderPath)
	if err != nil {
		return nil, err
	}
	folderPath, err = filepath.EvalSymlinks(absFolderPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(folderPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("folder path must be a directory")
	}
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
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
			return nil, err
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
	return clips, nil
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
	clips, err := scanAcceptedVideoClips(req.FolderPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolvedFolderPath := ""
	if len(clips) > 0 {
		resolvedFolderPath = filepath.Dir(clips[0].Path)
	} else {
		resolvedFolderPath, err = filepath.Abs(strings.TrimSpace(req.FolderPath))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolvedFolderPath, err = filepath.EvalSymlinks(resolvedFolderPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"workspace_path": strings.TrimSpace(req.WorkspacePath),
		"folder_path":    resolvedFolderPath,
		"clips":          clips,
	})
}
