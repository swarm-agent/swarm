package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SessionVideoAttachmentVersion  = 1
	SessionVideoAttachmentMaxCount = 8
	SessionVideoAttachmentMaxBytes = int64(2 << 30)
)

// VideoSourceRecord is private account/workspace authority for one read-only
// source file. Absolute paths never leave the authenticated source-media API.
type VideoSourceRecord struct {
	Version           int    `json:"version"`
	Ref               string `json:"ref"`
	AccountScopeID    string `json:"account_scope_id"`
	WorkspaceID       string `json:"workspace_id"`
	RootPath          string `json:"root_path"`
	RelativePath      string `json:"relative_path"`
	DisplayName       string `json:"display_name"`
	MIMEType          string `json:"mime_type"`
	SizeBytes         int64  `json:"size_bytes"`
	ModifiedAt        int64  `json:"modified_at"`
	SourceFingerprint string `json:"source_fingerprint"`
	CreatedAt         int64  `json:"created_at"`
}

// SessionVideoAttachmentReference is the bounded, path-free message contract.
type SessionVideoAttachmentReference struct {
	Ref               string `json:"ref"`
	Name              string `json:"name"`
	MIMEType          string `json:"mime_type"`
	SizeBytes         int64  `json:"size_bytes"`
	SourceFingerprint string `json:"source_fingerprint"`
}

func KeyVideoSourceRecord(accountScopeID, workspaceID, ref string) string {
	return fmt.Sprintf("v3/video_source/%s/%s/%s", keyPart(accountScopeID), keyPart(workspaceID), keyPart(ref))
}

func (s *SessionStore) PutVideoSourceRecord(record VideoSourceRecord) (VideoSourceRecord, error) {
	if s == nil || s.store == nil {
		return VideoSourceRecord{}, errors.New("session store is not configured")
	}
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.WorkspaceID = strings.TrimSpace(record.WorkspaceID)
	record.RootPath = filepath.Clean(strings.TrimSpace(record.RootPath))
	record.RelativePath = filepath.Clean(strings.TrimSpace(record.RelativePath))
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	record.MIMEType = strings.ToLower(strings.TrimSpace(record.MIMEType))
	if record.AccountScopeID == "" || record.WorkspaceID == "" || record.RootPath == "." || record.RelativePath == "." {
		return VideoSourceRecord{}, errors.New("video source account, workspace, root, and relative path are required")
	}
	if filepath.IsAbs(record.RelativePath) || record.RelativePath == ".." || strings.HasPrefix(record.RelativePath, ".."+string(filepath.Separator)) {
		return VideoSourceRecord{}, errors.New("video source relative path escapes its registered root")
	}
	if record.SizeBytes <= 0 || record.SizeBytes > SessionVideoAttachmentMaxBytes || !strings.HasPrefix(record.MIMEType, "video/") {
		return VideoSourceRecord{}, errors.New("video source metadata exceeds the supported contract")
	}
	fingerprint := videoSourceFingerprint(record.RootPath, record.RelativePath, record.SizeBytes, record.ModifiedAt)
	if record.SourceFingerprint != "" && record.SourceFingerprint != fingerprint {
		return VideoSourceRecord{}, errors.New("video source fingerprint is inconsistent")
	}
	record.SourceFingerprint = fingerprint
	record.Ref = "videosrc_" + videoSourceDigest(strings.Join([]string{record.AccountScopeID, record.WorkspaceID, record.RootPath, record.RelativePath, fingerprint}, "\x00"))
	record.Version = SessionVideoAttachmentVersion
	if record.CreatedAt == 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if err := s.store.PutJSON(KeyVideoSourceRecord(record.AccountScopeID, record.WorkspaceID, record.Ref), record); err != nil {
		return VideoSourceRecord{}, err
	}
	return record, nil
}

func (s *SessionStore) GetVideoSourceRecord(accountScopeID, workspaceID, ref string) (VideoSourceRecord, bool, error) {
	var record VideoSourceRecord
	accountScopeID, workspaceID, ref = strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID), strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "videosrc_") || len(ref) != len("videosrc_")+64 {
		return record, false, errors.New("invalid video source reference")
	}
	ok, err := s.store.GetJSON(KeyVideoSourceRecord(accountScopeID, workspaceID, ref), &record)
	if err != nil || !ok {
		return VideoSourceRecord{}, ok, err
	}
	if record.AccountScopeID != accountScopeID || record.WorkspaceID != workspaceID || record.Ref != ref {
		return VideoSourceRecord{}, false, errors.New("video source ownership metadata is inconsistent")
	}
	return record, true, nil
}

func (s *SessionStore) ValidateSessionVideoAttachments(accountScopeID string, session SessionSnapshot, references []SessionVideoAttachmentReference) ([]SessionVideoAttachmentReference, error) {
	if len(references) == 0 {
		return nil, nil
	}
	if len(references) > SessionVideoAttachmentMaxCount {
		return nil, fmt.Errorf("message video attachment count exceeds %d", SessionVideoAttachmentMaxCount)
	}
	workspaceIDs := sessionVideoWorkspaceIDs(session)
	seen := make(map[string]struct{}, len(references))
	validated := make([]SessionVideoAttachmentReference, 0, len(references))
	for index, reference := range references {
		reference.Ref = strings.TrimSpace(reference.Ref)
		if _, duplicate := seen[reference.Ref]; duplicate {
			return nil, fmt.Errorf("video source %q is attached more than once", reference.Ref)
		}
		seen[reference.Ref] = struct{}{}
		var record VideoSourceRecord
		var ok bool
		var err error
		for _, workspaceID := range workspaceIDs {
			record, ok, err = s.GetVideoSourceRecord(accountScopeID, workspaceID, reference.Ref)
			if err != nil || ok {
				break
			}
		}
		if err != nil || !ok {
			if err == nil {
				err = errors.New("video source not found in the session workspace scope")
			}
			return nil, fmt.Errorf("video attachment %d: %w", index, err)
		}
		file, err := openValidatedVideoSource(record)
		if err != nil {
			return nil, fmt.Errorf("video attachment %d is stale or unavailable: %w", index, err)
		}
		file.Close()
		validated = append(validated, SessionVideoAttachmentReference{
			Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType,
			SizeBytes: record.SizeBytes, SourceFingerprint: record.SourceFingerprint,
		})
	}
	return validated, nil
}

func sessionVideoWorkspaceIDs(session SessionSnapshot) []string {
	ids := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, key := range []string{"swarm_v3_source_workspace_id", "workspace_id"} {
		value, _ := session.Metadata[key].(string)
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	fallback := transcriptionWorkspaceID(session)
	if _, exists := seen[fallback]; !exists {
		ids = append(ids, fallback)
	}
	return ids
}

// ValidateVideoSourceRecord revalidates one private source record without
// exposing its path or file handle to callers.
func ValidateVideoSourceRecord(record VideoSourceRecord) error {
	file, err := openValidatedVideoSource(record)
	if err != nil {
		return err
	}
	return file.Close()
}

// OpenValidatedVideoSource opens and validates a private source file.
// Callers must close the returned file when finished.
func OpenValidatedVideoSource(record VideoSourceRecord) (*os.File, error) {
	return openValidatedVideoSource(record)
}

func openValidatedVideoSource(record VideoSourceRecord) (*os.File, error) {
	root, err := os.OpenRoot(record.RootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(record.RelativePath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != record.SizeBytes || info.ModTime().UnixMilli() != record.ModifiedAt {
		file.Close()
		return nil, errors.New("source fingerprint changed")
	}
	var header [512]byte
	n, readErr := io.ReadFull(file, header[:])
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		file.Close()
		return nil, readErr
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(header[:n])))
	if !strings.HasPrefix(detected, "video/") {
		fallback := strings.ToLower(strings.TrimSpace(mime.TypeByExtension(filepath.Ext(record.RelativePath))))
		if !strings.HasPrefix(fallback, "video/") || !strings.HasPrefix(record.MIMEType, "video/") {
			file.Close()
			return nil, errors.New("source content is not a supported video")
		}
	}
	return file, nil
}

func videoSourceFingerprint(root, relative string, size, modifiedAt int64) string {
	return videoSourceDigest(strings.Join([]string{filepath.Clean(root), filepath.Clean(relative), fmt.Sprint(size), fmt.Sprint(modifiedAt)}, "\x00"))
}

func videoSourceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
