package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/projectfiles"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/outbox"
)

// JobAutomationDeliverer hands a workflow's deliverables over (deliver steps)
// and finds what its produce steps wrote.
func (s *Server) JobAutomationDeliverer() jobautomation.Deliverer {
	return jobAutomationDeliverer{s: s}
}

type jobAutomationDeliverer struct{ s *Server }

// OutboxDeliveryWriter writes one workflow delivery's files and manifest to
// the machine's outbox and returns the manifest's key (outbox.Exporter's
// WriteDelivery when SWARM_OUTBOX_URL is set).
type OutboxDeliveryWriter func(ctx context.Context, deliveryID string, blobs []outbox.Blob, manifest map[string]any) (string, error)

// SetOutboxDeliveryWriter lets deliver steps export to the outbox.
func (s *Server) SetOutboxDeliveryWriter(write OutboxDeliveryWriter) { s.outboxDelivery = write }

// deliverableContent is a deliverable's bytes in a temporary file, checked
// against the checksum the deliverable carries.
type deliverableContent struct {
	path   string
	sha256 string
	size   int64
	name   string
	ctype  string
}

func (c deliverableContent) remove() { _ = os.Remove(c.path) }

// readDeliverable copies a file, media or report deliverable into a
// temporary file. A file whose content no longer has the deliverable's
// checksum is refused: what was reviewed is what is delivered.
func (d jobAutomationDeliverer) readDeliverable(ctx context.Context, folder string, result jobautomation.Result, item jobautomation.Deliverable) (deliverableContent, error) {
	name := filepath.Base(item.Path)
	var content deliverableContent
	var err error
	switch item.Source {
	case jobautomation.SourceGit:
		var blob outbox.Blob
		blob, err = outbox.NewBuilder("deliver", maxDeliverableFileBytes).File(ctx, folder, item.Head, item.Path)
		content = deliverableContent{path: blob.Path, sha256: blob.SHA256, size: blob.Size}
	case jobautomation.SourceFolder:
		var src string
		src, err = deliverFolderPath(folder, item.Path)
		if err == nil {
			content, err = copyToTemp(func() (io.ReadCloser, error) { return os.Open(src) })
		}
	case jobautomation.SourceMedia:
		content, err = d.mediaContent(result, item)
	case jobautomation.SourceReport:
		name = "report.md"
		report := result.Report
		content, err = copyToTemp(func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(report)), nil })
		item.SHA256 = content.sha256
	default:
		return deliverableContent{}, &jobautomation.RefusedError{Reason: "deliverable " + item.Name + " has no content to hand over (it is a " + item.Kind + ")"}
	}
	if err != nil {
		return deliverableContent{}, fmt.Errorf("read %s: %w", item.Name, err)
	}
	if item.SHA256 != "" && content.sha256 != item.SHA256 {
		content.remove()
		return deliverableContent{}, &jobautomation.RefusedError{Reason: "deliverable " + item.Name + " changed since the work recorded it (sha256 " + content.sha256 + ", recorded " + item.SHA256 + "): nothing was delivered"}
	}
	content.name = name
	content.ctype = firstNonEmptyString(item.Type, deliverableType(name))
	return content, nil
}

func copyToTemp(open func() (io.ReadCloser, error)) (deliverableContent, error) {
	src, err := open()
	if err != nil {
		return deliverableContent{}, err
	}
	defer src.Close()
	tmp, err := os.CreateTemp("", "swarm-deliver-*")
	if err != nil {
		return deliverableContent{}, err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(src, maxDeliverableFileBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil && n > maxDeliverableFileBytes {
		err = errors.New("larger than the deliverable limit")
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return deliverableContent{}, err
	}
	return deliverableContent{path: tmp.Name(), sha256: hex.EncodeToString(hash.Sum(nil)), size: n}, nil
}

func (d jobAutomationDeliverer) mediaContent(result jobautomation.Result, item jobautomation.Deliverable) (deliverableContent, error) {
	if d.s.sessions == nil || d.s.sessions.Store() == nil {
		return deliverableContent{}, errors.New("media is not available")
	}
	account := d.s.OutboxAccount()
	task, found, err := d.s.sessions.Store().GetProjectTask(account, result.ProjectID, result.RunID)
	if err != nil || !found || task == nil {
		return deliverableContent{}, fmt.Errorf("the media job %s is gone", result.JobID)
	}
	for _, file := range mediaJobFiles(task) {
		if file.name == item.Path && len(file.data) > 0 {
			data := file.data
			return copyToTemp(func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(data))), nil })
		}
	}
	return deliverableContent{}, fmt.Errorf("the media job has no file %s", item.Path)
}

// deliverFolderPath resolves a path inside the workflow's folder, refusing
// one that leaves it (symlinks included).
func deliverFolderPath(folder, rel string) (string, error) {
	root, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.Clean("/" + rel)[1:])
	resolved := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		resolved = r
	} else if parent, perr := filepath.EvalSymlinks(filepath.Dir(path)); perr == nil {
		resolved = filepath.Join(parent, filepath.Base(path))
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", &jobautomation.RefusedError{Reason: rel + " leaves the folder"}
	}
	return resolved, nil
}

// Deliver hands the deliverables over to the step's target.
func (d jobAutomationDeliverer) Deliver(ctx context.Context, req jobautomation.DeliverRequest) (jobautomation.DeliverOutcome, error) {
	switch req.To {
	case jobautomation.DeliverFolder:
		return d.toFolder(ctx, req)
	case jobautomation.DeliverOutbox:
		return d.toOutbox(ctx, req)
	case jobautomation.DeliverMerge, jobautomation.DeliverPublish:
		return d.toBranch(ctx, req)
	}
	return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "unknown deliver target " + req.To}
}

// toFolder copies each deliverable into the step's directory in the folder.
// An existing file with the same content is kept; a different one is never
// overwritten.
func (d jobAutomationDeliverer) toFolder(ctx context.Context, req jobautomation.DeliverRequest) (jobautomation.DeliverOutcome, error) {
	dir, err := deliverFolderPath(req.Folder, req.Path)
	if err != nil {
		return jobautomation.DeliverOutcome{}, err
	}
	if err := os.MkdirAll(dir, projectfiles.DirMode); err != nil {
		return jobautomation.DeliverOutcome{}, err
	}
	if again, err := deliverFolderPath(req.Folder, req.Path); err != nil || again != dir {
		return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: req.Path + " leaves the folder"}
	}
	out := jobautomation.DeliverOutcome{Where: dir}
	for _, item := range req.Deliverables {
		if item.Kind == jobautomation.DeliverableBranch {
			return out, &jobautomation.RefusedError{Reason: "a branch is delivered with to: merge or publish, not into a folder"}
		}
		content, err := d.readDeliverable(ctx, req.Folder, req.Result, item)
		if err != nil {
			return out, err
		}
		dest := filepath.Join(dir, content.name)
		if existing, err := copyToTemp(func() (io.ReadCloser, error) { return os.Open(dest) }); err == nil {
			existing.remove()
			if existing.sha256 != content.sha256 {
				content.remove()
				return out, &jobautomation.RefusedError{Reason: dest + " already exists with other content; Swarm never overwrites it: choose another path"}
			}
			content.remove()
			out.Delivered = append(out.Delivered, jobautomation.DeliveredItem{Name: item.Name, Kind: item.Kind, SHA256: content.sha256, Size: content.size, Where: dest})
			continue
		}
		err = installDeliverable(content.path, dest)
		content.remove()
		if err != nil {
			return out, err
		}
		out.Delivered = append(out.Delivered, jobautomation.DeliveredItem{Name: item.Name, Kind: item.Kind, SHA256: content.sha256, Size: content.size, Where: dest})
	}
	return out, nil
}

// installDeliverable copies src next to dest and renames it into place
// without replacing an existing file.
func installDeliverable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := projectfiles.CreateTemp(filepath.Dir(dest), ".swarm-deliver-")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		// A hard link fails when dest exists: never a silent overwrite.
		err = os.Link(tmp.Name(), dest)
	}
	_ = os.Remove(tmp.Name())
	return err
}

// toOutbox exports the deliverables and a manifest to the machine's outbox.
func (d jobAutomationDeliverer) toOutbox(ctx context.Context, req jobautomation.DeliverRequest) (jobautomation.DeliverOutcome, error) {
	if d.s.outboxDelivery == nil {
		return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "this machine has no outbox: its owner sets SWARM_OUTBOX_URL (a bucket or a folder) to deliver there"}
	}
	var blobs []outbox.Blob
	defer func() {
		for _, b := range blobs {
			_ = os.Remove(b.Path)
		}
	}()
	items := []map[string]any{}
	out := jobautomation.DeliverOutcome{}
	for _, item := range req.Deliverables {
		if item.Kind == jobautomation.DeliverableBranch {
			return out, &jobautomation.RefusedError{Reason: "a branch is delivered with to: merge or publish, not to the outbox"}
		}
		content, err := d.readDeliverable(ctx, req.Folder, req.Result, item)
		if err != nil {
			return out, err
		}
		blobs = append(blobs, outbox.Blob{Path: content.path, SHA256: content.sha256, Size: content.size, Type: content.ctype})
		items = append(items, map[string]any{"name": item.Name, "kind": item.Kind, "file": content.name, "sha256": content.sha256, "size": content.size, "type": content.ctype,
			"blob": "blobs/sha256/" + content.sha256, "from": item.From})
	}
	manifest := map[string]any{"kind": "workflow_delivery", "delivery_id": req.DeliveryID, "step": req.StepID, "job_id": req.Result.JobID, "folder": req.Folder,
		"brief": controlMCPTruncate(req.Result.Brief, 2000), "deliverables": items}
	key, err := d.s.outboxDelivery(ctx, req.DeliveryID, blobs, manifest)
	if err != nil {
		return out, err
	}
	out.Where = key
	for _, item := range items {
		out.Delivered = append(out.Delivered, jobautomation.DeliveredItem{Name: item["name"].(string), Kind: item["kind"].(string), SHA256: item["sha256"].(string),
			Size: item["size"].(int64), Where: key})
	}
	return out, nil
}

// toBranch delivers the work's branch the way a person's AI does by hand:
// swarm_integrate_task for a task, swarm_deliver_job for a worker job (a
// pull request where the folder's publish rule says so, else a local merge).
func (d jobAutomationDeliverer) toBranch(ctx context.Context, req jobautomation.DeliverRequest) (jobautomation.DeliverOutcome, error) {
	g := req.Result.Git
	if g == nil || g.Head == "" {
		return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "the work has no commits to " + req.To}
	}
	if req.Head != "" && req.Head != g.Head {
		return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "an earlier step moved the delivery head to " + req.Head + "; " + req.To + " delivers the work's own branch at " + g.Head +
			": deliver the moved head with a run step, or put this step before the step that commits"}
	}
	rule := false
	if d.s.publishConfig != "" {
		_, ok, err := d.s.publishRule(req.Folder)
		if err != nil {
			return jobautomation.DeliverOutcome{}, err
		}
		rule = ok
	}
	if req.To == jobautomation.DeliverPublish && !rule {
		return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "this folder has no publish rule (install.sh workspace publish): use to: merge, or ask its owner to set one"}
	}
	if req.To == jobautomation.DeliverMerge && rule {
		return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "this folder publishes as pull requests (its publish rule): use to: publish"}
	}
	c, err := d.s.jobAutomationWriteCall(ctx)
	if err != nil {
		return jobautomation.DeliverOutcome{}, err
	}
	// One integrate attempt per workflow attempt: the runner retries.
	c.workflowDelivery = true
	var response any
	if projectID, taskID, ok := parseTaskJobID(req.Result.JobID); ok {
		if req.To == jobautomation.DeliverPublish {
			return jobautomation.DeliverOutcome{}, &jobautomation.RefusedError{Reason: "a task of Swarm's own agents is merged (to: merge); publishing as a pull request is for worker jobs"}
		}
		response, err = controlMCPIntegrateTask(c, map[string]any{"project_id": projectID, "task_id": taskID})
	} else {
		response, err = controlMCPDeliverJob(c, map[string]any{"job_id": req.Result.JobID})
	}
	if err != nil {
		return jobautomation.DeliverOutcome{}, workflowDeliveryError(err)
	}
	where := deliveredBranchWhere(response)
	return jobautomation.DeliverOutcome{Where: where, Delivered: []jobautomation.DeliveredItem{{Name: "branch", Kind: jobautomation.DeliverableBranch, Where: where}}}, nil
}

// deliveredBranchWhere is one line for a merge or pull request result.
func deliveredBranchWhere(response any) string {
	encoded, _ := json.Marshal(response)
	var view map[string]any
	_ = json.Unmarshal(encoded, &view)
	for _, key := range []string{"pull_request", "pr_url", "url"} {
		if url, _ := view[key].(string); url != "" {
			return url
		}
	}
	integration := controlMCPMap(view["integration"])
	if head, _ := integration["resulting_target_head"].(string); head != "" {
		target, _ := integration["target_branch"].(string)
		where := strings.TrimSpace(firstNonEmptyString(target, "the folder's branch") + " at " + head)
		if warning, _ := integration["warning"].(string); warning != "" {
			where += "; warning: " + warning
		}
		return where
	}
	return controlMCPTruncate(string(encoded), 300)
}

// Locate finds the result file a produce step's job wrote: committed at its
// head in the folder's repository, or (an agent working in the folder) the
// file in the folder.
func (d jobAutomationDeliverer) Locate(ctx context.Context, folder string, spec jobautomation.ResultSpec, job jobautomation.AgentOutcome) (jobautomation.Deliverable, error) {
	found := jobautomation.Deliverable{Kind: spec.Kind, Path: spec.Path, Type: deliverableType(spec.Path)}
	if spec.Kind == jobautomation.DeliverableFile && outbox.MediaType(spec.Path) != "" {
		found.Kind = jobautomation.DeliverableMedia
	}
	head := job.Head
	if head == "" && job.Result != nil && job.Result.Git != nil {
		head = job.Result.Git.Head
	}
	if head != "" {
		if blob, err := outbox.NewBuilder("deliver", maxDeliverableFileBytes).File(ctx, folder, head, spec.Path); err == nil {
			_ = removeJobResultBlob(blob)
			found.Source, found.Head, found.SHA256, found.Size = jobautomation.SourceGit, head, blob.SHA256, blob.Size
			return found, nil
		}
	}
	path, err := deliverFolderPath(folder, spec.Path)
	if err != nil {
		return found, err
	}
	content, err := copyToTemp(func() (io.ReadCloser, error) { return os.Open(path) })
	if err != nil {
		if head != "" {
			return found, fmt.Errorf("it is not at the job's head %s nor in the folder", head)
		}
		return found, errors.New("it is not in the folder")
	}
	content.remove()
	found.Source, found.SHA256, found.Size = jobautomation.SourceFolder, content.sha256, content.size
	return found, nil
}

// transientInspectionCode reports a delivery assessment that could not read
// the Git state in time (or saw it move): nothing is wrong with the work, and
// reading it again later may succeed.
func transientInspectionCode(code string) bool {
	switch code {
	case "inspection_timeout", "inspection_cancelled", "capacity_exceeded", "heads_changed":
		return true
	}
	return false
}

// Only proven Git safety refusals bypass the workflow's configured retries.
// Known transient inspection failures use bounded automatic retries; other
// failures (including publish/network/store failures) retain configured retries.
func workflowDeliveryError(err error) error {
	var toolErr controlMCPToolError
	if !errors.As(err, &toolErr) {
		return err
	}
	code := toolErr.inspectionCode
	var body struct {
		Delivery pebblestore.TaskDeliveryAssessment `json:"delivery"`
	}
	_ = json.Unmarshal([]byte(toolErr.body), &body)
	if code == "" {
		code = body.Delivery.ReasonCode
	}
	if transientInspectionCode(code) {
		return &jobautomation.TransientInspectionError{Reason: err.Error()}
	}
	switch code {
	case "lane_branch_mismatch", "unresolved_recovery":
		return &jobautomation.RefusedError{Reason: err.Error()}
	}
	if body.Delivery.SourceDirty || body.Delivery.TargetDirty || body.Delivery.State == "dirty" || body.Delivery.State == "conflict" {
		return &jobautomation.RefusedError{Reason: err.Error()}
	}
	return err
}
