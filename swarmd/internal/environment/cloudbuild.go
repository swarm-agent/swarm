package environment

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 pebblestore "swarm/packages/swarmd/internal/store/pebble"
 "fmt"
 "io"
 "net/http"
 "net/url"
 "strings"
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
 ID string `json:"id,omitempty"`
 Status string `json:"status,omitempty"`
 Steps []BuildStep `json:"steps,omitempty"`
 Timeout string `json:"timeout,omitempty"`
 ServiceAccount string `json:"serviceAccount,omitempty"`
 Options BuildOptions `json:"options,omitempty"`
}
type BuildOptions struct {
 MachineType string `json:"machineType,omitempty"`
 Logging string `json:"logging,omitempty"`
}
type BuildStep struct {
 Name string `json:"name"`
 Entrypoint string `json:"entrypoint"`
 Args []string `json:"args"`
}

// GoogleREST uses an authenticated HTTP client supplied by startup. Credentials
// must be resolved by ADC with the cloud-platform scope, never stored in config.
type GoogleREST struct {
 Client *http.Client
 Project, Region string
}
func (g GoogleREST) call(ctx context.Context, method, endpoint string, body io.Reader, limit int64) ([]byte, error) {
 if g.Client == nil { return nil, fmt.Errorf("Google ADC HTTP client unavailable") }
 req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
 if err != nil { return nil, err }
 if body != nil { req.Header.Set("Content-Type", "application/json") }
 res, err := g.Client.Do(req)
 if err != nil { return nil, err }
 defer res.Body.Close()
 if res.StatusCode < 200 || res.StatusCode >= 300 { return nil, fmt.Errorf("Google API %s: HTTP %d", method, res.StatusCode) }
 data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
 if int64(len(data)) > limit { return nil, fmt.Errorf("Google API response exceeds limit") }
 return data, err
}
func (g GoogleREST) buildsURL() string {
 root := "https://cloudbuild.googleapis.com/v1/projects/"+url.PathEscape(g.Project)
 if g.Region != "" && g.Region != "global" { root += "/locations/"+url.PathEscape(g.Region) }
 return root+"/builds"
}
func (g GoogleREST) Create(ctx context.Context, build Build) (Build, error) {
 data, err := json.Marshal(build); if err != nil { return Build{}, err }
 data, err = g.call(ctx, "POST", g.buildsURL(), bytes.NewReader(data), 1<<20)
 if err != nil { return Build{}, err }
 // CreateBuild returns a long-running Operation, not a Build.
 var op struct { Metadata struct { Build Build `json:"build"` } `json:"metadata"` }
 if err := json.Unmarshal(data, &op); err != nil { return Build{}, err }
 if op.Metadata.Build.ID == "" { return Build{}, fmt.Errorf("Cloud Build operation has no build ID") }
 return op.Metadata.Build, nil
}
func (g GoogleREST) Get(ctx context.Context, id string) (Build, error) {
 data, err := g.call(ctx, "GET", g.buildsURL()+"/"+url.PathEscape(id), nil, 1<<20)
 if err != nil { return Build{}, err }
 var b Build; err = json.Unmarshal(data, &b); return b, err
}
// GoogleStorage shares the same ADC transport as the build client.
type GoogleStorage struct { REST GoogleREST }
func (g GoogleStorage) Put(ctx context.Context, bucket, object string, body io.Reader) error {
 endpoint := "https://storage.googleapis.com/upload/storage/v1/b/"+url.PathEscape(bucket)+"/o?uploadType=media&name="+url.QueryEscape(object)
 _, err := g.REST.call(ctx, "POST", endpoint, body, 1<<20); return err
}
func (g GoogleStorage) Get(ctx context.Context, bucket, object string) ([]byte, error) {
 return g.REST.call(ctx, "GET", "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket)+"/o/"+url.PathEscape(object)+"?alt=media", nil, 4<<20)
}
func (g GoogleStorage) DeletePrefix(ctx context.Context, bucket, prefix string) error {
 if !strings.HasPrefix(prefix, "landings/") || !strings.HasSuffix(prefix, "/") || prefix == "landings/" { return fmt.Errorf("invalid landing cleanup prefix") }
 root := "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket)+"/o"
 token := ""
 for {
 data, err := g.REST.call(ctx, "GET", root+"?prefix="+url.QueryEscape(prefix)+"&pageToken="+url.QueryEscape(token), nil, 4<<20)
 if err != nil { return err }
 var page struct { Items []struct { Name string `json:"name"` } `json:"items"`; Next string `json:"nextPageToken"` }
 if err := json.Unmarshal(data, &page); err != nil { return err }
 for _, item := range page.Items {
 if !strings.HasPrefix(item.Name, prefix) { return fmt.Errorf("storage returned object outside landing prefix") }
 if _, err := g.REST.call(ctx, "DELETE", root+"/"+url.PathEscape(item.Name), nil, 1<<20); err != nil { return err }
 }
 if page.Next == "" { return nil }; if page.Next == token { return fmt.Errorf("storage repeated page token") }; token = page.Next
 }
}

type CloudBuildConfig struct {
 Project, Region, Bucket, MachineType, ServiceAccount string
 TimeoutMinutes int
}
type CloudBuild struct {
 Store *pebblestore.Store
 Config CloudBuildConfig
 Storage CloudStorage
 Builds CloudBuilds
}
func (p CloudBuild) timeout() time.Duration { return time.Duration(p.Config.TimeoutMinutes)*time.Minute }

func (p CloudBuild) Acquire(ctx context.Context, spec Spec) (Environment, error) {
 if p.Storage == nil || p.Builds == nil || p.Store == nil || p.Config.Bucket == "" || p.Config.Project == "" || p.timeout() <= 0 || spec.CheckTimeout < time.Second || spec.Definition.BaseImage == "" { return nil, &Failure{"Cloud Build configuration unavailable or invalid"} }
 id, err := pebblestore.GenerateToken(16); if err != nil { return nil, err }
 e := &cloudEnvironment{provider:p, spec:spec, prefix:"landings/"+id+"/", started:time.Now()}
 fail := func(err error) (Environment, error) { return nil, errors.Join(&Failure{err.Error()}, e.Release()) }
 payload, sha, cleanup, err := p.Store.CloudBuildPayload(spec.Account,id,spec.Checkout,spec.Commit)
 if err != nil { return fail(err) }
 err = p.Storage.Put(ctx,p.Config.Bucket,e.prefix+"payload.tar.gz",payload)
 err = errors.Join(err,payload.Close(),cleanup())
 if err != nil { return fail(err) }
 request, err := json.Marshal(struct {
 Definition Definition `json:"definition"`
 Checks []string `json:"checks"`
 Timeout int `json:"timeout"`
 Base string `json:"base"`
 Head string `json:"head"`
 SHA256 string `json:"sha256"`
 }{spec.Definition,spec.Checks,int(spec.CheckTimeout/time.Second),spec.BaseCommit,spec.Commit,sha})
 if err != nil { return fail(err) }
 if err := p.Storage.Put(ctx,p.Config.Bucket,e.prefix+"request.json",bytes.NewReader(request)); err != nil { return fail(err) }
 build, err := p.Builds.Create(ctx,Build{Timeout:fmt.Sprintf("%ds",int(p.timeout()/time.Second)),ServiceAccount:p.Config.ServiceAccount,Options:BuildOptions{MachineType:p.Config.MachineType,Logging:"CLOUD_LOGGING_ONLY"},Steps:[]BuildStep{{Name:spec.Definition.BaseImage,Entrypoint:"bash",Args:[]string{"-lc",cloudBuildScript(p.Config.Bucket,e.prefix,sha)}}}})
 if err != nil { return fail(err) }; if build.ID == "" { return fail(fmt.Errorf("Cloud Build returned no build ID")) }
 e.id = build.ID
 if spec.Selected != nil { spec.Selected("cloudbuild:"+e.id) }
 return e,nil
}
type cloudCheck struct {
 ExitCode int `json:"exit_code"`
 DurationMS int64 `json:"duration_ms"`
 Output string `json:"output"`
}
type cloudEnvironment struct {
 provider CloudBuild
 spec Spec
 prefix, id string
 started time.Time
 index int
 results []cloudCheck
}
func (e *cloudEnvironment) Sync(context.Context,string) error { return nil }
func (e *cloudEnvironment) Release() error {
 ctx,cancel := context.WithTimeout(context.Background(),30*time.Second); defer cancel()
 return e.provider.Storage.DeletePrefix(ctx,e.provider.Config.Bucket,e.prefix)
}
func (e *cloudEnvironment) Exec(ctx context.Context, req ExecRequest) (*ExecResult,error) {
 return nil, &Failure{"Cloud Build result execution not implemented"}
}
