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

// BuiltinAgents are the agents that come with Swarm. An agent step that names
// one runs it as a task (swarm_run_task): the coder in its own worktree of the
// folder started at the delivery head, the reviewer read-only at that head,
// the others in the folder itself; other names identify custom workers.
var BuiltinAgents = map[string]bool{"coder": true, "designer": true, "finder": true, "swarm": true, "reviewer": true}

// IsBuiltinAgent reports whether an agent step's worker names a built-in agent.
func IsBuiltinAgent(name string) bool { return BuiltinAgents[strings.ToLower(strings.TrimSpace(name))] }

// Where an agent step's agent works (Step.Checkout).
const (
	// CheckoutSame is a new session in the worktree of the job the step
	// follows, read-only: a reviewer reading the work where it was done.
	CheckoutSame = "same"
	// CheckoutNew is the agent's own new worktree, started at the job's head.
	CheckoutNew = "new"
	// CheckoutNone is no worktree: the agent works in the folder itself.
	CheckoutNone = "none"
)

// CheckoutFor is where an agent step's agent works: the step's checkout, or
// the agent's default. role is the step worker's role when known (a
// reviewer worker named by id), "" otherwise.
//
//	reviewer:                   same (also new)
//	reviewer with fixes:         new  (it commits)
//	swarm, finder, designer:     none (they work in the folder, as their
//	                             tasks always do; the job's commits are
//	                             the folder's branches)
//	coder, any other worker:     new
func CheckoutFor(step Step, role string) string {
	if step.Checkout != "" {
		return step.Checkout
	}
	worker := strings.ToLower(strings.TrimSpace(step.Worker))
	switch {
	case step.Fixes != "":
		return CheckoutNew
	case worker == "finder" || worker == "swarm" || worker == "designer":
		return CheckoutNone
	case worker == "reviewer" || role == "reviewer":
		return CheckoutSame
	}
	return CheckoutNew
}

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
	jobIDPattern          = regexp.MustCompile(`^(worker|media|design|task)/[A-Za-z0-9._:-]{1,200}/[A-Za-z0-9._:-]{1,200}$`)
	refIDPattern          = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)
)

// Scope is which job ends an automation applies to: every job and task in a
// folder, or only one agent's tasks there (role), or only one worker's jobs,
// or only one job, or only the jobs of one schedule.
type Scope struct {
	Folder string `json:"folder"`
	// Role is a built-in agent (swarm, coder, finder, designer): only the
	// tasks that agent finished in the folder.
	Role string `json:"role,omitempty"`
	// Worker is a worker id: only that worker's jobs.
	Worker string `json:"worker,omitempty"`
	// Job is a job id (task/<project>/<task>, worker/<worker>/<run>,
	// media/..., design/...).
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
	Worker  string   `json:"worker,omitempty"`
	Brief   string   `json:"brief,omitempty"`
	ForEach *ForEach `json:"for_each,omitempty"`
	// Fixes ("security", a reviewer worker only) lets the reviewer commit
	// small security fixes on top of the reviewed head. Never the default.
	Fixes string `json:"fixes,omitempty"`
	// Checkout is where the step's agent works (CheckoutSame, CheckoutNew or
	// CheckoutNone); "" is the agent's default (CheckoutFor).
	Checkout string `json:"checkout,omitempty"`
	// Produce (agent) makes the step's agent write the workflow's result
	// file (Automation.Result); its file is then a deliverable of the
	// delivery.
	Produce bool `json:"produce,omitempty"`
	// deliver: To is where (folder, merge, publish, outbox), Path the
	// directory in the folder for to: folder, What the deliverables by name
	// (default: the workflow's result, else what fits the target).
	To   string   `json:"to,omitempty"`
	Path string   `json:"path,omitempty"`
	What []string `json:"what,omitempty"`
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
	ItemSource string `json:"-"`
	// ScopeWorkerID is save-resolved metadata, not part of the portable definition.
	ScopeWorkerID  string `json:"scope_worker_id,omitempty"`
	inputsRendered bool
	Inputs         map[string]Input  `json:"inputs,omitempty"`
	With           map[string]string `json:"with,omitempty"`
	On             *Trigger          `json:"on,omitempty"`
	TriggerOwner   string            `json:"trigger_owner,omitempty"`
	Proposed       bool              `json:"proposed,omitempty"`
	ProposedBy     string            `json:"proposed_by,omitempty"`
	BaseID         string            `json:"base_id,omitempty"`
	BaseRevision   uint64            `json:"base_revision,omitempty"`
	ID             string            `json:"id"`
	Account        string            `json:"account"`
	Name           string            `json:"name"`
	Scope          Scope             `json:"scope"`
	When           string            `json:"when"`
	Enabled        bool              `json:"enabled"`
	Retries        int               `json:"retries"`
	Steps          []Step            `json:"steps"`
	// Result is what the work must hand over (optional): see ResultSpec.
	Result    *ResultSpec `json:"result,omitempty"`
	Revision  uint64      `json:"revision"`
	CreatedAt int64       `json:"created_at"`
	UpdatedAt int64       `json:"updated_at"`
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
func (a *Automation) Normalize() error { return a.normalizeInputs() }

func (a *Automation) normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || utf8.RuneCountInString(a.Name) > maxNameRunes || strings.ContainsAny(a.Name, "\r\n") {
		return invalid("name is required (one line, at most %d characters)", maxNameRunes)
	}
	if err := a.Scope.normalize(); err != nil {
		return err
	}
	if err := a.validateTrigger(); err != nil {
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
	if a.On == nil || len(a.Steps) != 0 {
		if err := NormalizeSteps(a.Steps); err != nil {
			return err
		}
	}
	return a.normalizeResult()
}

func (s *Scope) normalize() error {
	s.Folder = strings.TrimSpace(s.Folder)
	if s.Folder == "" || !filepath.IsAbs(s.Folder) {
		return invalid("scope.folder must be the absolute path of a project folder")
	}
	s.Folder = filepath.Clean(s.Folder)
	s.Worker, s.Job, s.Schedule = strings.TrimSpace(s.Worker), strings.TrimSpace(s.Job), strings.TrimSpace(s.Schedule)
	if len(s.Worker) > 200 {
		return invalid("scope.worker must be a worker id or name (at most 200 bytes)")
	}
	if s.Schedule != "" && !refIDPattern.MatchString(s.Schedule) {
		return invalid("scope.schedule must be a schedule id")
	}
	if s.Job != "" && !jobIDPattern.MatchString(s.Job) {
		return invalid("scope.job must be a job id (task/<project>/<task>, worker/<worker>/<run>, media/<project>/<task> or design/<project>/<task>)")
	}
	s.Role = strings.ToLower(strings.TrimSpace(s.Role))
	if s.Role != "" {
		if s.Role == "reviewer" || !BuiltinAgents[s.Role] {
			return invalid("scope.role must be swarm, coder, finder or designer")
		}
		if s.Worker != "" || s.Schedule != "" {
			return invalid("scope.role is for a built-in agent's tasks; it cannot be combined with scope.worker or scope.schedule")
		}
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
		if err := step.normalizeForEach(); err != nil {
			return err
		}
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
		case StepDeliver:
			if err := step.normalizeDeliver(); err != nil {
				return err
			}
		default:
			return invalid("step %s: type must be run, agent, reopen or deliver", step.ID)
		}
		if step.Type != StepDeliver {
			if err := step.noDeliverFields(); err != nil {
				return err
			}
		}
		if step.Produce && step.Type != StepAgent {
			return invalid("step %s: produce is for an agent step", step.ID)
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
	if s.Command != "" || len(s.Args) > 0 || len(s.Secrets) > 0 || s.Worker != "" || s.Brief != "" || s.Fixes != "" || s.Checkout != "" {
		return invalid("step %s: a reopen step has from, message and max_rounds, not command, args, secrets, worker, brief, fixes or checkout", s.ID)
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
	if s.Worker != "" || s.Brief != "" || s.Fixes != "" || s.Checkout != "" || s.From != "" || s.Message != "" || s.MaxRounds != 0 {
		return invalid("step %s: a run step has command and args, not worker, brief, fixes, checkout, from, message or max_rounds", s.ID)
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
		return invalid("step %s: worker is required (swarm, coder, finder or designer: the agents that come with Swarm; reviewer, or a worker id)", s.ID)
	}
	if s.Brief == "" || utf8.RuneCountInString(s.Brief) > maxBriefRunes {
		return invalid("step %s: brief is required (at most %d characters)", s.ID, maxBriefRunes)
	}
	s.Fixes = strings.ToLower(strings.TrimSpace(s.Fixes))
	if s.Fixes != "" {
		if s.Fixes != FixesSecurity {
			return invalid("step %s: fixes must be %q (the only value)", s.ID, FixesSecurity)
		}
		if IsBuiltinAgent(s.Worker) && !strings.EqualFold(s.Worker, "reviewer") {
			return invalid("step %s: fixes is only for a reviewer worker; %s is not a reviewer", s.ID, s.Worker)
		}
	}
	s.Checkout = strings.ToLower(strings.TrimSpace(s.Checkout))
	if s.Checkout == "" {
		return nil
	}
	if s.Checkout != CheckoutSame && s.Checkout != CheckoutNew && s.Checkout != CheckoutNone {
		return invalid("step %s: checkout must be same (a new session in the worktree of the job it follows, read-only), new (its own worktree at the job's head) or none (the folder itself)", s.ID)
	}
	worker := strings.ToLower(s.Worker)
	switch {
	case s.Fixes != "" && s.Checkout != CheckoutNew:
		return invalid("step %s: a reviewer with fixes commits, so it works in its own worktree: checkout must be new", s.ID)
	case worker == "coder" && s.Checkout != CheckoutNew:
		return invalid("step %s: coder changes files, so it works in its own worktree: checkout must be new (to have the job's own agent continue in its worktree, use a reopen step)", s.ID)
	case (worker == "finder" || worker == "swarm" || worker == "designer") && s.Checkout != CheckoutNone:
		return invalid("step %s: %s works in the folder (checkout none), as its tasks always do; the job's commits are the folder's branches, named in its brief", s.ID, worker)
	case (worker == "reviewer" || !IsBuiltinAgent(worker)) && s.Checkout == CheckoutNone:
		return invalid("step %s: a worker always works in a worktree: checkout must be same (a reviewer) or new", s.ID)
	}
	return nil
}

// NonReviewerCheckoutError refuses checkout same on an agent step whose
// worker resolved to a role other than reviewer: only a reviewer, which
// cannot change files, works in another job's worktree.
func NonReviewerCheckoutError(stepID, worker, role string) error {
	return invalid("step %s: checkout same is only for a reviewer (it reads the job's worktree and cannot change it); %s is a %s worker", stepID, worker, role)
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
	var err error
	a, err = a.RenderInputs()
	if err != nil {
		return false
	}
	if a.ScopeWorkerID != "" {
		a.Scope.Worker = a.ScopeWorkerID
	}
	if a.Proposed || !a.Enabled || r == nil || len(a.Steps) == 0 {
		return false
	}
	if a.On != nil && (a.ID == "" || r.TriggerID != a.ID) {
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
	if a.Scope.Role != "" && (r.Kind != KindTask || r.Role != a.Scope.Role) {
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
