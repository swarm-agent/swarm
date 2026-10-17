// Package runner executes leased hub landings without starting a daemon.
package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Engine owns isolation and must release all per-request resources before returning.
type Engine interface {
	Health(context.Context, string) (docker, gvisor bool, err error)
	Execute(context.Context, store.RunnerLanding, string, func([]byte) error) ([]int, error)
}

type Runner struct {
	Hub, Credential, WorkDir, Name   string
	StateFile                        string
	Slots                            int
	Client                           *http.Client
	Engine                           Engine
	Logger                           *log.Logger
	heartbeat, statusInterval, retry time.Duration
}

// landingRequest extends the hub landing with its archive transport metadata.
// Keep this wire contract local while the hub persistence change lands separately.
type landingRequest struct {
	store.RunnerLanding
	PayloadFormat string `json:"payload_format"`
	ArchiveSHA256 string `json:"archive_sha256"`
	ArchiveBytes  int64  `json:"archive_bytes"`
}

type update struct {
	LeaseID    string `json:"lease_id"`
	Output     string `json:"output,omitempty"`
	ExitCodes  []int  `json:"exit_codes,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	State      string `json:"state,omitempty"`
}

func (r *Runner) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.Hub, "/")+path, &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Credential)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("hub %s: HTTP %d", path, resp.StatusCode)
	}
	return resp, nil
}
func (r *Runner) put(ctx context.Context, path string, body any) error {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := r.request(c, http.MethodPut, path, body)
	if err == nil {
		resp.Body.Close()
	}
	return err
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (r *Runner) log(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
}

func (r *Runner) Run(ctx context.Context) error {
	if r.Slots == 0 {
		r.Slots = 1
	}
	if r.Slots < 1 || r.Slots > 16 {
		return errors.New("slots must be between 1 and 16")
	}
	admission, stopAdmission := context.WithCancel(ctx)
	defer stopAdmission()
	u, err := url.Parse(r.Hub)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid hub URL")
	}
	if r.Engine == nil || r.Credential == "" || r.WorkDir == "" {
		return errors.New("engine, credential and work directory required")
	}
	if r.Client == nil {
		r.Client = &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("hub redirects refused") }}
	}
	if r.heartbeat == 0 {
		r.heartbeat = 20 * time.Second
	}
	if r.statusInterval == 0 {
		r.statusInterval = 60 * time.Second
	}
	if r.retry == 0 {
		r.retry = time.Second
	}
	r.WorkDir, err = filepath.Abs(r.WorkDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.WorkDir, 0700); err != nil {
		return err
	}
	if err := r.writeState("idle"); err != nil {
		return err
	}
	var mu sync.Mutex
	active := make(map[string]string)
	stateChanged := make(chan struct{}, 1)
	setState := func(id, timestamp string) error {
		mu.Lock()
		defer mu.Unlock()
		defer func() {
			select {
			case stateChanged <- struct{}{}:
			default:
			}
		}()
		if timestamp == "" {
			delete(active, id)
		} else {
			active[id] = timestamp
		}
		lines := make([]string, 0, len(active))
		for id, at := range active {
			lines = append(lines, "busy "+id+" "+at)
		}
		sort.Strings(lines)
		if len(lines) == 0 {
			return r.writeState("idle")
		}
		return r.writeState(strings.Join(lines, "\n"))
	}
	var reportMu sync.Mutex
	report := func() error {
		reportMu.Lock()
		defer reportMu.Unlock()
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		docker, gvisor, healthErr := r.Engine.Health(c, r.WorkDir)
		mu.Lock()
		state := "idle"
		if len(active) > 0 {
			state = "busy"
		}
		mu.Unlock()
		s := store.RunnerStatus{Name: r.Name, State: state, Docker: docker, GVisor: gvisor}
		if healthErr != nil {
			s.State = "unhealthy"
			s.Reason = healthErr.Error()
		}
		if err := r.put(c, "/v3/runners/status", s); err != nil {
			return err
		}
		return healthErr
	}
	// Status reporting continues during long polls and checks.
	done := make(chan struct{})
	reporterExited := make(chan struct{})
	defer func() { close(done); <-reporterExited }()
	go func() {
		defer close(reporterExited)
		t := time.NewTicker(r.statusInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				if err := report(); err != nil {
					r.log("runner final status: %v", err)
				}
				return
			case <-stateChanged:
				if err := report(); err != nil {
					r.log("runner status: %v", err)
				}
			case <-t.C:
				if err := report(); err != nil {
					r.log("runner status: %v", err)
				}
			}
		}
	}()
	worker := func() error {
		for admission.Err() == nil {
			if err := report(); err != nil {
				if !pause(admission, r.retry) {
					break
				}
				continue
			}
			if admission.Err() != nil {
				break
			}
			resp, err := r.request(admission, http.MethodPost, "/v3/runners/claim", map[string]int{"slots": r.Slots})
			if err != nil {
				if !pause(admission, r.retry) {
					break
				}
				continue
			}
			var claim struct {
				Landing *landingRequest `json:"landing"`
			}
			if resp.StatusCode != 204 {
				err = json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&claim)
			}
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("decode claim: %w", err)
			}
			if claim.Landing == nil {
				if !pause(admission, r.retry) {
					break
				}
				continue
			}
			r.log("runner claim %s", claim.Landing.ID)
			if err := setState(claim.Landing.ID, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			// Cancellation stops admission immediately, but only bounds held work once
			// draining begins. Reserve fifteen seconds of the grace for result delivery.
			work, stopWork := context.WithCancel(context.WithoutCancel(ctx))
			drained := make(chan struct{})
			go func() {
				select {
				case <-drained:
					return
				case <-admission.Done():
				}
				t := time.NewTimer(time.Duration(claim.Landing.CheckTimeout)*time.Second + 105*time.Second)
				defer t.Stop()
				select {
				case <-drained:
				case <-t.C:
					stopWork()
				}
			}()
			err = r.runLanding(work, *claim.Landing)
			close(drained)
			stopWork()
			if stateErr := setState(claim.Landing.ID, ""); stateErr != nil {
				err = errors.Join(err, stateErr)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	results := make(chan error, r.Slots)
	for i := 0; i < r.Slots; i++ {
		go func() {
			err := worker()
			if err != nil {
				stopAdmission()
			}
			results <- err
		}()
	}
	var runErr error
	for i := 0; i < r.Slots; i++ {
		runErr = errors.Join(runErr, <-results)
	}
	return runErr
}

func (r *Runner) writeState(state string) error {
	if r.StateFile == "" {
		return nil
	}
	// Atomic replacement prevents status readers observing a truncated busy line.
	if err := os.WriteFile(r.StateFile+".new", []byte(state+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(r.StateFile+".new", r.StateFile)
}

func (r *Runner) runLanding(ctx context.Context, l landingRequest) error {
	started := time.Now()
	path := "/v3/runners/landings/" + url.PathEscape(l.ID)
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	exited := make(chan struct{})
	var heartbeatErr error
	go func() {
		defer close(exited)
		t := time.NewTicker(r.heartbeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-work.Done():
				return
			case <-t.C:
				if err := r.put(work, path+"/heartbeat", update{LeaseID: l.LeaseID}); err != nil {
					heartbeatErr = err
					cancel()
					return
				}
			}
		}
	}()
	var outputMu sync.Mutex
	var output string
	emit := func(p []byte) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		for len(p) > 0 {
			n := min(len(p), 16<<10)
			chunk := string(p[:n])
			p = p[n:]
			output += chunk
			if len(output) > publish.CheckOutputLimit {
				output = output[len(output)-publish.CheckOutputLimit:]
			}
			if err := r.put(work, path+"/output", update{LeaseID: l.LeaseID, Output: chunk}); err != nil {
				return err
			}
		}
		return nil
	}
	dir, err := os.MkdirTemp(r.WorkDir, "landing-")
	var codes []int
	if err == nil {
		err = func() error {
			digest, e := hex.DecodeString(l.ArchiveSHA256)
			if l.PayloadFormat != "archive-v1" || e != nil || len(digest) != sha256.Size || l.ArchiveBytes <= 0 || l.ArchiveBytes > 512<<20 {
				return errors.New("invalid archive-v1 payload metadata")
			}
			resp, e := r.request(work, http.MethodGet, path+"/payload", nil)
			if e != nil {
				return fmt.Errorf("fetch payload: %w", e)
			}
			defer resp.Body.Close()
			f, e := os.OpenFile(dir+"/source.tar.gz", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			hash := sha256.New()
			n, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, l.ArchiveBytes+1))
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
			if n != l.ArchiveBytes {
				return errors.New("payload archive_bytes mismatch")
			}
			if !bytes.Equal(hash.Sum(nil), digest) {
				return errors.New("payload archive_sha256 mismatch")
			}
			codes, e = r.Engine.Execute(work, l.RunnerLanding, dir, emit)
			return e
		}()
		err = errors.Join(err, os.RemoveAll(dir))
	}
	close(stop)
	<-exited
	if heartbeatErr != nil && ctx.Err() == nil {
		return heartbeatErr
	}
	result := update{LeaseID: l.LeaseID, ExitCodes: codes, DurationMS: time.Since(started).Milliseconds(), Output: publish.CheckOutputExcerpt(output)}
	if err != nil {
		result.State = "environment_failed"
		result.ExitCodes = nil
		result.Output = publish.CheckOutputExcerpt(err.Error() + "\n" + output)
	}
	// The hub must support an explicit environment_failed result; never fabricate check failures.
	if e := r.put(context.WithoutCancel(ctx), path+"/result", result); e != nil {
		return e
	}
	r.log("runner result %s state=%s codes=%v", l.ID, result.State, codes)
	return nil
}
