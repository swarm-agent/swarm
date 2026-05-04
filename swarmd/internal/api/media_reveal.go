package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func (s *Server) handleImageStorageReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.imageGen == nil {
		writeError(w, http.StatusInternalServerError, errors.New("image generation service is not configured"))
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	assetID := strings.TrimSpace(r.URL.Query().Get("asset_id"))
	var revealPath string
	if assetID != "" {
		assetPath, _, err := s.imageGen.ResolveAssetPath(threadID, assetID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		revealPath = assetPath
	} else {
		storagePath, _, err := s.imageGen.ResolveSessionStoragePath(threadID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		revealPath = storagePath
	}
	method, err := revealLocalPath(revealPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": revealPath, "method": method})
}

func (s *Server) handleVideoStorageReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.videoThreads == nil {
		writeError(w, http.StatusInternalServerError, errors.New("video thread store is not configured"))
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	clipID := strings.TrimSpace(r.URL.Query().Get("clip_id"))
	thread, ok, err := s.videoThreads.Get(threadID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("video thread not found"))
		return
	}
	var revealPath string
	if clipID != "" {
		clip, ok := findVideoThreadClip(thread, clipID)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("video clip not found"))
			return
		}
		revealPath, err = resolveVideoClipFilePath(clip.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	} else {
		if storagePath, ok := thread.Metadata["tool_storage_path"].(string); ok && strings.TrimSpace(storagePath) != "" {
			revealPath = filepath.Clean(strings.TrimSpace(storagePath))
		} else if len(thread.VideoFolders) > 0 {
			revealPath = filepath.Clean(strings.TrimSpace(thread.VideoFolders[0]))
		}
		if revealPath == "" || revealPath == "." {
			writeError(w, http.StatusBadRequest, errors.New("video session storage path is not available"))
			return
		}
	}
	method, err := revealLocalPath(revealPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": revealPath, "method": method})
}

func revealLocalPath(targetPath string) (string, error) {
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if targetPath == "" || targetPath == "." {
		return "", errors.New("path is required")
	}
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	absPath = filepath.Clean(absPath)
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("show in file manager is only implemented on Linux")
	}
	fileURI := "file://" + filepath.ToSlash(absPath)
	if dbusSend, err := exec.LookPath("dbus-send"); err == nil {
		cmd := exec.Command(dbusSend,
			"--session",
			"--dest=org.freedesktop.FileManager1",
			"--type=method_call",
			"/org/freedesktop/FileManager1",
			"org.freedesktop.FileManager1.ShowItems",
			fmt.Sprintf("array:string:%s", fileURI),
			"string:",
		)
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Release()
			return "freedesktop-file-manager-show-items", nil
		}
	}
	openPath := absPath
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		openPath = filepath.Dir(absPath)
	}
	if xdgOpen, err := exec.LookPath("xdg-open"); err == nil {
		cmd := exec.Command(xdgOpen, openPath)
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Release()
			return "xdg-open", nil
		}
	}
	return "", errors.New("no Linux file manager opener found (tried dbus-send and xdg-open)")
}
