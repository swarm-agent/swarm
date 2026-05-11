package flowdiaglog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
)

var (
	appendMu sync.Mutex

	secretValuePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)([?&](?:key|api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|id[_-]?token|token)=)([^&\s"'\\]+)`),
		regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*bearer\s+)([A-Za-z0-9._~+/=-]+)`),
		regexp.MustCompile(`(?i)\b((?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|id[_-]?token|token)\s*[:=]\s*["']?)([^"',\s}\\]+)`),
	}
)

// Printf writes V3 flow assignment diagnostics to the daemon log and to a
// durable disk file. Keep this limited to IDs, paths, states, and errors.
func Printf(stage, format string, args ...any) {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "unknown"
	}
	message := sanitizeMessage(fmt.Sprintf("[swarmd.flowdiag] stage=%q "+format, append([]any{stage}, args...)...))
	log.Print(message)
	Append(message)
}

// Append writes a preformatted diagnostic message to the shared flow diagnostics log.
func Append(message string) {
	message = sanitizeMessage(message)
	path, err := Path()
	if err != nil {
		log.Printf("[swarmd.flowdiag] stage=%q reason=%q", "diagnostic_log_path_failed", err.Error())
		return
	}
	line := time.Now().Format(time.RFC3339Nano) + " " + message + "\n"

	appendMu.Lock()
	defer appendMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), appstorage.PrivateDirPerm); err != nil {
		log.Printf("[swarmd.flowdiag] stage=%q reason=%q path=%q", "diagnostic_log_write_failed", err.Error(), path)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, appstorage.PrivateFilePerm)
	if err != nil {
		log.Printf("[swarmd.flowdiag] stage=%q reason=%q path=%q", "diagnostic_log_write_failed", err.Error(), path)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("[swarmd.flowdiag] stage=%q reason=%q path=%q", "diagnostic_log_close_failed", err.Error(), path)
		}
	}()
	if err := file.Chmod(appstorage.PrivateFilePerm); err != nil {
		log.Printf("[swarmd.flowdiag] stage=%q reason=%q path=%q", "diagnostic_log_chmod_failed", err.Error(), path)
	}
	if _, err := file.WriteString(line); err != nil {
		log.Printf("[swarmd.flowdiag] stage=%q reason=%q path=%q", "diagnostic_log_write_failed", err.Error(), path)
	}
}

func sanitizeMessage(message string) string {
	for _, pattern := range secretValuePatterns {
		message = pattern.ReplaceAllString(message, `${1}[REDACTED]`)
	}
	return message
}

// Path returns the durable V3 flow assignment diagnostics log path.
func Path() (string, error) {
	dir, err := appstorage.DataDir("main")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "flow-assignments.log"), nil
}
