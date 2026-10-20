package jobautomation

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// When a job end triggers an automation.
const (
	WhenSucceeded = "succeeded"
	WhenFailed    = "failed"
	WhenAlways    = "always"
)

// Step types.
const (
	StepRun    = "run"
	StepAgent  = "agent"
	StepReopen = "reopen"
)

// Review verdicts an agent step's outcome may carry.
const (
	VerdictOK    = "ok"
	VerdictBlock = "block"
)

// Reviewer fixes: an agent step whose worker is a reviewer may set fixes to
// FixesSecurity (the only value), so the reviewer may commit small,
// security-only fixes on top of the reviewed head instead of blocking for
// them. Its commits move the delivery head only with an OK verdict and at
// most MaxReviewerFixLines changed lines (added plus removed).
const (
	FixesSecurity       = "security"
	MaxReviewerFixLines = 80
	ReasonFixesTooLarge = "reviewer fixes too large"
	// ReasonFixesUnreadable fails an OK verdict from a reviewer allowed to
	// fix whose own checkout (head and fix commits) could not be read: an
	// OK may mean "I fixed it", so the unfixed head is never delivered on it.
	ReasonFixesUnreadable = "reviewer fixes unreadable"
)

// nonReviewerRoles are the worker role names an agent step may name that
// are not a reviewer.
var nonReviewerRoles = map[string]bool{"coder": true, "designer": true, "finder": true, "swarm": true}

// Bounds.
const (
	DefaultStepTimeoutSeconds = 600
	MaxStepTimeoutSeconds     = 3600
	MaxRetries                = 3
	MaxSteps                  = 20
	MaxStepArgs               = 32
	maxNameRunes              = 200
	maxBriefRunes             = 20000
	maxArgBytes               = 4096
	maxCommandBytes           = 1024
	MaxStepConditions         = 8
	DefaultMaxRounds          = 2
	MaxRounds                 = 5
	maxReopenMessageRunes     = 4000
)

var (
	stepIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// stepOutputPlaceholder is the only substitution a reopen message
	// supports: {{steps.<id>.output}}, the recorded output line of an
	// earlier step.
	stepOutputPlaceholder = regexp.MustCompile(`\{\{steps\.([a-z0-9][a-z0-9_-]{0,63})\.output\}\}`)
	jobIDPattern          = regexp.MustCompile(`^(worker|media|design)/[A-Za-z0-9._:-]{1,200}/[A-Za-z0-9._:-]{1,200}$`)
	refIDPattern          = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)
)

// Scope is which job ends an automation applies to: every job in a folder,
// or only one worker's jobs there, or only one job, or only the jobs of one
// schedule.
type Scope struct {
	Folder string `json:"folder"`
	// Worker is a worker id: only that worker's jobs.
	Worker string `json:"worker,omitempty"`
	// Job is a job id (worker/<worker>/<run>, media/..., design/...).
	Job string `json:"job,omitempty"`
	// Schedule is a worker schedule id: only the jobs it started.
	Schedule string `json:"schedule,omitempty"`
}

// Condition is one test on an earlier step's outcome: its status
// (succeeded or failed) or, for an agent step, the verdict of the review the
// job it started recorded (ok or block). Exactly one of Status and Verdict.
type Condition struct {
	Step    string `json:"step"`
	Status  string `json:"status,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

// Step is one thing an automation does. A run step runs the owner's
// root-owned script on the host, as the daemon's user, in the folder; an
// agent step gives a worker a job; a
// reopen step sends its message and/or an earlier agent step's findings into
// the triggering job's own session as a follow-up.
type Step struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
	// If lists conditions that must all hold for the step to run; otherwise
	// it is skipped (not failed) and later steps still run.
	If []Condition `json:"if,omitempty"`
	// run
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Secrets was removed: a run step runs on the host and uses the
	// machine's own credentials, like the publish command. It is read only
	// so a step that still sends it is refused, never silently ignored.
	Secrets json.RawMessage `json:"secrets,omitempty"`
	// agent
	Worker string `json:"worker,omitempty"`
	Brief  string `json:"brief,omitempty"`
	// Fixes ("security", a reviewer worker only) lets the reviewer commit
	// small security fixes on top of the reviewed head. Never the default.
	Fixes string `json:"fixes,omitempty"`
	// reopen: From (optional when Message is set) names the earlier agent
	// step whose findings are sent; Message is placed before them, or sent
	// alone when there are none, and may hold {{steps.<id>.output}}.
	From      string `json:"from,omitempty"`
	Message   string `json:"message,omitempty"`
	MaxRounds int    `json:"max_rounds,omitempty"`
}

// Automation is a durable, account-owned definition. Revision changes on
// every update; a delivery keeps the revision it started with.
type Automation struct {
	ID        string `json:"id"`
	Account   string `json:"account"`
	Name      string `json:"name"`
	Scope     Scope  `json:"scope"`
	When      string `json:"when"`
	Enabled   bool   `json:"enabled"`
	Retries   int    `json:"retries"`
	Steps     []Step `json:"steps"`
	Revision  uint64 `json:"revision"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ValidationError is a definition Swarm refuses, with the reason.
type ValidationError struct{ msg string }

func (e ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return ValidationError{msg: fmt.Sprintf(format, args...)}
}

// IsValidationError reports whether err is a refused definition.
func IsValidationError(err error) bool {
	var v ValidationError
	return errors.As(err, &v)
}

// Normalize applies defaults and validates a definition. It never touches
// id, account, revision or times.
func (a *Automation) Normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || utf8.RuneCountInString(a.Name) > maxNameRunes || strings.ContainsAny(a.Name, "\r\n") {
		return invalid("name is required (one line, at most %d characters)", maxNameRunes)
	}
	if err := a.Scope.normalize(); err != nil {
		return err
	}
	a.When = strings.ToLower(strings.TrimSpace(a.When))
	if a.When == "" {
		a.When = WhenSucceeded
	}
	switch a.When {
	case WhenSucceeded, WhenFailed, WhenAlways:
	default:
		return invalid("when must be succeeded, failed or always")
	}
	if a.Retries < 0 || a.Retries > MaxRetries {
		return invalid("retries must be 0 to %d", MaxRetries)
	}
	return NormalizeSteps(a.Steps)
}

func (s *Scope) normalize() error {
	s.Folder = strings.TrimSpace(s.Folder)
	if s.Folder == "" || !filepath.IsAbs(s.Folder) {
		return invalid("scope.folder must be the absolute path of a project folder")
	}
	s.Folder = filepath.Clean(s.Folder)
	s.Worker, s.Job, s.Schedule = strings.TrimSpace(s.Worker), strings.TrimSpace(s.Job), strings.TrimSpace(s.Schedule)
	if s.Worker != "" && !refIDPattern.MatchString(s.Worker) {
		return invalid("scope.worker must be a worker id")
	}
	if s.Schedule != "" && !refIDPattern.MatchString(s.Schedule) {
		return invalid("scope.schedule must be a schedule id")
	}
	if s.Job != "" && !jobIDPattern.MatchString(s.Job) {
		return invalid("scope.job must be a job id (worker/<worker>/<run>, media/<project>/<task> or design/<project>/<task>)")
	}
	return nil
}

// HasRunStep reports whether steps contain a run step: a command the daemon
// runs on the host as its own user. The API lets only the machine's owner
// save one (see the api package's runStepOwnerError).
func HasRunStep(steps []Step) bool {
	for _, step := range steps {
		if step.Type == StepRun {
			return true
		}
	}
	return false
}

// NormalizeSteps applies step defaults (ids, timeouts) and validates them.
func NormalizeSteps(steps []Step) error {
	if len(steps) == 0 || len(steps) > MaxSteps {
		return invalid("an automation needs 1 to %d steps", MaxSteps)
	}
	seen := map[string]bool{}
	types := map[string]string{}
	for i := range steps {
		step := &steps[i]
		step.ID = strings.TrimSpace(step.ID)
		if step.ID == "" {
			step.ID = fmt.Sprintf("step-%d", i+1)
		}
		if !stepIDPattern.MatchString(step.ID) {
			return invalid("step id %q must be lowercase letters, digits, - or _ (at most 64)", step.ID)
		}
		if seen[step.ID] {
			return invalid("step id %q is used twice", step.ID)
		}
		if step.Secrets != nil {
			return invalid("step %s: %s", step.ID, secretsRemoved)
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = DefaultStepTimeoutSeconds
		}
		if step.TimeoutSeconds < 1 || step.TimeoutSeconds > MaxStepTimeoutSeconds {
			return invalid("step %s: timeout_seconds must be 1 to %d", step.ID, MaxStepTimeoutSeconds)
		}
		step.Type = strings.ToLower(strings.TrimSpace(step.Type))
		switch step.Type {
		case StepRun:
			if err := step.normalizeRun(); err != nil {
				return err
			}
		case StepAgent:
			if err := step.normalizeAgent(); err != nil {
				return err
			}
		case StepReopen:
			if err := step.normalizeReopen(types); err != nil {
				return err
			}
		default:
			return invalid("step %s: type must be run, agent or reopen", step.ID)
		}
		if err := step.normalizeConditions(types); err != nil {
			return err
		}
		seen[step.ID] = true
		types[step.ID] = step.Type
	}
	return nil
}

// normalizeConditions checks a step's if: each names an earlier step and
// tests exactly one of its status or (an agent step's) verdict.
func (s *Step) normalizeConditions(earlier map[string]string) error {
	if len(s.If) > MaxStepConditions {
		return invalid("step %s: at most %d conditions in if", s.ID, MaxStepConditions)
	}
	for i := range s.If {
		c := &s.If[i]
		c.Step = strings.TrimSpace(c.Step)
		c.Status = strings.ToLower(strings.TrimSpace(c.Status))
		c.Verdict = strings.ToLower(strings.TrimSpace(c.Verdict))
		typ, ok := earlier[c.Step]
		if !ok {
			return invalid("step %s: a condition names %q, which is not an earlier step", s.ID, c.Step)
		}
		if (c.Status == "") == (c.Verdict == "") {
			return invalid("step %s: a condition on %s has either status (succeeded|failed) or verdict (ok|block)", s.ID, c.Step)
		}
		if c.Status != "" && c.Status != StateSucceeded && c.Status != StateFailed {
			return invalid("step %s: a condition's status must be succeeded or failed", s.ID)
		}
		if c.Verdict != "" {
			if c.Verdict != VerdictOK && c.Verdict != VerdictBlock {
				return invalid("step %s: a condition's verdict must be ok or block", s.ID)
			}
			if typ != StepAgent {
				return invalid("step %s: a verdict condition needs an agent step; %s is a %s step", s.ID, c.Step, typ)
			}
		}
	}
	return nil
}

func (s *Step) normalizeReopen(earlier map[string]string) error {
	if s.Command != "" || len(s.Args) > 0 || len(s.Secrets) > 0 || s.Worker != "" || s.Brief != "" || s.Fixes != "" {
		return invalid("step %s: a reopen step has from, message and max_rounds, not command, args, secrets, worker, brief or fixes", s.ID)
	}
	s.From = strings.TrimSpace(s.From)
	s.Message = strings.TrimSpace(s.Message)
	if s.From == "" && s.Message == "" {
		return invalid("step %s: a reopen step needs from (an earlier agent step whose findings are sent), a message, or both", s.ID)
	}
	if s.From != "" {
		if typ, ok := earlier[s.From]; !ok || typ != StepAgent {
			return invalid("step %s: from must name an earlier agent step whose findings are sent", s.ID)
		}
	}
	if utf8.RuneCountInString(s.Message) > maxReopenMessageRunes {
		return invalid("step %s: message is at most %d characters", s.ID, maxReopenMessageRunes)
	}
	for _, m := range stepOutputPlaceholder.FindAllStringSubmatch(s.Message, -1) {
		if _, ok := earlier[m[1]]; !ok {
			return invalid("step %s: message placeholder %s names %q, which is not an earlier step", s.ID, m[0], m[1])
		}
	}
	if s.MaxRounds == 0 {
		s.MaxRounds = DefaultMaxRounds
	}
	if s.MaxRounds < 1 || s.MaxRounds > MaxRounds {
		return invalid("step %s: max_rounds must be 1 to %d", s.ID, MaxRounds)
	}
	return nil
}

func (s *Step) normalizeRun() error {
	if s.Worker != "" || s.Brief != "" || s.Fixes != "" || s.From != "" || s.Message != "" || s.MaxRounds != 0 {
		return invalid("step %s: a run step has command and args, not worker, brief, fixes, from, message or max_rounds", s.ID)
	}
	s.Command = strings.TrimSpace(s.Command)
	if err := ValidCommandPath(s.Command); err != nil {
		return invalid("step %s: %v", s.ID, err)
	}
	if len(s.Args) > MaxStepArgs {
		return invalid("step %s: at most %d args", s.ID, MaxStepArgs)
	}
	for _, arg := range s.Args {
		if len(arg) > maxArgBytes || strings.ContainsRune(arg, 0) {
			return invalid("step %s: an arg is longer than %d bytes or holds a NUL", s.ID, maxArgBytes)
		}
	}
	return nil
}

func (s *Step) normalizeAgent() error {
	if s.Command != "" || len(s.Args) > 0 || s.From != "" || s.Message != "" || s.MaxRounds != 0 {
		return invalid("step %s: an agent step has worker and brief, not command, args, from, message or max_rounds", s.ID)
	}
	s.Worker, s.Brief = strings.TrimSpace(s.Worker), strings.TrimSpace(s.Brief)
	if s.Worker == "" || len(s.Worker) > 200 {
		return invalid("step %s: worker is required (coder, designer, finder, reviewer or a worker id)", s.ID)
	}
	if s.Brief == "" || utf8.RuneCountInString(s.Brief) > maxBriefRunes {
		return invalid("step %s: brief is required (at most %d characters)", s.ID, maxBriefRunes)
	}
	s.Fixes = strings.ToLower(strings.TrimSpace(s.Fixes))
	if s.Fixes != "" {
		if s.Fixes != FixesSecurity {
			return invalid("step %s: fixes must be %q (the only value)", s.ID, FixesSecurity)
		}
		if nonReviewerRoles[strings.ToLower(s.Worker)] {
			return invalid("step %s: fixes is only for a reviewer worker; %s is not a reviewer", s.ID, s.Worker)
		}
	}
	return nil
}

// NonReviewerFixesError refuses fixes on an agent step whose worker
// resolved to a role other than reviewer (a worker id is resolved by the
// caller, which knows the account's workers).
func NonReviewerFixesError(stepID, worker, role string) error {
	return invalid("step %s: fixes is only for a reviewer worker; %s is a %s worker", stepID, worker, role)
}

// secretsRemoved is why a step that names secrets is refused.
const secretsRemoved = "secrets are no longer a step option: a run step runs on the host as the daemon's user and uses the machine's own credentials, like the publish command; remove secrets from the step"

// ValidCommandPath checks a run step's command: an absolute path, or a path
// relative to the folder that does not leave it, never under .git. Where
// the script lives is the owner's choice; before it runs, the resolved file
// must be owned by root and not writable by group or others (ResolveScript).
func ValidCommandPath(command string) error {
	if command == "" || len(command) > maxCommandBytes {
		return errors.New("command is required: the absolute path of a root-owned executable script, or its path relative to the folder, for example deliver/push.sh")
	}
	if strings.ContainsAny(command, "\\\x00") || path.Clean(command) != command || command == "." || command == "/" ||
		command == ".." || strings.HasPrefix(command, "../") {
		return fmt.Errorf("command %q must be a clean absolute path or a clean path inside the folder (no .., not under .git)", command)
	}
	for _, part := range strings.Split(command, "/") {
		if part == ".git" {
			return fmt.Errorf("command %q is under .git: a run step's script is never under .git", command)
		}
	}
	return nil
}

// Matches reports whether the automation applies to this job end.
func (a Automation) Matches(r *Result) bool {
	if !a.Enabled || r == nil {
		return false
	}
	if filepath.Clean(r.Folder) != a.Scope.Folder || r.Folder == "" {
		return false
	}
	if a.Scope.Job != "" && a.Scope.Job != r.JobID {
		return false
	}
	if a.Scope.Worker != "" && (r.Worker == nil || r.Worker.ID != a.Scope.Worker) {
		return false
	}
	if a.Scope.Schedule != "" && a.Scope.Schedule != r.ScheduleID {
		return false
	}
	switch a.When {
	case WhenSucceeded:
		return r.Status == StatusSucceeded
	case WhenFailed:
		return r.Status == StatusFailed || r.Status == StatusCancelled
	default:
		return true
	}
}
