package publish

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
)

// QueueStore must durably commit PutJSON before returning. The daemon owns one
// Queue per store; all enqueue and state transitions cross its mutex.
type QueueStore interface {
	GetJSON(string, any) (bool, error)
	PutJSON(string, any) error
}
type CheckResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
}
type ReviewEquivalence struct {
	ReviewedHead string `json:"reviewed_head"`
	RebasedHead  string `json:"rebased_head"`
	PatchID      string `json:"patch_id"`
	ReviewID     string `json:"review_id"`
}

type Entry struct {
	ValidateReview func(head, patch, base string) error `json:"-"`
	Checkpoint     func() error                         `json:"-"`
	Review         *ReviewEquivalence                   `json:"review_equivalence,omitempty"`
	Account        string                               `json:"account"`
	Request        Request                              `json:"request"`
	State          string                               `json:"delivery_state"`
	Commit         string                               `json:"commit,omitempty"`
	Reason         string                               `json:"reason,omitempty"`
	Checks         []CheckResult                        `json:"checks,omitempty"`
	SendBacks      int                                  `json:"send_backs"`
	Attempt        int                                  `json:"attempt,omitempty"`
}

// ErrNotSettled defers landing until the job and any follow-up have settled.
var ErrNotSettled = errors.New("job not settled")

// SendBackError denotes correctable source failures, not infrastructure errors.
type SendBackError struct{ Reason string }

func (e *SendBackError) Error() string { return e.Reason }

type QueueHooks struct {
	Finished func(context.Context, Entry) error
	Prepare  func(context.Context, *Entry) error // update, sandbox rebase and checks
	Land     func(context.Context, Entry) (LandResult, error)
	SendBack func(context.Context, Entry) error // canonical worker follow-up
}
type Queue struct {
	store   QueueStore
	mu      sync.Mutex
	folders map[string]*sync.Mutex
}

func NewQueue(s QueueStore) *Queue { return &Queue{store: s, folders: map[string]*sync.Mutex{}} }
func queueKey(account string) string {
	return fmt.Sprintf("publish_queue/%x", sha256.Sum256([]byte(account)))
}
func (q *Queue) load(account string) ([]Entry, error) {
	var entries []Entry
	_, e := q.store.GetJSON(queueKey(account), &entries)
	return entries, e
}
func active(e Entry) bool { return e.State == "queued" || e.State == "landing" }
func position(entries []Entry, id string) int {
	n := 0
	folder := ""
	for _, e := range entries {
		if e.Request.JobID == id {
			folder = e.Request.Folder
			break
		}
	}
	for _, e := range entries {
		if e.Request.Folder == folder && active(e) {
			n++
			if e.Request.JobID == id {
				return n
			}
		}
	}
	return 0
}
func (q *Queue) Enqueue(e Entry) (int, error) {
	_, pos, err := q.EnqueueDelivery(e, true)
	return pos, err
}

// EnqueueDelivery returns the actual durable receipt under the queue mutex.
// Only explicit delivery may restart a stopped attempt; automatic replay may not.
func (q *Queue) EnqueueDelivery(e Entry, retryStopped bool) (Entry, int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e.Account == "" || e.Request.JobID == "" || e.Request.Folder == "" {
		return Entry{}, 0, errors.New("queue requires account, job and folder")
	}
	entries, err := q.load(e.Account)
	if err != nil {
		return Entry{}, 0, err
	}
	for i, old := range entries {
		if old.Request.JobID != e.Request.JobID {
			continue
		}
		if active(old) || old.State == "landed" || (old.State == "landing_stopped" && !retryStopped) {
			return old, position(entries, e.Request.JobID), nil
		}
		// Only retain queue-owned evidence for the same source and destination.
		// Preparation revalidates both patches against the fresh immutable base.
		if old.Request.Folder == e.Request.Folder && old.Request.Branch == e.Request.Branch && old.Request.BaseBranch == e.Request.BaseBranch && old.Review != nil && old.Review.RebasedHead == e.Request.Head {
			e.Review = old.Review
		}
		e.SendBacks = old.SendBacks
		e.Attempt = old.Attempt
		if old.State == "landing_stopped" {
			e.SendBacks = 0
			e.Attempt++
		}
		entries = append(entries[:i], entries[i+1:]...)
		break
	}
	e.State = "queued"
	e.Reason, e.Commit, e.Checks = "", "", nil
	entries = append(entries, e)
	if err = q.store.PutJSON(queueKey(e.Account), entries); err != nil {
		return Entry{}, 0, err
	}
	return e, position(entries, e.Request.JobID), nil
}
func (q *Queue) Get(account, id string) (Entry, int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := q.load(account)
	if err != nil {
		return Entry{}, 0, err
	}
	for _, e := range entries {
		if e.Request.JobID == id {
			return e, position(entries, id), nil
		}
	}
	return Entry{}, 0, nil
}
func (q *Queue) save(e Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := q.load(e.Account)
	if err != nil {
		return err
	}
	for i := range entries {
		if entries[i].Request.JobID == e.Request.JobID {
			entries[i] = e
			return q.store.PutJSON(queueKey(e.Account), entries)
		}
	}
	return errors.New("queue entry disappeared")
}

// Process serializes a folder, but never holds the store mutex during external
// work. A persisted landing always restarts at Prepare, including after MOVED.
func (q *Queue) Process(ctx context.Context, account, folder string, h QueueHooks) error {
	key := account + "\x00" + folder
	q.mu.Lock()
	lock := q.folders[key]
	if lock == nil {
		lock = &sync.Mutex{}
		q.folders[key] = lock
	}
	q.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		entries, err := q.load(account)
		q.mu.Unlock()
		if err != nil {
			return err
		}
		var e Entry
		for _, candidate := range entries {
			if candidate.Request.Folder == folder && active(candidate) {
				e = candidate
				break
			}
		}
		if e.Request.JobID == "" {
			return nil
		}
		e.State = "landing"
		if err = q.save(e); err != nil {
			return err
		}
		e.Checkpoint = func() error { return q.save(e) }
		for attempt := 0; attempt < 3; attempt++ {
			err = h.Prepare(ctx, &e)
			if errors.Is(err, ErrNotSettled) {
				e.State = "queued"
				// Keep FIFO order and retry on the next scheduler round, without
				// checks, landing, send-backs or terminal notifications.
				return q.save(e)
			}
			if err == nil {
				if err = q.save(e); err != nil {
					return err
				}
				var receipt LandResult
				receipt, err = h.Land(ctx, e)
				if err == nil && !receipt.Moved {
					e.State = "landed"
					e.Commit = receipt.Commit
					break
				}
				if err == nil {
					if attempt == 2 {
						err = errors.New("base kept moving")
					} else {
						continue
					}
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			var back *SendBackError
			if errors.As(err, &back) {
				e.SendBacks++
				e.Reason = tail(back.Reason, 4096)
				e.State = "sent_back"
				if e.SendBacks >= 2 {
					e.State = "landing_stopped"
				}
				if h.SendBack == nil {
					return errors.New("worker follow-up unavailable")
				}
				if err = h.SendBack(ctx, e); err != nil {
					return err
				}
			} else {
				e.State = "land_failed"
				e.Reason = oneLine(err.Error(), 200)
			}
			break
		}
		if err = q.save(e); err != nil {
			return err
		}
		if h.Finished != nil {
			if err = h.Finished(ctx, e); err != nil {
				return err
			}
		}
	}
}

// Pending returns one active entry per folder for bounded daemon scheduling.
func (q *Queue) Pending(account string) ([]Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := q.load(account)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Entry
	for _, e := range entries {
		if active(e) && !seen[e.Request.Folder] {
			seen[e.Request.Folder] = true
			out = append(out, e)
		}
	}
	return out, nil
}
