package v3chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
)

// Worker reviews are independent of session-plan approval and never seize the
// composer. The permission is a discovery resource, not a permission to run a tool.
var workerReviewDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type workerReviewIntent struct {
	Review      client.WorkerReview
	WorkspaceID string
	Title       string
	Goal        string
	Schedule    string
	Expiration  string
	Consequence string
	Checkpoints []string
}

func isWorkerReviewPermission(record client.PermissionRecord) bool {
	return strings.EqualFold(record.Requirement, "automation_v2_acceptance") && normalizePermissionToolName(record.ToolName) == "manage_workers"
}

func parseWorkerReview(record client.PermissionRecord) (workerReviewIntent, bool) {
	if !isWorkerReviewPermission(record) || !permissionPending(record) {
		return workerReviewIntent{}, false
	}
	var payload struct {
		ReviewKind       string              `json:"review_kind"`
		WorkerReview     client.WorkerReview `json:"worker_review"`
		AutomationReview client.WorkerReview `json:"automation_review"`
		Scope            struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"scope"`
		Document struct {
			Title string `json:"title"`
			Info  struct {
				Goal string `json:"goal"`
			} `json:"info"`
			WorkerV2     json.RawMessage `json:"worker_v2"`
			AutomationV2 json.RawMessage `json:"automation_v2"`
			Checkpoints  []struct {
				Title              string   `json:"title"`
				Objective          string   `json:"objective"`
				Tasks              []string `json:"tasks"`
				AcceptanceCriteria []string `json:"acceptance_criteria"`
			} `json:"checkpoints"`
		} `json:"document"`
		AcceptanceConsequence string `json:"acceptance_consequence"`
	}
	if json.Unmarshal([]byte(record.ToolArguments), &payload) != nil {
		return workerReviewIntent{}, false
	}
	review := payload.WorkerReview
	if review.ProposalID == "" {
		review = payload.AutomationReview
	}
	if payload.ReviewKind != "worker_v2" && payload.ReviewKind != "automation_v2" {
		return workerReviewIntent{}, false
	}
	if review.ProposalID == "" || review.Revision == 0 || !workerReviewDigest.MatchString(review.Digest) || strings.TrimSpace(payload.Scope.WorkspaceID) == "" || strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(payload.Document.Title) == "" {
		return workerReviewIntent{}, false
	}
	settings := payload.Document.WorkerV2
	if len(settings) == 0 {
		settings = payload.Document.AutomationV2
	}
	var policy struct {
		Schedule struct {
			Kind            string `json:"kind"`
			IntervalSeconds int    `json:"interval_seconds"`
			Cron            string `json:"cron"`
			Timezone        string `json:"timezone"`
		} `json:"schedule"`
		Expiration struct {
			Kind      string `json:"kind"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"expiration"`
	}
	if json.Unmarshal(settings, &policy) != nil || policy.Schedule.Kind == "" {
		return workerReviewIntent{}, false
	}
	schedule := policy.Schedule.Kind
	switch schedule {
	case "interval":
		schedule = fmt.Sprintf("Every %d seconds", policy.Schedule.IntervalSeconds)
	case "cron":
		schedule = policy.Schedule.Cron + " (" + policy.Schedule.Timezone + ")"
	case "trigger":
		schedule = "On demand"
	}
	expiration := "Until stopped"
	if policy.Expiration.Kind == "at" {
		expiration = time.UnixMilli(policy.Expiration.ExpiresAt).Format(time.RFC822)
	}
	intent := workerReviewIntent{Review: review, WorkspaceID: payload.Scope.WorkspaceID, Title: payload.Document.Title, Goal: payload.Document.Info.Goal, Schedule: schedule, Expiration: expiration, Consequence: payload.AcceptanceConsequence}
	for _, checkpoint := range payload.Document.Checkpoints {
		intent.Checkpoints = append(intent.Checkpoints, "Checkpoint: "+checkpoint.Title)
		if checkpoint.Objective != "" {
			intent.Checkpoints = append(intent.Checkpoints, checkpoint.Objective)
		}
		for _, task := range checkpoint.Tasks {
			intent.Checkpoints = append(intent.Checkpoints, "Task: "+task)
		}
		for _, criterion := range checkpoint.AcceptanceCriteria {
			intent.Checkpoints = append(intent.Checkpoints, "Acceptance: "+criterion)
		}
	}
	if len(intent.Checkpoints) == 0 {
		return workerReviewIntent{}, false
	}
	return intent, true
}

func SelectPendingWorkerReviews(state State) []client.PermissionRecord {
	var reviews []client.PermissionRecord
	for _, item := range SelectPermissions(state) {
		if _, ok := parseWorkerReview(item.Record); ok {
			reviews = append(reviews, item.Record)
		}
	}
	return reviews
}

func (p *Page) workerReviewVisibleLocked() bool {
	if p.runtime == nil || p.runtime.Store() == nil || !p.workerReviewOpen {
		return false
	}
	if len(SelectPendingWorkerReviews(p.runtime.Store().Snapshot())) == 0 {
		p.workerReviewOpen = false
		return false
	}
	return true
}

func (p *Page) handleWorkerReviewKeyLocked(ev *tcell.EventKey) PageAction {
	reviews := SelectPendingWorkerReviews(p.runtime.Store().Snapshot())
	if len(reviews) == 0 {
		p.workerReviewOpen = false
		return PageActionNone
	}
	p.workerReviewIndex = maxInt(0, minInt(p.workerReviewIndex, len(reviews)-1))
	record := reviews[p.workerReviewIndex]
	if p.workerReviewBusy && ev.Key() != tcell.KeyEscape {
		return PageActionNone
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.workerReviewOpen = false
	case tcell.KeyUp:
		if p.workerReviewIndex > 0 {
			p.workerReviewIndex--
		}
	case tcell.KeyDown:
		if p.workerReviewIndex+1 < len(reviews) {
			p.workerReviewIndex++
		}
	case tcell.KeyRune:
		if ev.Rune() == 'a' || ev.Rune() == 'A' {
			p.decideWorkerReviewLocked(record, true)
		}
		if ev.Rune() == 'd' || ev.Rune() == 'D' {
			p.decideWorkerReviewLocked(record, false)
		}
	}
	return PageActionNone
}

func (p *Page) decideWorkerReviewLocked(record client.PermissionRecord, accept bool) {
	if p.workerReviewBusy {
		return
	}
	intent, ok := parseWorkerReview(record)
	if !ok {
		p.workerReviewError = "Worker review changed; reopen the latest review"
		return
	}
	transport, ok := p.runtime.transport.(workerReviewTransport)
	if !ok {
		p.workerReviewError = "Worker review transport is unavailable"
		return
	}
	p.workerReviewBusy, p.workerReviewError = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		latest, err := transport.GetWorkerReview(ctx, intent.WorkspaceID, record.SessionID)
		if err == nil && (latest.WorkerReview != intent.Review || latest.WorkspaceID != intent.WorkspaceID || latest.SessionID != record.SessionID) {
			err = errors.New("Worker review changed; inspect the latest revision")
		}
		if err == nil {
			state := p.runtime.Store().Snapshot()
			current := false
			if state.Session.ID == record.SessionID {
				for _, pending := range SelectPendingWorkerReviews(state) {
					if pending.ID == record.ID && pending.ToolArguments == record.ToolArguments {
						current = true
						break
					}
				}
			}
			if !current {
				err = errors.New("Worker review changed; inspect the latest revision")
			}
		}
		if err == nil {
			err = transport.DecideWorkerReview(ctx, intent.WorkspaceID, record.SessionID, intent.Review, accept)
		}
		// Reconcile only the canonical permission snapshot. Never reset the
		// chat transcript, composer, or run state while reviewing a worker.
		if err == nil {
			state := p.runtime.Store().Snapshot()
			if state.Session.ID != record.SessionID {
				err = errors.New("worker review session changed during refresh")
			} else {
				var hydrated client.SessionV3Hydrated
				hydrated, err = p.runtime.transport.GetSessionV3TUI(ctx, record.SessionID, state.Session.WorkspacePath, "")
				if err == nil {
					if p.runtime.Store().Snapshot().Session.ID != record.SessionID || hydrated.Session.ID != record.SessionID {
						err = errors.New("worker review session changed during refresh")
					} else {
						p.runtime.Store().Dispatch(PermissionsAction{Records: hydrated.PendingPermissions})
					}
				}
				// A failed targeted hydration is reported instead of claiming
				// that the review disappeared.
			}
		}
		p.mu.Lock()
		p.workerReviewBusy = false
		if err != nil {
			p.workerReviewError = err.Error()
		} else {
			p.workerReviewOpen = false
			p.workerReviewError = ""
		}
		p.mu.Unlock()
		p.runtime.signalWake()
	}()
}

type workerReviewTransport interface {
	GetWorkerReview(context.Context, string, string) (client.WorkerProposal, error)
	DecideWorkerReview(context.Context, string, string, client.WorkerReview, bool) error
}

func (p *Page) workerReviewRows(state State, width int, styles PageStyles) []renderRow {
	reviews := SelectPendingWorkerReviews(state)
	if len(reviews) == 0 {
		return nil
	}
	rows := []renderRow{{text: fmt.Sprintf("Workers · %d pending review · F6 to inspect (chat remains available)", len(reviews)), style: styles.Warning}}
	p.mu.Lock()
	open, index, busy, errText := p.workerReviewOpen, p.workerReviewIndex, p.workerReviewBusy, p.workerReviewError
	p.mu.Unlock()
	if !open {
		return rows
	}
	index = maxInt(0, minInt(index, len(reviews)-1))
	intent, ok := parseWorkerReview(reviews[index])
	if !ok {
		return rows
	}
	model := permissionCardModel{Title: "Worker review", Badge: "NOT PLAN APPROVAL", Meta: fmt.Sprintf("%d/%d · revision %d", index+1, len(reviews), intent.Review.Revision)}
	for _, line := range []string{intent.Title, "Goal: " + intent.Goal, "Schedule: " + intent.Schedule, "Expiration: " + intent.Expiration, intent.Consequence, "Review all instructions below before accepting.", "↑/↓ Choose worker · Esc Return to chat"} {
		for _, text := range wrapText(line, maxInt(1, width-4)) {
			model.Content = append(model.Content, permissionCardLine{Text: text, Style: styles.Text})
		}
	}
	for _, line := range intent.Checkpoints {
		for _, text := range wrapText(line, maxInt(1, width-4)) {
			model.Content = append(model.Content, permissionCardLine{Text: text, Style: styles.Text})
		}
	}
	if errText != "" {
		model.Content = append(model.Content, permissionCardLine{Text: "Error: " + errText, Style: styles.Error})
	}
	if busy {
		model.Content = append(model.Content, permissionCardLine{Text: "Resolving worker review…", Style: styles.Muted})
	}
	model.Content = append(model.Content, permissionCardLine{Text: "A Accept worker · D Decline worker · Esc Return to chat", Style: styles.Accent})
	return append(rows, permissionCardRows(permissionCardView{Model: model, Selected: true, Pending: false}, width, styles)...)
}
