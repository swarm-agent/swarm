package design

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// DefaultConcurrency is how many designs run at once when the caller sets none.
const DefaultConcurrency = 4

// Pipeline runs design jobs. Model, Checker and Files are required.
type Pipeline struct {
	Model       Model
	Checker     Checker
	Files       Files
	Progress    Progress // optional
	Concurrency int
	Now         func() time.Time
}

// Validate checks a request's bounds before anything is spent.
func Validate(req Request) error {
	brief := strings.TrimSpace(req.Brief)
	switch {
	case brief == "":
		return errors.New("brief is required")
	case utf8.RuneCountInString(brief) > MaxBriefRunes:
		return fmt.Errorf("brief is longer than %d characters", MaxBriefRunes)
	case utf8.RuneCountInString(req.Style) > MaxStyleRunes:
		return fmt.Errorf("style is longer than %d characters", MaxStyleRunes)
	case len(req.Variations) > MaxVariations:
		return fmt.Errorf("at most %d variations", MaxVariations)
	}
	for _, v := range req.Variations {
		if utf8.RuneCountInString(v) > MaxVariationRunes {
			return fmt.Errorf("a variation is longer than %d characters", MaxVariationRunes)
		}
	}
	if req.Mode() == ModeEdit {
		if req.Count > 1 {
			return errors.New("an edit changes one file: count must be 1")
		}
		if !strings.EqualFold(path.Ext(req.EditPath), ".html") && !strings.EqualFold(path.Ext(req.EditPath), ".htm") {
			return errors.New("edit_path must be an .html file")
		}
		return nil
	}
	if req.Count < 1 || req.Count > MaxCount {
		return fmt.Errorf("count must be 1 to %d", MaxCount)
	}
	if strings.TrimSpace(req.Folder) == "" {
		return errors.New("folder is required")
	}
	return nil
}

// Run runs a create or edit job and returns its result. A request error is
// returned before any model call; per-design failures are in the result.
func (p *Pipeline) Run(ctx context.Context, req Request) (BatchResult, error) {
	if err := Validate(req); err != nil {
		return BatchResult{}, err
	}
	if p.Model == nil || p.Checker == nil || p.Files == nil {
		return BatchResult{}, errors.New("design pipeline is not configured")
	}
	if req.Mode() == ModeEdit {
		return p.runEdit(ctx, req)
	}
	return p.runCreate(ctx, req)
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pipeline) newBatch(req Request) BatchResult {
	return BatchResult{
		SchemaVersion: SchemaVersion,
		Mode:          req.Mode(),
		Brief:         strings.TrimSpace(req.Brief),
		Folder:        req.Folder,
		EditPath:      req.EditPath,
		Model:         p.Model.Info(),
		StartedAt:     p.now().UnixMilli(),
	}
}

// check runs the structural check and, when it passes, the browser check.
func (p *Pipeline) check(ctx context.Context, html string) *CheckReport {
	started := p.now()
	if diags := StructuralCheck(html); len(diags) > 0 {
		return &CheckReport{Status: CheckErrors, Diagnostics: boundDiagnostics(diags), DurationMS: p.now().Sub(started).Milliseconds()}
	}
	report := p.Checker.Check(ctx, []byte(html))
	report.Diagnostics = boundDiagnostics(report.Diagnostics)
	if report.Status == CheckErrors && len(report.Diagnostics) == 0 {
		report.Diagnostics = []Diagnostic{{Kind: "check_error", Message: "the browser check reported an error without details"}}
	}
	if report.Status != CheckOK && report.Status != CheckErrors {
		report.Status = CheckUnavailable
	}
	return &report
}

func (p *Pipeline) call(ctx context.Context, item *ItemResult, c ModelCall) (string, error) {
	reply, err := p.Model.Complete(ctx, c)
	reply.Usage.Phase = c.Phase
	if reply.Usage.Reported || err == nil {
		item.Calls = append(item.Calls, reply.Usage)
	}
	return reply.Text, err
}

func (p *Pipeline) runCreate(ctx context.Context, req Request) (BatchResult, error) {
	batch := p.newBatch(req)
	batch.Items = make([]ItemResult, req.Count)
	concurrency := p.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		stopped atomic.Bool
		sem     = make(chan struct{}, concurrency)
	)
	record := func(item ItemResult) {
		mu.Lock()
		batch.Items[item.Index-1] = item
		snapshot := p.finish(batch, false)
		// Written under the lock so an older snapshot never replaces a newer one.
		if err := p.writeResults(ctx, snapshot); err != nil {
			batch.ResultsError = err.Error()
		}
		mu.Unlock()
		if p.Progress != nil {
			p.Progress.Item(item)
			p.Progress.Batch(snapshot)
		}
	}
	for i := 0; i < req.Count; i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil || stopped.Load() {
			status := StatusCancelled
			if stopped.Load() {
				status = StatusSkippedBudget
			}
			for j := i; j < req.Count; j++ {
				record(ItemResult{Index: j + 1, Status: status})
			}
			break
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()
			item := p.createOne(ctx, req, index, &stopped)
			record(item)
		}(i)
	}
	wg.Wait()
	mu.Lock()
	final := p.finish(batch, true)
	if err := p.writeResults(ctx, final); err != nil {
		final.ResultsError = err.Error()
	}
	mu.Unlock()
	if p.Progress != nil {
		p.Progress.Batch(final)
	}
	return final, nil
}

func (p *Pipeline) createOne(ctx context.Context, req Request, index int, stopped *atomic.Bool) ItemResult {
	started := p.now()
	item := ItemResult{Index: index + 1, Variation: variationFor(req, index)}
	defer func() { item.DurationMS = p.now().Sub(started).Milliseconds() }()
	if stopped.Load() {
		item.Status = StatusSkippedBudget
		return item
	}
	text, err := p.call(ctx, &item, ModelCall{Phase: "generate", Item: index + 1, Instructions: generateInstructions, Input: generateInput(req, index, item.Variation)})
	if err != nil {
		return p.callFailed(ctx, item, err, stopped)
	}
	html, err := ExtractHTML(text)
	if err != nil {
		item.Status, item.Error = StatusGenerationFailed, err.Error()
		return item
	}
	item.CheckBefore = p.check(ctx, html)
	switch item.CheckBefore.Status {
	case CheckOK:
		item.Status = StatusPassed
	case CheckUnavailable:
		item.Status = StatusCheckUnavailable
	default:
		html = p.repair(ctx, &item, html, item.CheckBefore.Diagnostics, stopped)
	}
	rel := path.Join(req.Folder, fmt.Sprintf("%02d.html", index+1))
	if err := p.Files.CreateExclusive(ctx, rel, []byte(html)); err != nil {
		item.Status, item.Error = StatusFailed, "write: "+err.Error()
		return item
	}
	item.Path, item.SHA256 = rel, digest([]byte(html))
	return item
}

// repair makes the one repair call and checks once more. It sets the item's
// status and returns the document to keep.
func (p *Pipeline) repair(ctx context.Context, item *ItemResult, html string, diags []Diagnostic, stopped *atomic.Bool) string {
	item.Repair = &RepairOutcome{}
	text, err := p.call(ctx, item, ModelCall{Phase: "repair", Item: item.Index, Instructions: repairInstructions, Input: repairInput(html, diags)})
	if err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			stopped.Store(true)
		}
		item.Status, item.Repair.Refused = StatusFailed, "repair call failed: "+err.Error()
		return html
	}
	edits, err := ParseEdits(text)
	if err == nil {
		item.Repair.Edits = len(edits)
		var repaired string
		if repaired, err = ApplyEdits(html, edits); err == nil {
			html = repaired
		}
	}
	if err != nil {
		item.Status, item.Repair.Refused = StatusFailed, err.Error()
		return html
	}
	item.CheckAfter = p.check(ctx, html)
	switch item.CheckAfter.Status {
	case CheckOK:
		item.Status = StatusRepaired
	case CheckUnavailable:
		item.Status = StatusCheckUnavailable
	default:
		item.Status = StatusFailed
	}
	return html
}

func (p *Pipeline) callFailed(ctx context.Context, item ItemResult, err error, stopped *atomic.Bool) ItemResult {
	switch {
	case errors.Is(err, ErrBudgetExceeded):
		stopped.Store(true)
		item.Status = StatusSkippedBudget
	case ctx.Err() != nil:
		item.Status = StatusCancelled
	default:
		item.Status = StatusGenerationFailed
	}
	item.Error = err.Error()
	return item
}

func (p *Pipeline) runEdit(ctx context.Context, req Request) (BatchResult, error) {
	batch := p.newBatch(req)
	started := p.now()
	item := ItemResult{Index: 1, Path: req.EditPath}
	data, base, err := p.Files.Read(ctx, req.EditPath)
	if err != nil {
		return BatchResult{}, fmt.Errorf("read %s: %w", req.EditPath, err)
	}
	if !utf8.Valid(data) || len(data) > MaxHTMLBytes {
		return BatchResult{}, fmt.Errorf("%s is not UTF-8 HTML under 4 MiB", req.EditPath)
	}
	batch.EditBaseSHA256 = base
	var stopped atomic.Bool
	item = p.editOne(ctx, req, item, string(data), base, &stopped)
	item.DurationMS = p.now().Sub(started).Milliseconds()
	batch.Items = []ItemResult{item}
	if item.SHA256 != "" {
		batch.EditFinalSHA256 = item.SHA256
	}
	final := p.finish(batch, true)
	if p.Progress != nil {
		p.Progress.Item(item)
		p.Progress.Batch(final)
	}
	return final, nil
}

func (p *Pipeline) editOne(ctx context.Context, req Request, item ItemResult, original, base string, stopped *atomic.Bool) ItemResult {
	text, err := p.call(ctx, &item, ModelCall{Phase: "edit", Item: 1, Instructions: editInstructions, Input: editInput(req.Brief, original)})
	if err != nil {
		return p.callFailed(ctx, item, err, stopped)
	}
	edits, err := ParseEdits(text)
	var html string
	if err == nil {
		html, err = ApplyEdits(original, edits)
	}
	if err != nil {
		item.Status, item.Error = StatusEditRefused, err.Error()
		return item
	}
	item.CheckBefore = p.check(ctx, html)
	switch item.CheckBefore.Status {
	case CheckOK:
		item.Status = StatusPassed
	case CheckUnavailable:
		item.Status = StatusCheckUnavailable
	default:
		html = p.repair(ctx, &item, html, item.CheckBefore.Diagnostics, stopped)
	}
	if item.Status != StatusPassed && item.Status != StatusRepaired {
		// Nothing unverified or broken replaces the user's file.
		return item
	}
	if err := p.Files.ReplaceIfDigest(ctx, req.EditPath, base, []byte(html)); err != nil {
		if errors.Is(err, ErrStaleBase) {
			item.Status, item.Error = StatusStaleBase, err.Error()
		} else {
			item.Status, item.Error = StatusFailed, "write: "+err.Error()
		}
		return item
	}
	item.SHA256 = digest([]byte(html))
	return item
}

// finish fills counts and totals from the items.
func (p *Pipeline) finish(batch BatchResult, done bool) BatchResult {
	out := batch
	out.Items = append([]ItemResult(nil), batch.Items...)
	out.Counts = map[Status]int{}
	out.Totals = Totals{CostComplete: true}
	for _, item := range out.Items {
		if item.Status == "" {
			continue
		}
		out.Counts[item.Status]++
		for _, c := range item.Calls {
			out.Totals.Calls++
			out.Totals.Tokens += c.Total
			if c.CostUSD != nil {
				out.Totals.CostUSD += *c.CostUSD
			}
			if !c.Reported || c.CostUSD == nil {
				out.Totals.CostComplete = false
			}
		}
	}
	if done {
		out.FinishedAt = p.now().UnixMilli()
	}
	return out
}

func (p *Pipeline) writeResults(ctx context.Context, batch BatchResult) error {
	if batch.Mode != ModeCreate {
		return nil
	}
	data, err := json.MarshalIndent(batch, "", "  ")
	if err != nil {
		return err
	}
	// results.json is a copy for the user; the job record holds the same result.
	return p.Files.WriteResults(context.WithoutCancel(ctx), path.Join(batch.Folder, ResultsFile), append(data, '\n'))
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
