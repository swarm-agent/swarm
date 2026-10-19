package jobautomation

import (
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
	StepRun   = "run"
	StepAgent = "agent"
)

// Bounds.
const (
	DefaultStepTimeoutSeconds = 600
	MaxStepTimeoutSeconds     = 3600
	MaxRetries                = 3
	MaxSteps                  = 20
	MaxStepArgs               = 32
	MaxStepSecrets            = 16
	maxNameRunes              = 200
	maxBriefRunes             = 20000
	maxArgBytes               = 4096
	maxCommandBytes           = 1024
)

var (
	stepIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	jobIDPattern      = regexp.MustCompile(`^(worker|media|design)/[A-Za-z0-9._:-]{1,200}/[A-Za-z0-9._:-]{1,200}$`)
	refIDPattern      = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)
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

// Step is one thing an automation does. A run step runs a script of the
// folder in the folder's sandbox; an agent step gives a worker a job.
type Step struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
	// run
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Secrets names the folder grants the script needs; the sandbox gets the
	// folder's grants as stand-ins either way. Naming them makes the step
	// refuse clearly when the secrets gateway is off or one is not granted.
	Secrets []string `json:"secrets,omitempty"`
	// agent
	Worker string `json:"worker,omitempty"`
	Brief  string `json:"brief,omitempty"`
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

// NormalizeSteps applies step defaults (ids, timeouts) and validates them.
func NormalizeSteps(steps []Step) error {
	if len(steps) == 0 || len(steps) > MaxSteps {
		return invalid("an automation needs 1 to %d steps", MaxSteps)
	}
	seen := map[string]bool{}
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
		seen[step.ID] = true
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
		default:
			return invalid("step %s: type must be run or agent", step.ID)
		}
	}
	return nil
}

func (s *Step) normalizeRun() error {
	if s.Worker != "" || s.Brief != "" {
		return invalid("step %s: a run step has command and args, not worker or brief", s.ID)
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
	if len(s.Secrets) > MaxStepSecrets {
		return invalid("step %s: at most %d secrets", s.ID, MaxStepSecrets)
	}
	for i, name := range s.Secrets {
		s.Secrets[i] = strings.TrimSpace(name)
		if !secretNamePattern.MatchString(s.Secrets[i]) {
			return invalid("step %s: secret %q is not a secret name", s.ID, name)
		}
	}
	return nil
}

func (s *Step) normalizeAgent() error {
	if s.Command != "" || len(s.Args) > 0 || len(s.Secrets) > 0 {
		return invalid("step %s: an agent step has worker and brief, not command, args or secrets", s.ID)
	}
	s.Worker, s.Brief = strings.TrimSpace(s.Worker), strings.TrimSpace(s.Brief)
	if s.Worker == "" || len(s.Worker) > 200 {
		return invalid("step %s: worker is required (coder, designer, finder, reviewer or a worker id)", s.ID)
	}
	if s.Brief == "" || utf8.RuneCountInString(s.Brief) > maxBriefRunes {
		return invalid("step %s: brief is required (at most %d characters)", s.ID, maxBriefRunes)
	}
	return nil
}

// ValidCommandPath checks a run step's command: a path to a script inside
// the folder, relative to it, with no way out of it.
func ValidCommandPath(command string) error {
	if command == "" || len(command) > maxCommandBytes {
		return errors.New("command is required: the path of an executable script inside the folder, for example deliver/push.sh")
	}
	if strings.HasPrefix(command, "/") || strings.ContainsAny(command, "\\\x00") || path.Clean(command) != command || command == "." ||
		command == ".." || strings.HasPrefix(command, "../") || strings.HasPrefix(command, ".git/") || command == ".git" {
		return fmt.Errorf("command %q must be a clean path inside the folder (no absolute path, no .., not under .git)", command)
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
