package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/identity"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// Media jobs over Swarm Control: an AI asks for an image, a video or a sound
// with the options the chosen model has (swarm_media_models lists them), and
// gets the files back: listed on the job, shown inline (images), or saved into
// one of the machine's workspaces. Generation itself is the media task path
// (projects_media.go); this file is how an AI reaches it.

const (
	controlMCPMediaInputMaxBytes  = 50 << 20
	controlMCPMediaInlineMaxBytes = 3 << 20
	controlMCPMediaInlineMax      = 4
)

// controlMCPMediaFile is one generated file of a media job.
type controlMCPMediaFile struct {
	record    pebblestore.ProjectTaskDeliverable
	mediaType string
	data      []byte
}

// mediaTask reads a media job's task straight from the store: its files are
// kept inline in the task record, which the HTTP view replaces with links.
func (c *controlMCPCall) mediaTask(projectID, taskID string) (*pebblestore.ProjectTaskRecord, error) {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || c.server.sessions == nil || c.server.sessions.Store() == nil {
		return nil, controlMCPToolFailure("media files are not available on this machine")
	}
	return c.server.readableMediaTask(principal, projectID, taskID)
}

// mediaFiles decodes a media job's generated files, in order.
func mediaTaskFiles(task *pebblestore.ProjectTaskRecord) []controlMCPMediaFile {
	files := []controlMCPMediaFile{}
	for _, d := range task.Deliverables {
		file := controlMCPMediaFile{record: d}
		if mediaType, data, ok := decodeMediaDataURL(d.MediaURL); ok {
			file.mediaType, file.data = mediaType, data
		}
		files = append(files, file)
	}
	return files
}

// decodeMediaDataURL reads a base64 data: URL.
func decodeMediaDataURL(value string) (string, []byte, bool) {
	header, payload, found := strings.Cut(strings.TrimSpace(value), ",")
	if !found || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return "", nil, false
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	return mediaType, data, true
}

// view is what an AI reads about one file.
func (f controlMCPMediaFile) view(index int) map[string]any {
	d := f.record
	out := map[string]any{"file": index, "id": d.ID, "kind": d.Kind, "status": d.Status, "cost_usd": d.CostUSD, "price_status": d.PriceStatus, "pricing_summary": d.PricingSummary}
	if d.Title != "" {
		out["title"] = controlMCPTruncate(d.Title, 160)
	}
	if len(f.data) > 0 {
		sum := sha256.Sum256(f.data)
		out["media_type"], out["size_bytes"], out["sha256"] = f.mediaType, len(f.data), hex.EncodeToString(sum[:])
	}
	for key, value := range map[string]string{"model": d.Model, "aspect_ratio": d.AspectRatio, "resolution": d.Resolution} {
		if value != "" {
			out[key] = value
		}
	}
	if d.DurationSeconds > 0 {
		out["duration_seconds"] = d.DurationSeconds
	}
	if d.Status == "failed" && d.Description != "" {
		out["error"] = controlMCPTruncate(d.Description, 400)
	}
	return out
}

// filename is the name a saved file gets: the job's title and its number.
func (f controlMCPMediaFile) filename(title string, index int) string {
	ext := ".bin"
	if exts, _ := mime.ExtensionsByType(f.mediaType); len(exts) > 0 {
		ext = exts[0]
	}
	switch f.mediaType {
	case "image/jpeg":
		ext = ".jpg"
	case "audio/mpeg", "audio/mp3":
		ext = ".mp3"
	case "video/mp4":
		ext = ".mp4"
	}
	slug := strings.Trim(controlMCPSlugPattern.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		slug = "media"
	}
	return slug + "-" + strconv.Itoa(index) + ext
}

var controlMCPSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// controlMCPMediaJobFiles adds a media job's files to its view, shows its
// images inline when asked, and saves the files into a workspace when asked.
func (c *controlMCPCall) controlMCPMediaJobFiles(out map[string]any, projectID, taskID string, args map[string]any) (any, error) {
	task, err := c.mediaTask(projectID, taskID)
	if err != nil {
		return nil, err
	}
	return mediaJobFileView(out, task, args, controlMCPMediaJobGuidance)
}

func mediaJobFileView(out map[string]any, task *pebblestore.ProjectTaskRecord, args map[string]any, guidance func(map[string]any, []controlMCPMediaFile)) (any, error) {
	files := mediaTaskFiles(task)
	views := make([]map[string]any, 0, len(files))
	for i, file := range files {
		views = append(views, file.view(i+1))
	}
	out["files"] = views
	var total float64
	priceStatus := ""
	for _, file := range files {
		if file.record.Status != "ready" {
			continue
		}
		total += file.record.CostUSD
		status := file.record.PriceStatus
		if status == "" || status == "unrecorded" {
			status = "unknown"
		}
		if priceStatus == "" || status == "unknown" {
			priceStatus = status
		} else if priceStatus != "unknown" && priceStatus != status {
			priceStatus = "known"
		}
	}
	if priceStatus == "" {
		priceStatus = "known"
	}
	if task.RetryOf != "" {
		out["retry_of"] = task.RetryOf
	}
	out["media_cost_usd"] = total
	out["cost_usd"] = total
	out["price_status"] = priceStatus
	if task.Model != "" {
		out["model"] = task.Model
	}
	guidance(out, files)
	show, _ := args["include_media"].(bool)
	if !show {
		return out, nil
	}
	images := []controlMCPImage{}
	notes := []string{}
	for i, file := range files {
		if !strings.HasPrefix(file.mediaType, "image/") || len(file.data) == 0 {
			continue
		}
		if len(images) >= controlMCPMediaInlineMax {
			notes = append(notes, fmt.Sprintf("file %d not shown: at most %d images per call", i+1, controlMCPMediaInlineMax))
			continue
		}
		if len(file.data) > controlMCPMediaInlineMaxBytes {
			notes = append(notes, fmt.Sprintf("file %d not shown: larger than %d MB; save it with swarm_save_media", i+1, controlMCPMediaInlineMaxBytes>>20))
			continue
		}
		images = append(images, controlMCPImage{mediaType: file.mediaType, data: file.data})
	}
	if len(notes) > 0 {
		out["media_notes"] = notes
	}
	return controlMCPRichResult{value: out, images: images}, nil
}

// controlMCPMediaJobGuidance specializes the generic job guidance for media.
func controlMCPMediaJobGuidance(out map[string]any, files []controlMCPMediaFile) {
	status, _ := out["status"].(string)
	switch status {
	case "needs_review", "completed":
		prefix := "the files are ready: "
		for _, file := range files {
			if strings.HasPrefix(file.mediaType, "image/") {
				prefix += "include_media shows the images, "
				break
			}
		}
		out["next"] = prefix + "swarm_save_media writes the files into a workspace; there is nothing to merge"
	case "failed", "blocked", "cancelled", "rejected":
		out["next"] = "the media job did not finish: read each file's error, then make a corrected request with swarm_request_media (a new job)"
		if status != "failed" {
			return
		}
		for _, file := range files {
			if file.record.Status != "failed" {
				continue
			}
			message := strings.ToLower(file.record.Description)
			if strings.Contains(message, "prohibited_content") || strings.Contains(message, "input blocked") {
				out["hint"] = "the provider refused the brief by its content policy: rephrase it; naming real artists, songs, films, brands or people often triggers this"
				out["next"] = "the media job did not finish: read each file's error, then rephrase the brief and make a corrected request with swarm_request_media (a new job)"
				break
			}
		}
	}
}

// saveMediaFiles writes a job's files into a folder inside a registered
// workspace: an absolute path below one, or a path relative to the job's own
// workspace. Existing files are never overwritten.
func (c *controlMCPCall) saveMediaFiles(task *pebblestore.ProjectTaskRecord, files []controlMCPMediaFile, saveTo string) ([]map[string]any, error) {
	dir, err := c.mediaSaveDir(task, saveTo)
	if err != nil {
		return nil, err
	}
	return saveMediaFiles(task, files, dir)
}

func saveMediaFiles(task *pebblestore.ProjectTaskRecord, files []controlMCPMediaFile, dir string, roots ...*os.Root) ([]map[string]any, error) {
	mkdirAll, readFile, openFile := os.MkdirAll, os.ReadFile, os.OpenFile
	if len(roots) > 0 {
		mkdirAll, readFile, openFile = roots[0].MkdirAll, roots[0].ReadFile, roots[0].OpenFile
	}
	if err := mkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	saved := []map[string]any{}
	for i, file := range files {
		if len(file.data) == 0 {
			continue
		}
		name := file.filename(task.Title, i+1)
		path := filepath.Join(dir, name)
		if existing, err := readFile(path); err == nil {
			if string(existing) == string(file.data) {
				saved = append(saved, map[string]any{"file": i + 1, "path": path, "unchanged": true})
				continue
			}
			suffix := task.ID
			if len(suffix) > 6 {
				suffix = suffix[len(suffix)-6:]
			}
			path = filepath.Join(dir, strings.TrimSuffix(name, filepath.Ext(name))+"-"+suffix+filepath.Ext(name))
		}
		handle, err := openFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return saved, fmt.Errorf("write %s: %w", path, err)
		}
		remove := os.Remove
		if len(roots) > 0 {
			remove = roots[0].Remove
		}
		if err := finishMediaSave(handle, file.data, func() error { return remove(path) }); err != nil {
			return saved, fmt.Errorf("write %s: %w", path, err)
		}
		saved = append(saved, map[string]any{"file": i + 1, "path": path, "size_bytes": len(file.data)})
	}
	return saved, nil
}

// mediaSaveDir resolves a folder inside a registered workspace, refusing any
// path that leaves it (also through a symlink).
func (c *controlMCPCall) mediaSaveDir(task *pebblestore.ProjectTaskRecord, saveTo string) (string, error) {
	roots, err := c.workspaceRoots()
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(task.WorkspacePath)
	if base == "" {
		base = c.projectFolder(task.ProjectID)
	}
	return mediaSaveDir(roots, base, saveTo, mcpMediaErrors)
}

type mediaErrorWording struct {
	missingBase, outsideWorkspace, missingFile, invalidPath string
}

var mcpMediaErrors = mediaErrorWording{
	missingBase:      "folder must be an absolute path inside a registered workspace: this job's project has no folder",
	outsideWorkspace: "folder %s is not inside a registered workspace (see swarm_list_projects)",
	missingFile:      "input %d: job %s has no ready file %d (swarm_get_job lists its files)",
	invalidPath:      "input %d: %s must be an absolute path inside a registered workspace",
}

func mediaSaveDir(roots []string, base, saveTo string, wording mediaErrorWording) (string, error) {
	target := saveTo
	if !filepath.IsAbs(target) {
		if base == "" {
			return "", errors.New(wording.missingBase)
		}
		target = filepath.Join(base, target)
	}
	target = filepath.Clean(target)
	root := controlMCPWorkspaceRootOf(roots, target)
	if root == "" {
		return "", fmt.Errorf(wording.outsideWorkspace, target)
	}
	// The deepest existing folder must still be inside the workspace once
	// symlinks are resolved.
	existing := target
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("folder %s leaves its workspace", target)
	}
	return target, nil
}

func (c *controlMCPCall) workspaceRoots() ([]string, error) {
	response, err := c.dispatch(http.MethodGet, "/v1/workspace/list", map[string][]string{"limit": {"500"}}, nil)
	if err != nil {
		return nil, err
	}
	roots := []string{}
	for _, raw := range controlMCPList(response["workspaces"]) {
		if path, _ := controlMCPMap(raw)["path"].(string); filepath.IsAbs(path) {
			roots = append(roots, filepath.Clean(path))
		}
	}
	return roots, nil
}

func controlMCPWorkspaceRootOf(roots []string, path string) string {
	best := ""
	for _, root := range roots {
		if (path == root || strings.HasPrefix(path, root+string(filepath.Separator))) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

// controlMCPMediaInputs turns the inputs of a media request into the task's
// attached media: a file of an earlier media job, or a file in a registered
// workspace. Bytes are read here, so the request carries exactly them.
func (c *controlMCPCall) controlMCPMediaInputs(raw any) ([]map[string]any, error) {
	return mediaInputs(raw, c.workspaceRoots, c.mediaTask, mcpMediaErrors)
}

func mediaInputs(raw any, workspaceRoots func() ([]string, error), mediaTask func(string, string) (*pebblestore.ProjectTaskRecord, error), wording mediaErrorWording, readInput ...func(string) ([]byte, string, error)) ([]map[string]any, error) {
	inputs := controlMCPList(raw)
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > 14 {
		return nil, controlMCPToolFailure("at most 14 inputs")
	}
	var roots []string
	attached := []map[string]any{}
	for i, item := range inputs {
		input := controlMCPMap(item)
		jobID, _ := input["job_id"].(string)
		path, _ := input["path"].(string)
		var data []byte
		var mediaType, name string
		switch {
		case jobID != "":
			kind, projectID, taskID, err := controlMCPParseJobID(map[string]any{"job_id": jobID})
			if err != nil {
				return nil, err
			}
			if kind != controlMCPJobMedia {
				return nil, controlMCPToolFailure("input %d: job %s is not a media job; give a file with path instead", i+1, jobID)
			}
			task, err := mediaTask(projectID, taskID)
			if err != nil {
				return nil, err
			}
			files := mediaTaskFiles(task)
			index := controlMCPInt(input, "file", 1)
			if index < 1 || index > len(files) || len(files[index-1].data) == 0 {
				return nil, controlMCPToolFailure(wording.missingFile, i+1, jobID, index)
			}
			data, mediaType = files[index-1].data, files[index-1].mediaType
			name = files[index-1].filename(task.Title, index)
		case path != "":
			if len(readInput) > 0 {
				var err error
				data, mediaType, err = readInput[0](path)
				if err != nil {
					return nil, err
				}
				name = filepath.Base(path)
				break
			}
			if roots == nil {
				var err error
				if roots, err = workspaceRoots(); err != nil {
					return nil, err
				}
			}
			clean := filepath.Clean(path)
			if !filepath.IsAbs(clean) || controlMCPWorkspaceRootOf(roots, clean) == "" {
				return nil, controlMCPToolFailure(wording.invalidPath, i+1, path)
			}
			info, err := os.Stat(clean)
			if err != nil || !info.Mode().IsRegular() {
				return nil, controlMCPToolFailure("input %d: %s is not a readable file", i+1, path)
			}
			if info.Size() > controlMCPMediaInputMaxBytes {
				return nil, controlMCPToolFailure("input %d: %s is larger than %d MB", i+1, path, controlMCPMediaInputMaxBytes>>20)
			}
			if data, err = os.ReadFile(clean); err != nil {
				return nil, controlMCPToolFailure("input %d: %s could not be read", i+1, path)
			}
			mediaType = mime.TypeByExtension(strings.ToLower(filepath.Ext(clean)))
			if mediaType == "" {
				mediaType = http.DetectContentType(data)
			}
			mediaType, _, _ = strings.Cut(mediaType, ";")
			name = filepath.Base(clean)
		default:
			return nil, controlMCPToolFailure("input %d needs job_id (an earlier media job) or path (a file in a workspace)", i+1)
		}
		kind := strings.SplitN(mediaType, "/", 2)[0]
		if kind != "image" && kind != "video" && kind != "audio" {
			return nil, controlMCPToolFailure("input %d is %s; inputs are images, videos or audio", i+1, mediaType)
		}
		attached = append(attached, map[string]any{
			"id": "input_" + strconv.Itoa(i+1), "kind": kind, "media_type": mediaType, "filename": name,
			"url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), "size_bytes": len(data),
		})
	}
	return attached, nil
}

// controlMCPListMediaModels lists the media models: which are ready, the
// default per kind, and each model's options, so a request names only values
// the model takes.
func controlMCPListMediaModels(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || c.server.imageGen == nil || c.server.model == nil {
		return nil, controlMCPToolFailure("media models are not available on this machine")
	}
	return c.server.mediaModels(c.request.Context(), principal, args, controlMCPMediaModelsGuidance, "%d more need a provider key (include_unready lists them; swarm_list_providers)")
}

const controlMCPMediaModelsGuidance = "Pass model and its options to swarm_request_media; a value the model does not take is refused before anything is spent. The default is used when no model is named; swarm_set_media_models changes it when the person asks."

func (s *Server) mediaModels(ctx context.Context, principal identity.Principal, args map[string]any, guidance, unreadyGuidance string) (any, error) {
	if s.imageGen == nil || s.model == nil {
		return nil, errors.New("media models are not available on this machine")
	}
	catalog, err := s.mediaCatalogFor(ctx, principal)
	if err != nil {
		return nil, err
	}
	image, video, audio := s.mediaDefaults(principal, catalog)
	wanted := controlMCPString(args, "kind")
	all, _ := args["include_unready"].(bool)
	out := map[string]any{}
	add := func(kind, def string, options []mediaCatalogOption) {
		if wanted != "" && wanted != kind {
			return
		}
		models := []map[string]any{}
		hidden := 0
		for _, option := range options {
			if !option.Ready && !all {
				hidden++
				continue
			}
			// Live and unsupported sound models remain unavailable without
			// a generation contract; supported speech models carry options.
			if kind == "sound" && option.GenerationOptions == nil {
				continue
			}
			models = append(models, controlMCPMediaModelView(option))
		}
		entry := map[string]any{"default": def, "models": models}
		if hidden > 0 {
			entry["not_ready"] = fmt.Sprintf(unreadyGuidance, hidden)
		}
		out[kind] = entry
	}
	add("image", image, catalog.ImageModels)
	add("video", video, catalog.VideoGenerationModels)
	add("sound", audio, catalog.AudioModels)
	out["next"] = guidance
	return out, nil
}

func controlMCPMediaModelView(option mediaCatalogOption) map[string]any {
	view := map[string]any{"model": option.ID, "provider": option.Provider}
	view["pricing"] = conciseMediaPricing(option.Pricing, audiogen.SpeechModel(option.ID))
	if option.DisplayName != "" && option.DisplayName != option.ID {
		view["name"] = option.DisplayName
	}
	if !option.Ready && option.Reason != "" {
		view["not_ready"] = controlMCPTruncate(option.Reason, 160)
	}
	opts := option.GenerationOptions
	if opts == nil {
		view["options"] = "unknown: Swarm has no settings data for this model and refuses option values for it"
		return view
	}
	for key, value := range map[string][]string{"aspect_ratios": opts.AspectRatios, "resolutions": opts.Resolutions} {
		if len(value) > 0 {
			view[key] = value
		}
	}
	if opts.Speech {
		view["operations"] = []string{"speech"}
		view["voices"], view["formats"], view["max_prompt_bytes"] = opts.Voices, opts.Formats, opts.MaxPromptBytes
	}
	if len(opts.Durations) > 0 {
		view["durations_seconds"] = opts.Durations
	}
	if len(opts.ResolutionDurations) > 0 {
		view["durations_by_resolution"] = opts.ResolutionDurations
	}
	defaults := map[string]any{}
	if opts.DefaultRatio != "" {
		defaults["aspect_ratio"] = opts.DefaultRatio
	}
	if opts.DefaultRes != "" {
		defaults["resolution"] = opts.DefaultRes
	}
	if opts.DefaultDur > 0 {
		defaults["duration_seconds"] = opts.DefaultDur
	}
	if len(defaults) > 0 {
		view["defaults"] = defaults
	}
	if opts.MaxOutputs > 0 {
		view["max_count"] = opts.MaxOutputs
	}
	if opts.InitialImage != nil {
		view["image_input"] = opts.InitialImage.Supported
	}
	if c := opts.Constraints; c != nil {
		view["operations"] = controlMCPVideoOperations(c)
	}
	return view
}

func controlMCPSaveMedia(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	kind, projectID, taskID, err := controlMCPParseJobID(args)
	if err != nil {
		return nil, err
	}
	if kind != controlMCPJobMedia {
		return nil, controlMCPToolFailure("%s is not a media job: a worker's files arrive by merging its job (swarm_deliver_job)", controlMCPString(args, "job_id"))
	}
	task, err := c.mediaTask(projectID, taskID)
	if err != nil {
		return nil, err
	}
	saved, err := c.saveMediaFiles(task, mediaTaskFiles(task), controlMCPString(args, "folder"))
	if err != nil {
		return nil, controlMCPToolFailure("%s", controlMCPTruncate(err.Error(), 300))
	}
	if len(saved) == 0 {
		return nil, controlMCPToolFailure("job %s has no ready files yet (swarm_get_job)", controlMCPString(args, "job_id"))
	}
	return map[string]any{"job_id": controlMCPJobID(kind, projectID, taskID), "saved": saved}, nil
}

// controlMCPVideoOperations is what a video model can do, in a line each.
func controlMCPVideoOperations(c *videogen.VideoOperationConstraints) map[string]any {
	op := func(supported bool, reason, detail string) any {
		if !supported {
			if reason == "" {
				return false
			}
			return "no: " + reason
		}
		if detail == "" {
			return true
		}
		return detail
	}
	create := ""
	if c.Create.InitialImageSupported {
		create = "yes; inputs may give one image as the first frame"
	}
	extend := ""
	if c.Extend.Supported {
		parts := []string{"yes; inputs give the video to extend"}
		if c.Extend.LockedDurationSeconds > 0 {
			parts = append(parts, fmt.Sprintf("adds %ds", c.Extend.LockedDurationSeconds))
		}
		if c.Extend.LockedResolution != "" {
			parts = append(parts, "at "+c.Extend.LockedResolution)
		}
		if c.Extend.MaxTotalDurationSec > 0 {
			parts = append(parts, fmt.Sprintf("up to %.0fs in total", c.Extend.MaxTotalDurationSec))
		}
		extend = strings.Join(parts, ", ")
	}
	return map[string]any{
		"create": op(c.Create.Supported, c.Create.Reason, create),
		"extend": op(c.Extend.Supported, c.Extend.Reason, extend),
		"edit":   op(c.Edit.Supported, c.Edit.Reason, ""),
	}
}

// projectFolder is a project's first folder: where a media job without a
// folder of its own saves relative paths.
func (c *controlMCPCall) projectFolder(projectID string) string {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || c.server.sessions == nil || c.server.sessions.Store() == nil {
		return ""
	}
	project, found, err := c.server.sessions.Store().GetProject(principal.AccountScopeID, projectID)
	if err != nil || !found || project == nil {
		return ""
	}
	for _, workspace := range project.Workspaces {
		if path := strings.TrimSpace(workspace.Path); filepath.IsAbs(path) {
			return path
		}
	}
	return ""
}

// controlMCPReadMediaChunk is the most of a file one swarm_read_media call
// returns (base64 keeps the answer under the tool size limit).
const controlMCPReadMediaChunk = 2 << 20

// controlMCPReadMedia returns part of one generated file of a media job, so a
// client can show or keep files of any size, a piece at a time.
func controlMCPReadMedia(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:read"); err != nil {
		return nil, err
	}
	projectID, err := controlMCPID(args, "project_id")
	if err != nil {
		return nil, err
	}
	taskID, err := controlMCPID(args, "task_id")
	if err != nil {
		return nil, err
	}
	task, err := c.mediaTask(projectID, taskID)
	if err != nil {
		return nil, err
	}
	files := mediaTaskFiles(task)
	index := controlMCPInt(args, "file", 1)
	if index < 1 || index > len(files) {
		return nil, controlMCPToolFailure("job has %d files; file is 1 to %d", len(files), len(files))
	}
	file := files[index-1]
	if len(file.data) == 0 {
		return nil, controlMCPToolFailure("file %d has no content yet (status %s)", index, file.record.Status)
	}
	offset := controlMCPInt(args, "offset", 0)
	if offset < 0 || offset > len(file.data) {
		return nil, controlMCPToolFailure("offset is 0 to %d", len(file.data))
	}
	length := controlMCPInt(args, "length", controlMCPReadMediaChunk)
	if length <= 0 || length > controlMCPReadMediaChunk {
		length = controlMCPReadMediaChunk
	}
	end := min(offset+length, len(file.data))
	out := file.view(index)
	out["filename"] = file.filename(task.Title, index)
	out["offset"] = offset
	out["data_base64"] = base64.StdEncoding.EncodeToString(file.data[offset:end])
	out["done"] = end == len(file.data)
	return out, nil
}

// conciseMediaPricing exposes catalog prices, never a raw catalog payload.
func conciseMediaPricing(raw json.RawMessage, speech ...bool) map[string]any {
	var catalog struct {
		Billing struct {
			Status string `json:"status"`
			Lines  []struct {
				Billable   string  `json:"billable"`
				Unit       string  `json:"unit"`
				Variant    string  `json:"variant"`
				Price      float64 `json:"price_usd"`
				Conditions struct {
					ServiceTier string `json:"service_tier"`
					Resolution  string `json:"resolution"`
				} `json:"conditions"`
			} `json:"lines"`
		} `json:"billing"`
	}
	out := map[string]any{"billing_status": "unknown"}
	if json.Unmarshal(raw, &catalog) != nil {
		return out
	}
	if catalog.Billing.Status != "" {
		out["billing_status"] = catalog.Billing.Status
	}
	prices := []map[string]any{}
	for _, line := range catalog.Billing.Lines {
		if line.Conditions.ServiceTier != "" && line.Conditions.ServiceTier != "standard" {
			continue
		}
		variant := strings.ToLower(line.Variant)
		if strings.HasPrefix(variant, "batch") || strings.HasPrefix(variant, "flex") || strings.HasPrefix(variant, "priority") {
			continue
		}
		speechTokens := len(speech) > 0 && speech[0] && line.Unit == "million_tokens" && (line.Billable == "text_input" || line.Billable == "audio_output")
		if !speechTokens && line.Unit != "image" && !(line.Unit == "million_tokens" && line.Billable == "image_output") && line.Unit != "second" {
			continue
		}
		item := map[string]any{"unit": line.Unit, "price_usd": line.Price, "billable": line.Billable}
		if line.Variant != "" {
			item["variant"] = line.Variant
		}
		if line.Conditions.Resolution != "" {
			item["resolution"] = line.Conditions.Resolution
		}
		prices = append(prices, item)
		if len(prices) == 12 {
			break
		}
	}
	out["prices"] = prices
	return out
}

func finishMediaSave(handle interface {
	Write([]byte) (int, error)
	Close() error
}, data []byte, remove func() error) error {
	n, writeErr := handle.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := handle.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr, remove())
	}
	return nil
}
