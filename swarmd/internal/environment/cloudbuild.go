package environment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"sync"
	"time"
)

// CloudStorage and CloudBuilds are the two remote boundaries used by a landing.
// Implementations must return errors for unsuccessful remote operations.
type CloudStorage interface {
	Put(context.Context, string, string, io.Reader) error
	Get(context.Context, string, string) ([]byte, error)
	DeletePrefix(context.Context, string, string) error
}
type CloudBuilds interface {
	Create(context.Context, Build) (Build, error)
	Get(context.Context, string) (Build, error)
}
type Build struct {
	LogsBucket     string       `json:"logsBucket,omitempty"`
	StatusDetail   string       `json:"statusDetail,omitempty"`
	ID             string       `json:"id,omitempty"`
	Status         string       `json:"status,omitempty"`
	Steps          []BuildStep  `json:"steps,omitempty"`
	Timeout        string       `json:"timeout,omitempty"`
	ServiceAccount string       `json:"serviceAccount,omitempty"`
	Options        BuildOptions `json:"options,omitempty"`
}
type BuildOptions struct {
	MachineType string `json:"machineType,omitempty"`
	Logging     string `json:"logging,omitempty"`
}
type BuildStep struct {
	Name       string   `json:"name"`
	Entrypoint string   `json:"entrypoint"`
	Args       []string `json:"args"`
}

// GoogleREST uses an authenticated HTTP client supplied by startup. Credentials
// must be resolved by ADC with the cloud-platform scope, never stored in config.
type GoogleREST struct {
	Client          *http.Client
	Project, Region string
}

type googleAPIError struct {
	method string
	status int
	body   string
}

func (e *googleAPIError) Error() string {
	return fmt.Sprintf("Google API %s: HTTP %d: %s", e.method, e.status, e.body)
}

func (g GoogleREST) call(ctx context.Context, method, endpoint string, body io.Reader, limit int64) ([]byte, error) {
	if g.Client == nil {
		return nil, fmt.Errorf("Google ADC HTTP client unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var offset int64
	seeker, replayable := body.(io.Seeker)
	if replayable {
		var err error
		offset, err = seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		if attempt > 0 && body != nil {
			if !replayable {
				return nil, fmt.Errorf("Google API retry requires a seekable request body")
			}
			if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
				return nil, err
			}
		}
		var reader io.Reader
		if body != nil {
			reader = struct{ io.Reader }{body}
		} // transport must not close a replayable file
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := g.Client.Do(req)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, limit+1))
		res.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("Google API response exceeds limit")
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return data, nil
		}
		apiErr := &googleAPIError{method: method, status: res.StatusCode, body: strings.TrimSpace(string(data))}
		if attempt >= 4 || (res.StatusCode != 429 && res.StatusCode < 500) {
			return nil, apiErr
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (g GoogleREST) buildsURL() string {
	root := "https://cloudbuild.googleapis.com/v1/projects/" + url.PathEscape(g.Project)
	if g.Region != "" && g.Region != "global" {
		root += "/locations/" + url.PathEscape(g.Region)
	}
	return root + "/builds"
}
func (g GoogleREST) Create(ctx context.Context, build Build) (Build, error) {
	data, err := json.Marshal(build)
	if err != nil {
		return Build{}, err
	}
	data, err = g.call(ctx, "POST", g.buildsURL(), bytes.NewReader(data), 1<<20)
	if err != nil {
		return Build{}, err
	}
	// CreateBuild returns a long-running Operation, not a Build.
	var op struct {
		Metadata struct {
			Build Build `json:"build"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &op); err != nil {
		return Build{}, err
	}
	if op.Metadata.Build.ID == "" {
		return Build{}, fmt.Errorf("Cloud Build operation has no build ID")
	}
	return op.Metadata.Build, nil
}
func (g GoogleREST) Get(ctx context.Context, id string) (Build, error) {
	data, err := g.call(ctx, "GET", g.buildsURL()+"/"+url.PathEscape(id), nil, 1<<20)
	if err != nil {
		return Build{}, err
	}
	var b Build
	err = json.Unmarshal(data, &b)
	return b, err
}

// GoogleStorage shares the same ADC transport as the build client.
type GoogleStorage struct{ REST GoogleREST }

func (g GoogleStorage) Put(ctx context.Context, bucket, object string, body io.Reader) error {
	endpoint := "https://storage.googleapis.com/upload/storage/v1/b/" + url.PathEscape(bucket) + "/o?uploadType=media&name=" + url.QueryEscape(object)
	_, err := g.REST.call(ctx, "POST", endpoint, body, 1<<20)
	return err
}
func (g GoogleStorage) Get(ctx context.Context, bucket, object string) ([]byte, error) {
	return g.REST.call(ctx, "GET", "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket)+"/o/"+url.PathEscape(object)+"?alt=media", nil, 4<<20)
}
func (g GoogleStorage) DeletePrefix(ctx context.Context, bucket, prefix string) error {
	if !strings.HasPrefix(prefix, "landings/") || !strings.HasSuffix(prefix, "/") || prefix == "landings/" {
		return fmt.Errorf("invalid landing cleanup prefix")
	}
	root := "https://storage.googleapis.com/storage/v1/b/" + url.PathEscape(bucket) + "/o"
	token := ""
	for {
		data, err := g.REST.call(ctx, "GET", root+"?prefix="+url.QueryEscape(prefix)+"&pageToken="+url.QueryEscape(token), nil, 4<<20)
		if err != nil {
			return err
		}
		var page struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			Next string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, item := range page.Items {
			if !strings.HasPrefix(item.Name, prefix) {
				return fmt.Errorf("storage returned object outside landing prefix")
			}
			if _, err := g.REST.call(ctx, "DELETE", root+"/"+url.PathEscape(item.Name), nil, 1<<20); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		if page.Next == token {
			return fmt.Errorf("storage repeated page token")
		}
		token = page.Next
	}
}

type CloudBuildConfig struct {
	Project, Region, Bucket, MachineType, ServiceAccount string
	TimeoutMinutes                                       int
	// Cache is enabled when nil. A false value disables remote cache operations.
	Cache *bool
}
type CloudBuild struct {
	Store   *pebblestore.Store
	Config  CloudBuildConfig
	Storage CloudStorage
	Builds  CloudBuilds
}

// ResolveCloudBuildTimeout resolves zero from the folder's check timeout and
// caps the result before duration arithmetic to avoid overflow.
// A permission-only probe has no folder and supplies a zero check timeout.
func ResolveCloudBuildTimeout(minutes int, checkTimeout time.Duration) (time.Duration, error) {
	if minutes < 0 {
		return 0, fmt.Errorf("cloudbuild_timeout_minutes must be non-negative (0 uses the folder check timeout plus 5 minutes)")
	}
	const maximum = 24 * time.Hour
	if minutes > 0 {
		if minutes >= 1440 {
			return maximum, nil
		}
		return time.Duration(minutes) * time.Minute, nil
	}
	if checkTimeout >= maximum-5*time.Minute {
		return maximum, nil
	}
	return checkTimeout + 5*time.Minute, nil
}

func (p CloudBuild) Acquire(ctx context.Context, spec Spec) (Environment, error) {
	for _, field := range []struct {
		invalid bool
		name    string
	}{
		{p.Config.Project == "", "cloudbuild_project"},
		{p.Config.Bucket == "", "cloudbuild_bucket"},
		{p.Storage == nil, "storage client"},
		{p.Builds == nil, "builds client"},
		{p.Store == nil, "store"},
		{spec.CheckTimeout < time.Second, "CheckTimeout (must be at least one second)"},
		{spec.Definition.BaseImage == "", "BaseImage"},
	} {
		if field.invalid {
			return nil, &Failure{"Cloud Build configuration unavailable or invalid: " + field.name}
		}
	}
	timeout, err := ResolveCloudBuildTimeout(p.Config.TimeoutMinutes, spec.CheckTimeout)
	if err != nil {
		return nil, &Failure{err.Error()}
	}
	id, err := pebblestore.GenerateToken(16)
	if err != nil {
		return nil, err
	}
	e := &cloudEnvironment{provider: p, spec: spec, prefix: "landings/" + id + "/", started: time.Now(), timeout: timeout}
	uploadCtx, cancelUpload := context.WithCancel(ctx)
	defer cancelUpload()
	fail := func(err error) (Environment, error) {
		cancelUpload()
		first := e.Release()
		// Sweep once more after cancellation for an upload committed during cleanup.
		return nil, errors.Join(&Failure{err.Error()}, first, e.Release())
	}
	payload, sha, cleanup, err := p.Store.CloudBuildPayload(spec.Account, id, spec.Checkout, spec.Commit)
	if err != nil {
		return fail(err)
	}
	err = p.Storage.Put(uploadCtx, p.Config.Bucket, e.prefix+"payload.tar.gz", payload)
	err = errors.Join(err, payload.Close(), cleanup())
	if err != nil {
		return fail(err)
	}
	script := cloudBuildScriptWithCache(p.Config.Bucket, e.prefix, sha, p.cacheKey(spec))
	scriptSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
	if err := p.Storage.Put(uploadCtx, p.Config.Bucket, e.prefix+"script.sh", strings.NewReader(script)); err != nil {
		return fail(err)
	}
	checkScriptSHAs := make([]string, 0)
	if len(spec.Checks) > 1 {
		checkScriptSHAs = make([]string, len(spec.Checks))
		for i := range spec.Checks {
			prefix := fmt.Sprintf("%schecks/%d/", e.prefix, i)
			script := cloudBuildCheckScript(p.Config.Bucket, e.prefix, sha, p.cacheKey(spec), prefix, i, i == 0)
			checkScriptSHAs[i] = fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
			if err := p.Storage.Put(uploadCtx, p.Config.Bucket, prefix+"script.sh", strings.NewReader(script)); err != nil {
				return fail(err)
			}
		}
	}
	request, err := json.Marshal(struct {
		Definition        Definition `json:"definition"`
		Checks            []string   `json:"checks"`
		Timeout           int        `json:"timeout"`
		Base              string     `json:"base"`
		Head              string     `json:"head"`
		SHA256            string     `json:"sha256"`
		ScriptSHA256      string     `json:"script_sha256"`
		CheckScriptSHA256 []string   `json:"check_script_sha256,omitempty"`
	}{spec.Definition, spec.Checks, int(spec.CheckTimeout / time.Second), spec.BaseCommit, spec.Commit, sha, scriptSHA, checkScriptSHAs})
	if err != nil {
		return fail(err)
	}
	if err := p.Storage.Put(uploadCtx, p.Config.Bucket, e.prefix+"request.json", bytes.NewReader(request)); err != nil {
		return fail(err)
	}
	if len(spec.Checks) > 1 {
		e.children = make([]*cloudEnvironment, len(spec.Checks))
		var wg sync.WaitGroup
		for i, command := range spec.Checks {
			childSpec := spec
			childSpec.Checks = []string{command}
			child := &cloudEnvironment{provider: p, spec: childSpec, prefix: fmt.Sprintf("%schecks/%d/", e.prefix, i), started: e.started, timeout: timeout}
			e.children[i] = child
			wg.Add(1)
			go func(i int, child *cloudEnvironment) {
				defer wg.Done()
				build, err := p.Builds.Create(ctx, Build{Timeout: fmt.Sprintf("%ds", int(timeout/time.Second)), ServiceAccount: p.Config.ServiceAccount, LogsBucket: "gs://" + p.Config.Bucket, Options: BuildOptions{MachineType: p.Config.MachineType, Logging: "LEGACY"}, Steps: []BuildStep{{Name: spec.Definition.BaseImage, Entrypoint: "bash", Args: cloudBuildStepArgs(p.Config.Bucket, child.prefix, checkScriptSHAs[i])}}})
				if err == nil && build.ID == "" {
					err = fmt.Errorf("Cloud Build returned no build ID")
				}
				if err != nil {
					child.createErr = &Failure{fmt.Sprintf("Cloud Build check %d: %s", i+1, err)}
				}
				child.id = build.ID
			}(i, child)
		}
		wg.Wait()
		if spec.Selected != nil {
			ids := make([]string, len(e.children))
			for i, child := range e.children {
				ids[i] = child.id
			}
			spec.Selected("cloudbuild:" + strings.Join(ids, ","))
		}
		return e, nil
	}
	build, err := p.Builds.Create(ctx, Build{Timeout: fmt.Sprintf("%ds", int(timeout/time.Second)), ServiceAccount: p.Config.ServiceAccount, LogsBucket: "gs://" + p.Config.Bucket, Options: BuildOptions{MachineType: p.Config.MachineType, Logging: "LEGACY"}, Steps: []BuildStep{{Name: spec.Definition.BaseImage, Entrypoint: "bash", Args: cloudBuildStepArgs(p.Config.Bucket, e.prefix, scriptSHA)}}})
	if err != nil {
		return fail(err)
	}
	if build.ID == "" {
		return fail(fmt.Errorf("Cloud Build returned no build ID"))
	}
	e.id = build.ID
	if spec.Selected != nil {
		spec.Selected("cloudbuild:" + e.id)
	}
	return e, nil
}

type cloudCheck struct {
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
}
type cloudEnvironment struct {
	provider     CloudBuild
	spec         Spec
	prefix, id   string
	started      time.Time
	timeout      time.Duration
	index        int
	results      []cloudCheck
	children     []*cloudEnvironment
	phaseSummary string
	createErr    error
	outcomes     []*ExecResult
	errors       []error
}

func (e *cloudEnvironment) Sync(context.Context, string) error { return nil }
func (e *cloudEnvironment) Release() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.provider.Storage.DeletePrefix(ctx, e.provider.Config.Bucket, e.prefix)
}
func (e *cloudEnvironment) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	if e.index >= len(e.spec.Checks) || len(req.Command) != 3 || req.Command[0] != "bash" || req.Command[1] != "-lc" || req.Command[2] != e.spec.Checks[e.index] {
		return nil, &Failure{"Cloud Build check sequence mismatch"}
	}
	if e.children != nil {
		// Drain every check receipt before exposing a failure: the landing caller may
		// release the shared payload immediately after the first failed check.
		if e.outcomes == nil {
			e.outcomes = make([]*ExecResult, len(e.children))
			e.errors = make([]error, len(e.children))
			var wg sync.WaitGroup
			for i, child := range e.children {
				wg.Add(1)
				go func(i int, child *cloudEnvironment) {
					defer wg.Done()
					e.outcomes[i], e.errors[i] = child.Exec(ctx, ExecRequest{Command: []string{"bash", "-lc", child.spec.Checks[0]}})
				}(i, child)
			}
			wg.Wait()
			var summaries []string
			for i, child := range e.children {
				if child.phaseSummary != "" {
					summaries = append(summaries, fmt.Sprintf("check_%d{%s}", i, child.phaseSummary))
				}
			}
			if len(summaries) > 0 {
				log.Printf("Cloud Build landing phases: %s", strings.Join(summaries, " "))
			}
		}
		i := e.index
		e.index++
		if e.outcomes[i] != nil && req.OnOutput != nil {
			req.OnOutput([]byte(e.outcomes[i].Stdout))
		}
		return e.outcomes[i], e.errors[i]
	}
	if e.createErr != nil {
		return nil, e.createErr
	}
	for e.results == nil {
		// The receipt is authoritative even if the remaining cache work fails.
		data, resultErr := e.provider.Storage.Get(ctx, e.provider.Config.Bucket, e.prefix+"result.json")
		var b Build
		var err error
		if resultErr == nil {
			b.Status = "RESULT_READY"
		} else {
			b, err = e.provider.Builds.Get(ctx, e.id)
		}
		failure := func(reason string) (*ExecResult, error) {
			return nil, &Failure{fmt.Sprintf("cloudbuild:%s status=%s: %s", e.id, b.Status, reason)}
		}
		if err != nil {
			return failure(err.Error())
		}
		switch b.Status {
		case "SUCCESS", "RESULT_READY":
			if resultErr != nil {
				// Cover publication racing the terminal build status read.
				data, err = e.provider.Storage.Get(ctx, e.provider.Config.Bucket, e.prefix+"result.json")
			}
			if err != nil {
				return failure("result.json unavailable: " + err.Error())
			}
			var result struct {
				Checks []cloudCheck        `json:"checks"`
				Phases map[string]*float64 `json:"phases"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return failure("invalid result.json")
			}
			if len(result.Checks) != len(e.spec.Checks) {
				return failure("incomplete check results")
			}
			for _, c := range result.Checks {
				if c.DurationMS < 0 || len(c.Output) > 65536 {
					return failure("invalid check result")
				}
			}
			if len(result.Phases) > len(e.spec.Checks)+8 {
				return failure("too many phase timings")
			}
			var names []string
			for name, seconds := range result.Phases {
				if name == "" || len(name) > 64 || strings.IndexFunc(name, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') }) >= 0 || (seconds != nil && (*seconds < 0 || math.IsInf(*seconds, 0) || math.IsNaN(*seconds))) {
					return failure("invalid phase timing")
				}
				names = append(names, name)
			}
			sort.Strings(names)
			var summary []string
			for _, name := range names {
				value := "pending"
				if seconds := result.Phases[name]; seconds != nil {
					value = fmt.Sprintf("%.3fs", *seconds)
				}
				summary = append(summary, name+"="+value)
			}
			e.phaseSummary = strings.Join(summary, " ")
			if e.phaseSummary != "" {
				for i := range result.Checks {
					result.Checks[i].Output += "\nCloud Build phases: " + e.phaseSummary
				}
				if !strings.Contains(e.prefix, "/checks/") {
					log.Printf("Cloud Build landing %s phases: %s", e.id, e.phaseSummary)
				}
			}
			e.results = result.Checks
		case "QUEUED", "PENDING", "WORKING":
			if time.Since(e.started) > e.timeout {
				return failure("build wait exceeded configured timeout")
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return failure(ctx.Err().Error())
			case <-timer.C:
			}
		default:
			detail := b.StatusDetail
			if logs, ok := e.provider.Builds.(interface {
				LogTail(context.Context, Build) (string, error)
			}); ok {
				if tail, err := logs.LogTail(ctx, b); err == nil && strings.TrimSpace(tail) != "" {
					detail = tail
				}
			}
			return failure("build did not complete successfully\n" + boundedBuildTail(detail, 8192-len(e.id)-len(b.Status)-150))
		}
	}
	check := e.results[e.index]
	e.index++
	if req.OnOutput != nil {
		req.OnOutput([]byte(check.Output))
	}
	return &ExecResult{ExitCode: check.ExitCode, Stdout: check.Output, DurationMS: check.DurationMS}, nil
}

func (p CloudBuild) cacheKey(spec Spec) string {
	if spec.Folder == "" || (p.Config.Cache != nil && !*p.Config.Cache) {
		return ""
	}
	// Account and project namespace the folder without exposing identifiers.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(p.Config.Project+"\x00"+spec.Account+"\x00"+spec.Folder)))
}

// cloudBuildStepArgs escapes Cloud Build substitutions, not shell syntax.
// Cloud Build removes the extra dollars before bash receives the bootstrap.
func cloudBuildStepArgs(bucket, prefix, scriptSHA string) []string {
	return []string{"-c", strings.ReplaceAll(cloudBuildBootstrap(bucket, prefix, scriptSHA), "$", "$$")}
}
