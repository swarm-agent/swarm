package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
		apiErr := fmt.Errorf("Google API %s: HTTP %d: %s", method, res.StatusCode, strings.TrimSpace(string(data)))
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
	request, err := json.Marshal(struct {
		Definition Definition `json:"definition"`
		Checks     []string   `json:"checks"`
		Timeout    int        `json:"timeout"`
		Base       string     `json:"base"`
		Head       string     `json:"head"`
		SHA256     string     `json:"sha256"`
	}{spec.Definition, spec.Checks, int(spec.CheckTimeout / time.Second), spec.BaseCommit, spec.Commit, sha})
	if err != nil {
		return fail(err)
	}
	if err := p.Storage.Put(uploadCtx, p.Config.Bucket, e.prefix+"request.json", bytes.NewReader(request)); err != nil {
		return fail(err)
	}
	build, err := p.Builds.Create(ctx, Build{Timeout: fmt.Sprintf("%ds", int(timeout/time.Second)), ServiceAccount: p.Config.ServiceAccount, Options: BuildOptions{MachineType: p.Config.MachineType, Logging: "CLOUD_LOGGING_ONLY"}, Steps: []BuildStep{{Name: spec.Definition.BaseImage, Entrypoint: "bash", Args: []string{"-lc", cloudBuildScript(p.Config.Bucket, e.prefix, sha)}}}})
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
	provider   CloudBuild
	spec       Spec
	prefix, id string
	started    time.Time
	timeout    time.Duration
	index      int
	results    []cloudCheck
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
	for e.results == nil {
		b, err := e.provider.Builds.Get(ctx, e.id)
		failure := func(reason string) (*ExecResult, error) {
			return nil, &Failure{fmt.Sprintf("cloudbuild:%s status=%s: %s", e.id, b.Status, reason)}
		}
		if err != nil {
			return failure(err.Error())
		}
		switch b.Status {
		case "SUCCESS":
			data, err := e.provider.Storage.Get(ctx, e.provider.Config.Bucket, e.prefix+"result.json")
			if err != nil {
				return failure("result.json unavailable: " + err.Error())
			}
			var result struct {
				Checks []cloudCheck `json:"checks"`
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
			e.results = result.Checks
		case "QUEUED", "PENDING", "WORKING":
			if time.Since(e.started) > e.timeout {
				return failure("build wait exceeded configured timeout")
			}
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return failure(ctx.Err().Error())
			case <-timer.C:
			}
		default:
			return failure("build did not complete successfully")
		}
	}
	check := e.results[e.index]
	e.index++
	if req.OnOutput != nil {
		req.OnOutput([]byte(check.Output))
	}
	return &ExecResult{ExitCode: check.ExitCode, Stdout: check.Output, DurationMS: check.DurationMS}, nil
}
