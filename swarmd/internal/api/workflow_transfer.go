package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/projectfiles"
)

const workflowFileLimit = 256 << 10

type workflowDefinition struct {
	Version int `json:"swarm_workflow"`
	jobAutomationBody
}

func (d workflowDefinition) MarshalJSON() ([]byte, error) {
	type plain workflowDefinition
	data, err := json.Marshal(plain(d))
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if d.Scope.Folder == "" {
		delete(fields["scope"].(map[string]any), "folder")
	}
	return json.Marshal(fields)
}

func portableWorkflow(a jobautomation.Automation, folder string) workflowDefinition {
	scope := a.Scope
	if filepath.Clean(scope.Folder) == filepath.Clean(folder) {
		scope.Folder = ""
	}
	return workflowDefinition{1, jobAutomationBody{On: a.On, Name: a.Name, Scope: scope, When: a.When, Enabled: &a.Enabled, Retries: a.Retries, Steps: a.Steps, Result: a.Result}}
}

func workflowDefinitionsEqual(a, b jobautomation.Automation) bool {
	return reflect.DeepEqual(portableWorkflow(a, ""), portableWorkflow(b, ""))
}

func decodeWorkflow(data []byte) (workflowDefinition, error) {
	var d workflowDefinition
	if len(data) > workflowFileLimit {
		return d, controlMCPToolFailure("workflow exceeds 256 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, controlMCPToolFailure("invalid workflow JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, controlMCPToolFailure("expected one workflow JSON object")
	}
	if d.Version != 1 {
		return d, controlMCPToolFailure("swarm_workflow must be version 1")
	}
	return d, nil
}

// os.Root confines every operation, including symlink resolution, to the
// selected folder even if a directory is replaced during an operation.
func workflowPath(path string) error {
	if !filepath.IsLocal(path) {
		return controlMCPToolFailure("workflow path must be relative and inside the project folder")
	}
	return nil
}

func (c *controlMCPCall) workflowExportFolder(folder string) (string, error) {
	response, err := c.dispatch(http.MethodGet, "/v3/projects", url.Values{"limit": {"200"}}, nil)
	if err != nil {
		return "", err
	}
	for _, raw := range controlMCPList(response["projects"]) {
		for _, workspace := range controlMCPProjectWorkspaces(controlMCPMap(raw)) {
			path, _ := workspace["path"].(string)
			if filepath.IsAbs(path) && filepath.Clean(path) == filepath.Clean(folder) {
				return path, nil
			}
		}
	}
	return "", controlMCPToolFailure("workflow export folder is not an account project folder")
}

func (s *Server) ExportWorkflow(caller WorkflowCaller, args map[string]any) (any, error) {
	c := s.workflowContext(caller)
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, err
	}
	var a jobautomation.Automation
	if id := controlMCPString(args, "id"); id != "" {
		a, err = store.Get(account, id)
	} else {
		a, err = store.FindByName(account, controlMCPString(args, "name"))
	}
	if err != nil {
		return nil, err
	}
	d := portableWorkflow(a, a.Scope.Folder)
	path, write := args["path"].(string)
	if !write {
		return d, nil
	}
	if err := c.requireTokenScope("automations:write"); err != nil {
		return nil, err
	}
	if path == "" {
		if strings.ContainsAny(a.Name, `/\\`) || a.Name == "." || a.Name == ".." {
			return nil, controlMCPToolFailure("specify a safe relative export path for this workflow name")
		}
		path = filepath.Join(".swarm", "workflows", a.Name+".json")
	}
	if err := workflowPath(path); err != nil {
		return nil, err
	}
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == ".git" {
			return nil, controlMCPToolFailure("workflow export paths must not contain .git")
		}
	}
	if !strings.HasSuffix(path, ".json") {
		return nil, controlMCPToolFailure("workflow export path must end in .json")
	}
	// A workflow's explicit scope is not filesystem authority. Resolve it
	// against the account-visible project folders before creating anything.
	folder, err := c.workflowExportFolder(a.Scope.Folder)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := workflowNoSymlinks(root, path); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := root.MkdirAll(filepath.Dir(path), projectfiles.DirMode); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, projectfiles.FileMode)
	if errors.Is(err, os.ErrExist) {
		old, readErr := readWorkflowFile(root, path)
		if readErr != nil {
			return nil, readErr
		}
		if bytes.Equal(old, data) {
			return d, nil
		}
		overwrite, _ := args["overwrite"].(bool)
		if !overwrite {
			return nil, controlMCPToolFailure("export file differs; pass overwrite: true to replace it")
		}
		// Replace the directory entry, never truncate a symlink or hardlink target.
		temp, tempErr := root.OpenFile(path+".swarm-new", os.O_WRONLY|os.O_CREATE|os.O_EXCL, projectfiles.FileMode)
		if tempErr != nil {
			return nil, tempErr
		}
		defer root.Remove(path + ".swarm-new")
		_, err = temp.Write(data)
		closeErr := temp.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if err := root.Rename(path+".swarm-new", path); err != nil {
			return nil, err
		}
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return d, nil
}

// Refuse symlink components as well as relying on Root confinement. This
// keeps file definitions unambiguous and avoids replacing linked destinations.
func workflowNoSymlinks(root *os.Root, path string) error {
	current := ""
	for _, component := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return controlMCPToolFailure("workflow paths must not contain symlinks")
		}
	}
	return nil
}

func readWorkflowFile(root *os.Root, path string) ([]byte, error) {
	if err := workflowNoSymlinks(root, path); err != nil {
		return nil, err
	}
	// Inspect before opening: opening a FIFO can block before f.Stat runs.
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, controlMCPToolFailure("workflow must be a regular file")
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, controlMCPToolFailure("workflow must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, workflowFileLimit+1))
	if err == nil && len(data) > workflowFileLimit {
		err = controlMCPToolFailure("workflow exceeds 256 KiB")
	}
	return data, err
}

func (s *Server) importWorkflowDefinition(caller WorkflowCaller, data []byte, folder string) (any, error) {
	d, err := decodeWorkflow(data)
	if err != nil {
		return nil, err
	}
	if d.Scope.Folder == "" {
		d.Scope.Folder = folder
	}
	encoded, err := json.Marshal(d.jobAutomationBody)
	if err != nil {
		return nil, err
	}
	var args map[string]any
	if err := json.Unmarshal(encoded, &args); err != nil {
		return nil, err
	}
	return s.saveWorkflowArgs(caller, args, true, "import")
}

func (s *Server) ImportWorkflow(caller WorkflowCaller, args map[string]any) (any, error) {
	c := s.workflowContext(caller)
	if _, _, err := s.workflowAccount(caller); err != nil {
		return nil, err
	}
	raw, definition := args["definition"]
	path, hasPath := args["path"].(string)
	if definition == hasPath {
		return nil, controlMCPToolFailure("provide exactly one of definition or path")
	}
	// Inline definitions with an explicit scope.folder need no project, just
	// like set. Files always require the canonical project/folder selection.
	folder := ""
	if hasPath || controlMCPString(args, "project") != "" || controlMCPString(args, "workspace") != "" {
		place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
		if err != nil {
			return nil, err
		}
		folder = place.path
	}
	if definition {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		return s.importWorkflowDefinition(caller, data, folder)
	}
	if err := workflowPath(path); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := workflowNoSymlinks(root, path); err != nil {
		return nil, err
	}
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		data, err := readWorkflowFile(root, path)
		if err != nil {
			return nil, err
		}
		return s.importWorkflowDefinition(caller, data, folder)
	}
	dir, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(1001)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 1000 {
		return nil, controlMCPToolFailure("workflow directory exceeds 1000 entries")
	}
	results := []map[string]any{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		file := filepath.Join(path, entry.Name())
		data, err := readWorkflowFile(root, file)
		var out any
		if err == nil {
			out, err = s.importWorkflowDefinition(caller, data, folder)
		}
		result := map[string]any{"path": file}
		if err != nil {
			result["status"] = "refused"
			result["error"] = err.Error()
			var refusal controlMCPToolError
			if errors.As(err, &refusal) {
				result["error"] = refusal.message
			}
		} else {
			result["status"] = "updated"
			view := out.(map[string]any)
			if view["created"] == true {
				result["status"] = "created"
			}
			if view["unchanged"] == true {
				result["status"] = "unchanged"
			}
		}
		results = append(results, result)
	}
	return map[string]any{"files": results}, nil
}

func (s *Server) handleWorkflowTransfer(w http.ResponseWriter, r *http.Request, action string) {
	var body struct {
		Name       string          `json:"name,omitempty"`
		ID         string          `json:"id,omitempty"`
		Path       *string         `json:"path,omitempty"`
		Project    string          `json:"project,omitempty"`
		Workspace  string          `json:"workspace,omitempty"`
		Overwrite  bool            `json:"overwrite,omitempty"`
		Definition json.RawMessage `json:"definition,omitempty"`
	}
	if err := decodeJSONStrict(w, r, workflowFileLimit, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	if action == "export" && body.Path != nil && !s.requireScope(w, r, "automations:write") {
		return
	}
	data, _ := json.Marshal(body)
	var args map[string]any
	_ = json.Unmarshal(data, &args)
	caller := workflowCallerFromRequest(r)
	var out any
	var err error
	if action == "export" {
		out, err = s.ExportWorkflow(caller, args)
	} else {
		out, err = s.ImportWorkflow(caller, args)
	}
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), errRunStepNeedsOwner.Error()) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, 200, out)
}

func workflowTransferTools() []controlMCPTool {
	common := map[string]any{"path": controlMCPStringProp("Relative file or directory inside the project folder.", 0, 4096), "project": controlMCPStringProp("Project id or name.", 0, 256), "workspace": controlMCPStringProp("Project folder when several are linked.", 0, 4096), "definition": map[string]any{"type": "object", "description": "Portable JSON object with swarm_workflow: 1; mutually exclusive with path."}}
	return []controlMCPTool{
		{Name: "swarm_export_workflow", Title: "Export workflow", Description: "Export portable workflow JSON by name or id. Supply path (empty for .swarm/workflows/<name>.json) to write inside its folder; different files require overwrite: true. Keep workflows as code: export to .swarm/workflows, commit, then swarm_import_workflow on another machine.", InputSchema: controlMCPObject(nil, map[string]any{"name": controlMCPStringProp("Workflow name.", 0, 200), "id": controlMCPStringProp("Workflow id.", 0, 200), "path": controlMCPStringProp("Optional relative destination; empty selects .swarm/workflows/<name>.json.", 0, 4096), "overwrite": map[string]any{"type": "boolean"}}), Annotations: controlMCPWrites, call: controlMCPExportWorkflow},
		{Name: "swarm_import_workflow", Title: "Import workflow", Description: fmt.Sprintf("Import portable JSON (swarm_workflow: %d) from definition or a project-relative path. A directory imports every *.json with created/updated/unchanged/refused per file. Uses set validation and updates by name; changes are retained by swarm_workflow_history and reversible with swarm_restore_workflow. Run steps require the owner's full key, never a write key. Export to .swarm/workflows, commit, import on another machine; omitted scope.folder binds to that project folder. No watcher.", 1), InputSchema: controlMCPObject(nil, common), Annotations: controlMCPWrites, call: controlMCPImportWorkflow},
	}
}
