package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	assetID := strings.TrimSpace(r.URL.Query().Get("asset_id"))
	var revealPath string
	if assetID != "" {
		assetPath, _, err := s.imageGen.ResolveAssetPathForPrincipal(principal, threadID, assetID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		revealPath = assetPath
	} else {
		storagePath, _, err := s.imageGen.ResolveSessionStoragePathForPrincipal(principal, threadID)
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
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	clipID := strings.TrimSpace(r.URL.Query().Get("clip_id"))
	thread, ok, err := s.videoThreads.GetForAccount(principal.AccountScopeID, threadID)
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
		clipPath, file, openErr := openManagedVideoClip(thread, clip.Path)
		if openErr != nil {
			writeError(w, http.StatusBadRequest, openErr)
			return
		}
		revealPath = clipPath
		file.Close()
	} else {
		revealPath, err = managedVideoStoragePath(thread)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		root, openErr := os.OpenRoot(revealPath)
		if openErr != nil {
			writeError(w, http.StatusBadRequest, openErr)
			return
		}
		root.Close()
	}
	method, err := revealLocalPath(revealPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": revealPath, "method": method})
}

type revealLocalPathDependencies struct {
	lookPath func(string) (string, error)
	run      func(string, []string, []string) error
}

func revealLocalPath(targetPath string) (string, error) {
	return revealLocalPathWithDependencies(targetPath, revealLocalPathDependencies{
		lookPath: exec.LookPath,
		run: func(executable string, args, env []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, args...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				return fmt.Errorf("timed out waiting for native opener: %w", ctx.Err())
			}
			if err != nil {
				detail := strings.TrimSpace(string(output))
				if detail != "" {
					return fmt.Errorf("%w: %s", err, detail)
				}
			}
			return err
		},
	})
}

func revealLocalPathWithDependencies(targetPath string, dependencies revealLocalPathDependencies) (string, error) {
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if targetPath == "" || targetPath == "." {
		return "", errors.New("path is required")
	}
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	absPath = filepath.Clean(absPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("inspect reveal path: %w", err)
	}
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("show in file manager is only implemented on Linux")
	}
	if dependencies.lookPath == nil || dependencies.run == nil {
		return "", errors.New("native file manager opener is not configured")
	}

	env := localDesktopSessionEnvironment()
	fileURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String()
	failures := make([]string, 0, 3)
	if dbusSend, lookupErr := dependencies.lookPath("dbus-send"); lookupErr == nil {
		method := "ShowItems"
		methodLabel := "freedesktop-file-manager-show-items"
		if info.IsDir() {
			method = "ShowFolders"
			methodLabel = "freedesktop-file-manager-show-folders"
		}
		args := []string{
			"--session",
			"--print-reply",
			"--reply-timeout=5000",
			"--dest=org.freedesktop.FileManager1",
			"--type=method_call",
			"/org/freedesktop/FileManager1",
			"org.freedesktop.FileManager1." + method,
			fmt.Sprintf("array:string:%s", fileURI),
			"string:",
		}
		if runErr := dependencies.run(dbusSend, args, env); runErr == nil {
			return methodLabel, nil
		} else {
			failures = append(failures, "FileManager1: "+runErr.Error())
		}
	}

	openPath := absPath
	if !info.IsDir() {
		openPath = filepath.Dir(absPath)
	}
	for _, candidate := range []string{"gio", "xdg-open"} {
		executable, lookupErr := dependencies.lookPath(candidate)
		if lookupErr != nil {
			continue
		}
		args := []string{openPath}
		if candidate == "gio" {
			args = []string{"open", openPath}
		}
		if runErr := dependencies.run(executable, args, env); runErr == nil {
			return candidate, nil
		} else {
			failures = append(failures, candidate+": "+runErr.Error())
		}
	}
	if len(failures) == 0 {
		return "", errors.New("no Linux file manager opener found (tried dbus-send, gio, and xdg-open)")
	}
	return "", fmt.Errorf("native file manager did not open the folder (%s)", strings.Join(failures, "; "))
}

func localDesktopSessionEnvironment() []string {
	env := os.Environ()
	if runtime.GOOS != "linux" {
		return env
	}
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		candidate := filepath.Join(string(filepath.Separator), "run", "user", strconv.Itoa(os.Getuid()))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			runtimeDir = candidate
			env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
		}
	}
	if strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS")) == "" && runtimeDir != "" {
		bus := filepath.Join(runtimeDir, "bus")
		if info, err := os.Stat(bus); err == nil && info.Mode()&os.ModeSocket != 0 {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+bus)
		}
	}
	return env
}
