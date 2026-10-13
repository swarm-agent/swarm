// Package publish delivers a job's commits as a pull request instead of a
// local merge, for folders whose owner set a publish command on the machine
// (`install.sh workspace publish PATH COMMAND`). Swarm never pushes by itself
// and agents never see the command or its credentials: the daemon runs the
// owner's command on the host with the job's branch, head and report, and
// records the pull request it prints. Merging stays with the forge's own rules.
package publish

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// ConfigEnv names the file the daemon reads publish rules from.
const ConfigEnv = "SWARM_PUBLISH_CONFIG"

// Rule is one folder's publish setting.
type Rule struct {
	Folder  string `json:"folder"`
	Command string `json:"command"`
	// Auto delivers a scheduled job that finished with commits without
	// waiting for an AI to call swarm_deliver_job.
	Auto bool `json:"auto"`
}

// Request is what the command is told, as SWARM_PUBLISH_* variables.
type Request struct {
	Folder     string
	JobID      string
	Branch     string // the job's branch in Folder
	Head       string // the commit to publish
	BaseBranch string // the branch the job started from
	Title      string
	Body       string // the job's report
}

// Result is what the command printed: the pull request it opened or updated.
type Result struct {
	URL    string `json:"url"`
	Output string `json:"output,omitempty"`
}

var (
	urlPattern    = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._~%/+-]+$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// Load reads the rules: one per line, `FOLDER<TAB>COMMAND[<TAB>auto]`, blank
// lines and #comments ignored. A missing file is no rules. The file must be
// owned by root and not writable by anyone else, like the commands it names:
// only the machine's administrator decides what runs.
func Load(path string) ([]Rule, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if err := ownedByRoot(path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("publish config: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("publish config: %w", err)
	}
	rules := []Rule{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("publish config line %d: want FOLDER<TAB>COMMAND[<TAB>auto]", line)
		}
		rule := Rule{Folder: filepath.Clean(strings.TrimSpace(fields[0])), Command: strings.TrimSpace(fields[1])}
		if len(fields) == 3 {
			if strings.TrimSpace(fields[2]) != "auto" {
				return nil, fmt.Errorf("publish config line %d: third field must be auto", line)
			}
			rule.Auto = true
		}
		if !filepath.IsAbs(rule.Folder) || !filepath.IsAbs(rule.Command) {
			return nil, fmt.Errorf("publish config line %d: folder and command must be absolute paths", line)
		}
		rules = append(rules, rule)
	}
	return rules, scanner.Err()
}

// For is the rule of the folder a job ran in, if any.
func For(rules []Rule, folder string) (Rule, bool) {
	folder = filepath.Clean(folder)
	for _, rule := range rules {
		if rule.Folder == folder {
			return rule, true
		}
	}
	return Rule{}, false
}

// Run runs the rule's command for one job and returns the pull request it
// printed on its last line. The command runs on the host as the daemon's user,
// in the folder, with a minimal environment and a time limit.
func Run(ctx context.Context, rule Rule, req Request) (Result, error) {
	if err := ownedByRoot(rule.Command, true); err != nil {
		return Result{}, fmt.Errorf("publish command %s: %w", rule.Command, err)
	}
	if !commitPattern.MatchString(req.Head) || !branchPattern.MatchString(req.Branch) || (req.BaseBranch != "" && !branchPattern.MatchString(req.BaseBranch)) {
		return Result{}, errors.New("publish: the job has no valid branch and head commit")
	}
	body, err := os.CreateTemp("", "swarm-publish-body-*")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(body.Name())
	if _, err := body.WriteString(req.Body); err != nil {
		body.Close()
		return Result{}, err
	}
	if err := body.Close(); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, rule.Command)
	cmd.Dir = rule.Folder
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + os.Getenv("HOME"),
		"SWARM_PUBLISH_FOLDER=" + rule.Folder,
		"SWARM_PUBLISH_JOB_ID=" + req.JobID,
		"SWARM_PUBLISH_BRANCH=" + req.Branch,
		"SWARM_PUBLISH_HEAD=" + req.Head,
		"SWARM_PUBLISH_BASE_BRANCH=" + req.BaseBranch,
		"SWARM_PUBLISH_TITLE=" + oneLine(req.Title, 200),
		"SWARM_PUBLISH_BODY_FILE=" + body.Name(),
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limited{buf: &stdout, max: 64 << 10}, &limited{buf: &stderr, max: 64 << 10}
	runErr := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	if runErr != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = output
		}
		return Result{Output: tail(reason, 600)}, fmt.Errorf("publish command failed: %v: %s", runErr, tail(reason, 300))
	}
	lines := strings.Split(output, "\n")
	url := strings.TrimSpace(lines[len(lines)-1])
	if !urlPattern.MatchString(url) {
		return Result{Output: tail(output, 600)}, errors.New("publish command did not print the pull request URL on its last line")
	}
	return Result{URL: url, Output: tail(output, 600)}, nil
}

// ownedByRoot refuses a file another user could change. Tests may set
// allowOwner to accept files owned by the current user.
func ownedByRoot(path string, executable bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read its owner")
	}
	if stat.Uid != 0 && int(stat.Uid) != allowOwner {
		return errors.New("must be owned by root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("must not be writable by group or others")
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return errors.New("is not executable")
	}
	return nil
}

// allowOwner is -1 outside tests.
var allowOwner = -1

func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}

func tail(text string, limit int) string {
	if len(text) > limit {
		return text[len(text)-limit:]
	}
	return text
}

type limited struct {
	buf *bytes.Buffer
	max int
}

func (w *limited) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
