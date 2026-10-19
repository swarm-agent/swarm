// Package publish delivers a job's commits as a pull request instead of a
// local merge, for folders whose owner set a publish command on the machine
// (`swarmctl workspace publish PATH COMMAND`). Swarm never pushes by itself
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
	"log"
	"os"
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
	// Sync starts each worker job from the forge's latest base branch (the
	// command's update step) instead of the folder's checked-out branch, so a
	// pull request merged on the forge is the next job's starting point.
	Sync bool `json:"sync"`
}

// Base is where the command says a job should start: a commit it fetched into
// the folder and the forge branch it is the tip of.
type Base struct {
	Commit string `json:"commit"`
	Branch string `json:"branch"`
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

// removedOptions are rule options of removed features. A rule file written
// for an older Swarm still loads: each is ignored with one log line.
var removedOptions = map[string]bool{"land": true, "auto": true, "require-proof": true, "require-review": true, "pipeline": true}

// Load reads the rules: one per line, `FOLDER<TAB>COMMAND[<TAB>OPTIONS]`, where
// OPTIONS is a comma-separated list; sync is the only option. Blank lines and
// #comments are ignored. A missing file is no rules. The file must be
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
			return nil, fmt.Errorf("publish config line %d: want FOLDER<TAB>COMMAND[<TAB>OPTIONS]", line)
		}
		rule := Rule{Folder: filepath.Clean(strings.TrimSpace(fields[0])), Command: strings.TrimSpace(fields[1])}
		if len(fields) == 3 {
			var ignored []string
			for _, option := range strings.Split(fields[2], ",") {
				option = strings.TrimSpace(option)
				switch {
				case option == "sync":
					rule.Sync = true
				case removedOptions[option]:
					ignored = append(ignored, option)
				default:
					return nil, fmt.Errorf("publish config line %d: the only option is sync", line)
				}
			}
			if len(ignored) > 0 {
				log.Printf("publish config line %d: ignoring removed options %s", line, strings.Join(ignored, ","))
			}
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
	scratch, err := os.MkdirTemp("", "swarm-publish-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(scratch)
	body := filepath.Join(scratch, "body")
	if err := os.WriteFile(body, []byte(req.Body), 0o600); err != nil {
		return Result{}, err
	}
	output, err := command(ctx, rule, scratch, 5*time.Minute, "publish",
		"SWARM_PUBLISH_JOB_ID="+req.JobID,
		"SWARM_PUBLISH_BRANCH="+req.Branch,
		"SWARM_PUBLISH_HEAD="+req.Head,
		"SWARM_PUBLISH_BASE_BRANCH="+req.BaseBranch,
		"SWARM_PUBLISH_TITLE="+oneLine(req.Title, 200),
		"SWARM_PUBLISH_BODY_FILE="+body,
	)
	if err != nil {
		return Result{Output: tail(output, 600)}, err
	}
	url := lastLine(output)
	if !urlPattern.MatchString(url) {
		return Result{Output: tail(output, 600)}, errors.New("publish command did not print the pull request URL on its last line")
	}
	return Result{URL: url, Output: tail(output, 600)}, nil
}

// Update runs the rule's command's update step before a job starts: it brings
// the forge's base branch into the folder and prints `COMMIT BRANCH` on its
// last line, the commit the job starts from and the branch its pull request
// targets. current is the folder's checked-out branch, the base when the
// command has none of its own. A failure stops the job from starting: a job
// on a stale base would open a pull request that cannot merge.
func Update(ctx context.Context, rule Rule, current string) (Base, error) {
	if err := ownedByRoot(rule.Command, true); err != nil {
		return Base{}, fmt.Errorf("publish command %s: %w", rule.Command, err)
	}
	if current != "" && !branchPattern.MatchString(current) {
		return Base{}, errors.New("publish: the folder's branch is not a branch name")
	}
	scratch, err := os.MkdirTemp("", "swarm-publish-*")
	if err != nil {
		return Base{}, err
	}
	defer os.RemoveAll(scratch)
	output, err := command(ctx, rule, scratch, 2*time.Minute, "update", "SWARM_PUBLISH_BASE_BRANCH="+current)
	if err != nil {
		return Base{}, err
	}
	fields := strings.Fields(lastLine(output))
	if len(fields) != 2 || !commitPattern.MatchString(fields[0]) || !branchPattern.MatchString(fields[1]) {
		return Base{}, errors.New("publish command's update step did not print COMMIT BRANCH on its last line")
	}
	return Base{Commit: fields[0], Branch: fields[1]}, nil
}

// command runs the rule's command on the host as the daemon's user, in the
// folder, with a minimal environment, its own scratch folder and a time limit,
// and returns what it printed.
func command(ctx context.Context, rule Rule, scratch string, limit time.Duration, action string, env ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	_, runErr := RunScript(ctx, Script{
		Path: rule.Command, Dir: rule.Folder, Scratch: scratch, Timeout: limit,
		Env:    append([]string{"SWARM_PUBLISH_ACTION=" + action, "SWARM_PUBLISH_FOLDER=" + rule.Folder}, env...),
		Stdout: &limited{buf: &stdout, max: 64 << 10}, Stderr: &limited{buf: &stderr, max: 64 << 10},
	})
	output := strings.TrimSpace(stdout.String())
	if runErr != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = output
		}
		return reason, fmt.Errorf("publish command failed: %v: %s", runErr, tail(reason, 300))
	}
	return output, nil
}

func lastLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// ownedByRoot refuses a file another user could change. Tests may set
// allowOwner to accept files owned by the current user.
func ownedByRoot(path string, executable bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrNoOwner
	}
	if stat.Uid != 0 && int(stat.Uid) != allowOwner {
		return ErrNotRootOwned
	}
	if info.Mode().Perm()&0o022 != 0 {
		return ErrWritable
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return ErrNotExecutable
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
