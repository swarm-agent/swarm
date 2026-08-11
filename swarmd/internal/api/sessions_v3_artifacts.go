package api

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionsV3ArtifactMaxBytes int64 = 32 << 20

type sessionsV3ResolvedArtifact struct {
	Reference  pebblestore.SessionPlanArtifactReference
	Descriptor pebblestore.PlanFinalHandoffArtifact
}

func (s *Server) handleSessionV3Artifact(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	artifact, found, err := s.resolveSessionV3Artifact(sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	workspaceRoot := sessionV3ArtifactWorkspaceRoot(session)
	file, info, err := openSessionV3ArtifactFile(workspaceRoot, artifact.Reference.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact file is unavailable"))
		return
	}
	defer file.Close()
	if info.Size() > sessionsV3ArtifactMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("artifact exceeds the preview size limit"))
		return
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": artifact.Descriptor.Filename})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", artifact.Descriptor.MediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, artifact.Descriptor.Filename, info.ModTime(), file)
}

func (s *Server) resolveSessionV3Artifact(sessionID, artifactID string) (sessionsV3ResolvedArtifact, bool, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" || strings.Contains(artifactID, "/") {
		return sessionsV3ResolvedArtifact{}, false, nil
	}
	plans := make([]pebblestore.SessionPlanSnapshot, 0, sessionsV3PlansPageMaxLimit+1)
	if active, ok, err := s.sessions.GetActivePlan(sessionID); err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	} else if ok {
		plans = append(plans, active)
	}
	listed, _, err := s.sessions.ListPlans(sessionID, sessionsV3PlansPageMaxLimit)
	if err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	}
	for _, plan := range listed {
		if len(plans) == 0 || plan.ID != plans[0].ID {
			plans = append(plans, plan)
		}
	}
	for _, plan := range plans {
		if plan.Document == nil {
			continue
		}
		for _, checkpoint := range plan.Document.Checkpoints {
			if checkpoint.Handoff == nil || strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted {
				continue
			}
			artifacts := append([]pebblestore.SessionPlanArtifactReference(nil), plan.Document.Artifacts...)
			artifacts = append(artifacts, checkpoint.Artifacts...)
			for _, reference := range artifacts {
				descriptors := sessionruntime.ProjectPlanFinalHandoffArtifacts(plan.ID, checkpoint.ID, []pebblestore.SessionPlanArtifactReference{reference})
				if len(descriptors) == 1 && descriptors[0].ID == artifactID {
					return sessionsV3ResolvedArtifact{Reference: reference, Descriptor: descriptors[0]}, true, nil
				}
			}
		}
	}
	return sessionsV3ResolvedArtifact{}, false, nil
}

func sessionV3ArtifactWorkspaceRoot(session pebblestore.SessionSnapshot) string {
	for _, candidate := range []string{
		sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_workspace_path"),
		session.WorktreeRootPath,
		session.WorkspacePath,
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func openSessionV3ArtifactFile(workspaceRoot, artifactPath string) (*os.File, os.FileInfo, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	artifactPath = strings.TrimSpace(artifactPath)
	if workspaceRoot == "" || artifactPath == "" || filepath.IsAbs(artifactPath) || strings.Contains(artifactPath, "\\") {
		return nil, nil, errors.New("invalid artifact path")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(artifactPath))
	if cleanRelative == "." || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return nil, nil, errors.New("artifact path escapes workspace")
	}
	resolvedRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, nil, err
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, nil, err
	}
	candidate := filepath.Join(resolvedRoot, cleanRelative)
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, nil, err
	}
	resolvedCandidate, err = filepath.Abs(resolvedCandidate)
	if err != nil {
		return nil, nil, err
	}
	if filepath.Clean(candidate) != filepath.Clean(resolvedCandidate) {
		return nil, nil, errors.New("artifact path contains a symlink")
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, errors.New("artifact path escapes workspace")
	}
	root, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("artifact is not a regular file")
	}
	return file, info, nil
}
