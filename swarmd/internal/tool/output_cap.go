package tool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Tool output cap: the bound on the text one tool result gives the model.
// One setting (tool_output_cap_bytes in the startup config) governs every
// tool that can return unbounded text: bash (foreground, background status
// and the report delivered when a background command ends), read without an
// explicit max_lines, search, and webfetch/websearch page text. Text over the
// cap keeps its head and tail around a marker; for command output the full
// text is saved to a file the agent reads selectively with read/search.
const (
	// DefaultToolOutputCapBytes is the cap when the setting is unset (0).
	DefaultToolOutputCapBytes = 32 * 1024
	// MinToolOutputCapBytes and MaxToolOutputCapBytes bound the setting.
	MinToolOutputCapBytes = 4 * 1024
	MaxToolOutputCapBytes = 1024 * 1024

	// toolOutputMarkerReserve is kept out of the head and tail so the
	// marker fits within the cap.
	toolOutputMarkerReserve = 512
	// maxSavedToolOutputs bounds the saved full outputs kept per workspace;
	// the oldest are removed first.
	maxSavedToolOutputs = 50
)

// toolOutputDirParts is the workspace-relative directory full outputs are
// saved in. It ignores itself in Git (a .gitignore of "*"), so saved output
// never shows up as a change or gets committed.
var toolOutputDirParts = []string{".swarm", "tool-output"}

var toolOutputCapBytes atomic.Int64

// SetToolOutputCapBytes sets the cap. 0 selects DefaultToolOutputCapBytes;
// any other value outside [MinToolOutputCapBytes, MaxToolOutputCapBytes] is
// refused and leaves the cap unchanged.
func SetToolOutputCapBytes(n int) error {
	if err := ValidateToolOutputCapBytes(n); err != nil {
		return err
	}
	toolOutputCapBytes.Store(int64(n))
	return nil
}

// ValidateToolOutputCapBytes reports whether n is an accepted cap setting.
func ValidateToolOutputCapBytes(n int) error {
	if n == 0 || (n >= MinToolOutputCapBytes && n <= MaxToolOutputCapBytes) {
		return nil
	}
	return fmt.Errorf("tool output cap %d bytes is outside %d..%d (0 selects the default %d)", n, MinToolOutputCapBytes, MaxToolOutputCapBytes, DefaultToolOutputCapBytes)
}

// ToolOutputCapBytes is the cap in effect.
func ToolOutputCapBytes() int {
	if n := toolOutputCapBytes.Load(); n > 0 {
		return int(n)
	}
	return DefaultToolOutputCapBytes
}

// cappedToolText is text after the cap: unchanged when it fit, otherwise
// head + marker + tail, with where the full text was saved.
type cappedToolText struct {
	Text          string
	Capped        bool
	OriginalBytes int
	OmittedBytes  int
	SavedPath     string
	SaveError     string
}

// annotate adds the cap's fields to a tool response when the text was cut.
func (c cappedToolText) annotate(response map[string]any) {
	if !c.Capped || response == nil {
		return
	}
	response["output_capped"] = true
	response["output_original_bytes"] = c.OriginalBytes
	response["output_omitted_bytes"] = c.OmittedBytes
	response["output_cap_bytes"] = ToolOutputCapBytes()
	if c.SavedPath != "" {
		response["full_output_path"] = c.SavedPath
	}
	if c.SaveError != "" {
		response["full_output_error"] = c.SaveError
	}
}

// capToolOutputText applies the cap to text. Text over the cap keeps its
// head and its tail (where errors and summaries usually are) around a
// marker naming how much was omitted and, when save is true, the file the
// full text was saved to in the scope's workspace.
func capToolOutputText(scope WorkspaceScope, label, text string, save bool) cappedToolText {
	return capToolText(text, func(out *cappedToolText, omitted int) string {
		if !save {
			return fmt.Sprintf("[%d bytes omitted]", omitted)
		}
		path, err := saveToolOutput(scope, label, text)
		if err != nil {
			out.SaveError = err.Error()
			return fmt.Sprintf("[%d bytes omitted; full output could not be saved: %s]", omitted, clipBytes(err.Error(), 200))
		}
		out.SavedPath = path
		return fmt.Sprintf("[%d bytes omitted; full output saved to %s; read it with read/search]", omitted, path)
	})
}

// capToolOutputTextUnsaved applies the cap where the full text cannot be
// saved anywhere the agent can read it (for example a command that ran in
// a managed lease, whose read/search do not see the host workspace). The
// marker says plainly that the full output is unavailable and why; no path
// is offered.
func capToolOutputTextUnsaved(text, reason string) cappedToolText {
	return capToolText(text, func(out *cappedToolText, omitted int) string {
		out.SaveError = reason
		return fmt.Sprintf("[%d bytes omitted; full output unavailable: %s]", omitted, reason)
	})
}

func capToolText(text string, marker func(out *cappedToolText, omitted int) string) cappedToolText {
	out := cappedToolText{Text: text, OriginalBytes: len(text)}
	limit := ToolOutputCapBytes()
	if len(text) <= limit {
		return out
	}
	budget := limit - toolOutputMarkerReserve
	head := headAtBoundary(text, budget/2)
	tail := tailAtBoundary(text, budget-budget/2)
	omitted := len(text) - len(head) - len(tail)
	out.Text = head + "\n..." + marker(&out, omitted) + "...\n" + tail
	out.Capped = true
	out.OmittedBytes = omitted
	return out
}

// headAtBoundary is at most n bytes from the start of text, ending at a line
// break when one is in its second half and never inside a UTF-8 sequence.
func headAtBoundary(text string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(text) {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	head := text[:n]
	if i := strings.LastIndexByte(head, '\n'); i >= n/2 {
		head = head[:i+1]
	}
	return head
}

// tailAtBoundary is at most n bytes from the end of text, starting after a
// line break when one is in its first half and never inside a UTF-8 sequence.
func tailAtBoundary(text string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(text) {
		return text
	}
	start := len(text) - n
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	tail := text[start:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)/2 {
		tail = tail[i+1:]
	}
	return tail
}

// toolOutputSaveTestHook, when set by a test, runs after the tool-output
// directory is opened and before anything is written in it, so a test can
// replace path components at the point an agent's Bash could race the save.
var toolOutputSaveTestHook func()

// saveToolOutput writes text to a new file in the workspace's tool-output
// directory and returns its path. The workspace is agent-writable, so every
// operation goes through os.Root handles: each directory level is opened as
// a root, must be a real directory (not a symlink) at the moment it is
// opened, and all later create/list/remove calls are relative to the opened
// directory, so a component swapped for a symlink afterwards cannot redirect
// them. After the write the path is checked again; when a level was replaced
// meanwhile, the file is removed (from the directory actually written to)
// and the save fails rather than report a path that leads elsewhere.
func saveToolOutput(scope WorkspaceScope, label, text string) (string, error) {
	primary := strings.TrimSpace(scope.PrimaryPath)
	if primary == "" || !filepath.IsAbs(primary) {
		return "", errors.New("no workspace to save it in")
	}
	primary = filepath.Clean(primary)
	dirPath := filepath.Join(append([]string{primary}, toolOutputDirParts...)...)
	if err := refuseProtectedPath(dirPath); err != nil {
		return "", err
	}
	if err := refuseRootContainingStorage(primary); err != nil {
		return "", err
	}
	workspace, err := os.OpenRoot(primary)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	defer workspace.Close()
	levels, err := openRealSubdirectories(workspace, toolOutputDirParts)
	defer func() {
		for _, level := range levels {
			_ = level.Close()
		}
	}()
	if err != nil {
		return "", err
	}
	dir := levels[len(levels)-1]
	if toolOutputSaveTestHook != nil {
		toolOutputSaveTestHook()
	}
	if err := writeRootedExclusive(dir, ".gitignore", "*\n"); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("write tool-output .gitignore: %w", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s.txt", time.Now().UTC().Format("20060102T150405.000000000Z"), toolOutputLabel(label), hex.EncodeToString(suffix))
	if err := writeRootedExclusive(dir, name, text); err != nil {
		return "", err
	}
	if err := confirmRealSubdirectories(workspace, toolOutputDirParts, levels); err != nil {
		_ = dir.Remove(name)
		return "", err
	}
	pruneSavedToolOutputs(dir)
	return filepath.Join(dirPath, name), nil
}

// openRealSubdirectories opens parts below workspace one level at a time,
// creating missing levels (mode 0700). Each level must be a real directory:
// the name's Lstat (no symlink followed) must be the same file as the
// opened handle. The returned handles, outermost first, must be closed by
// the caller, also on error.
func openRealSubdirectories(workspace *os.Root, parts []string) ([]*os.Root, error) {
	var levels []*os.Root
	parent := workspace
	for _, part := range parts {
		if err := parent.Mkdir(part, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return levels, err
		}
		level, err := parent.OpenRoot(part)
		if err != nil {
			return levels, err
		}
		levels = append(levels, level)
		if err := sameRealDirectory(parent, part, level); err != nil {
			return levels, err
		}
		parent = level
	}
	return levels, nil
}

// confirmRealSubdirectories checks that each name still leads to the
// directory opened for it.
func confirmRealSubdirectories(workspace *os.Root, parts []string, levels []*os.Root) error {
	parent := workspace
	for i, part := range parts {
		if err := sameRealDirectory(parent, part, levels[i]); err != nil {
			return fmt.Errorf("tool-output directory was replaced during the save: %w", err)
		}
		parent = levels[i]
	}
	return nil
}

func sameRealDirectory(parent *os.Root, name string, opened *os.Root) error {
	named, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if named.Mode()&os.ModeSymlink != 0 || !named.IsDir() {
		return fmt.Errorf("%s is not a directory", name)
	}
	actual, err := opened.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(named, actual) {
		return fmt.Errorf("%s changed while it was opened", name)
	}
	return nil
}

// writeRootedExclusive creates name in dir (mode 0600), failing when
// anything exists there.
func writeRootedExclusive(dir *os.Root, name, text string) error {
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(text); err != nil {
		_ = file.Close()
		_ = dir.Remove(name)
		return err
	}
	return file.Close()
}

// pruneSavedToolOutputs removes the oldest saved outputs beyond
// maxSavedToolOutputs from the opened directory. Names start with a UTC
// timestamp, so name order is age order. Only regular .txt files are touched.
func pruneSavedToolOutputs(dir *os.Root) {
	handle, err := dir.Open(".")
	if err != nil {
		return
	}
	entries, err := handle.ReadDir(-1)
	_ = handle.Close()
	if err != nil {
		return
	}
	var saved []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".txt") {
			saved = append(saved, entry.Name())
		}
	}
	if len(saved) <= maxSavedToolOutputs {
		return
	}
	sort.Strings(saved)
	for _, name := range saved[:len(saved)-maxSavedToolOutputs] {
		_ = dir.Remove(name)
	}
}

func toolOutputLabel(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 24 {
			break
		}
	}
	if b.Len() == 0 {
		return "output"
	}
	return b.String()
}
