package imagegenlog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
)

var (
	appendMu sync.Mutex

	secretValuePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)([?&](?:key|api[_-]?key|apikey|google[_-]?api[_-]?key|x-goog-api-key|access[_-]?token|refresh[_-]?token|id[_-]?token|token)=)([^&\s"'\\]+)`),
		regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*bearer\s+)([A-Za-z0-9._~+/=-]+)`),
		regexp.MustCompile(`(?i)\b((?:api[_-]?key|apikey|google[_-]?api[_-]?key|x-goog-api-key|access[_-]?token|refresh[_-]?token|id[_-]?token|token)\s*[:=]\s*["']?)([^"',\s}\\]+)`),
	}
)

// Printf writes an image generation diagnostic line to both the daemon log and
// the durable image generation diagnostic file. Keep payload bytes out of the
// formatted message; this sink is for shapes, counts, paths, and failure causes.
func Printf(component, format string, args ...any) {
	prefix := "[swarmd.imagegen]"
	if component != "" {
		prefix = "[swarmd." + component + ".imagegen]"
	}
	message := sanitizeMessage(fmt.Sprintf(prefix+" "+format, args...))
	log.Print(message)
	Append(message)
}

// Append writes a preformatted diagnostic message to the shared imagegen log.
func Append(message string) {
	message = sanitizeMessage(message)
	path, err := Path()
	if err != nil {
		log.Printf("[swarmd.imagegen] stage=diagnostic_log_path_failed reason=%q", err.Error())
		return
	}
	line := time.Now().Format(time.RFC3339Nano) + " " + message + "\n"

	appendMu.Lock()
	defer appendMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), appstorage.PrivateDirPerm); err != nil {
		log.Printf("[swarmd.imagegen] stage=diagnostic_log_write_failed reason=%q path=%q", err.Error(), path)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, appstorage.PrivateFilePerm)
	if err != nil {
		log.Printf("[swarmd.imagegen] stage=diagnostic_log_write_failed reason=%q path=%q", err.Error(), path)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("[swarmd.imagegen] stage=diagnostic_log_close_failed reason=%q path=%q", err.Error(), path)
		}
	}()
	if err := file.Chmod(appstorage.PrivateFilePerm); err != nil {
		log.Printf("[swarmd.imagegen] stage=diagnostic_log_chmod_failed reason=%q path=%q", err.Error(), path)
	}
	if _, err := file.WriteString(line); err != nil {
		log.Printf("[swarmd.imagegen] stage=diagnostic_log_write_failed reason=%q path=%q", err.Error(), path)
	}
}

func sanitizeMessage(message string) string {
	for _, pattern := range secretValuePatterns {
		message = pattern.ReplaceAllString(message, `${1}[REDACTED]`)
	}
	return message
}

// Path returns the durable image generation diagnostics log path.
func Path() (string, error) {
	dir, err := appstorage.DataDir("main")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "imagegen.log"), nil
}
