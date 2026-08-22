package pebblestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	AudioSourceRecordVersion       = 1
	AudioSourceFingerprintV1       = "sha256-root-relative-size-mtime.v1"
	AudioSourceMaxBytes      int64 = 2 << 30
)

// AudioSourceRecord is private account/workspace authority for one read-only
// audio file. Absolute paths never leave the authenticated source-media API.
type AudioSourceRecord struct {
	Version            int    `json:"version"`
	Ref                string `json:"ref"`
	AccountScopeID     string `json:"account_scope_id"`
	WorkspaceID        string `json:"workspace_id"`
	RootPath           string `json:"root_path"`
	RelativePath       string `json:"relative_path"`
	DisplayName        string `json:"display_name"`
	MIMEType           string `json:"mime_type"`
	SizeBytes          int64  `json:"size_bytes"`
	ModifiedAt         int64  `json:"modified_at"`
	SourceFingerprint  string `json:"source_fingerprint"`
	FingerprintVersion string `json:"fingerprint_version"`
	CreatedAt          int64  `json:"created_at"`
}

// AudioSourceReference is the bounded, path-free audio source contract.
type AudioSourceReference struct {
	Ref                string `json:"ref"`
	Name               string `json:"name"`
	MIMEType           string `json:"mime_type"`
	SizeBytes          int64  `json:"size_bytes"`
	SourceFingerprint  string `json:"source_fingerprint"`
	FingerprintVersion string `json:"fingerprint_version"`
}

func KeyAudioSourceRecord(accountScopeID, workspaceID, ref string) string {
	return fmt.Sprintf("v3/audio_source/%s/%s/%s", keyPart(accountScopeID), keyPart(workspaceID), keyPart(ref))
}

func (s *SessionStore) PutAudioSourceRecord(record AudioSourceRecord) (AudioSourceRecord, error) {
	if s == nil || s.store == nil {
		return AudioSourceRecord{}, errors.New("session store is not configured")
	}
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.WorkspaceID = strings.TrimSpace(record.WorkspaceID)
	record.RootPath = filepath.Clean(strings.TrimSpace(record.RootPath))
	record.RelativePath = filepath.Clean(strings.TrimSpace(record.RelativePath))
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	record.MIMEType = strings.ToLower(strings.TrimSpace(record.MIMEType))
	if record.AccountScopeID == "" || record.WorkspaceID == "" || record.RootPath == "." || record.RelativePath == "." || record.DisplayName == "" {
		return AudioSourceRecord{}, errors.New("audio source account, workspace, root, relative path, and name are required")
	}
	if filepath.IsAbs(record.RelativePath) || record.RelativePath == ".." || strings.HasPrefix(record.RelativePath, ".."+string(filepath.Separator)) {
		return AudioSourceRecord{}, errors.New("audio source relative path escapes its registered root")
	}
	if record.SizeBytes <= 0 || record.SizeBytes > AudioSourceMaxBytes || !strings.HasPrefix(record.MIMEType, "audio/") {
		return AudioSourceRecord{}, errors.New("audio source metadata exceeds the supported contract")
	}
	if expectedMIME, ok := supportedAudioMIMEForExtension(filepath.Ext(record.RelativePath)); !ok || record.MIMEType != expectedMIME {
		return AudioSourceRecord{}, errors.New("audio source extension and MIME type do not match the supported contract")
	}
	fingerprint := audioSourceFingerprint(record.RootPath, record.RelativePath, record.SizeBytes, record.ModifiedAt)
	if record.SourceFingerprint != "" && record.SourceFingerprint != fingerprint {
		return AudioSourceRecord{}, errors.New("audio source fingerprint is inconsistent")
	}
	record.SourceFingerprint = fingerprint
	record.FingerprintVersion = AudioSourceFingerprintV1
	record.Ref = audioSourceReference(record.AccountScopeID, record.WorkspaceID, record.RootPath, record.RelativePath, fingerprint)
	record.Version = AudioSourceRecordVersion
	if record.CreatedAt == 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if err := s.store.PutJSON(KeyAudioSourceRecord(record.AccountScopeID, record.WorkspaceID, record.Ref), record); err != nil {
		return AudioSourceRecord{}, err
	}
	return record, nil
}

func (s *SessionStore) GetAudioSourceRecord(accountScopeID, workspaceID, ref string) (AudioSourceRecord, bool, error) {
	var record AudioSourceRecord
	accountScopeID, workspaceID, ref = strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID), strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "audiosrc_") || len(ref) != len("audiosrc_")+64 {
		return record, false, errors.New("invalid audio source reference")
	}
	ok, err := s.store.GetJSON(KeyAudioSourceRecord(accountScopeID, workspaceID, ref), &record)
	if err != nil || !ok {
		return AudioSourceRecord{}, ok, err
	}
	if record.AccountScopeID != accountScopeID || record.WorkspaceID != workspaceID || record.Ref != ref {
		return AudioSourceRecord{}, false, errors.New("audio source ownership metadata is inconsistent")
	}
	return record, true, nil
}

// ValidateAudioSourceRecord revalidates one private source record without
// exposing its path or file handle to callers.
func ValidateAudioSourceRecord(record AudioSourceRecord) error {
	if record.Version != AudioSourceRecordVersion || record.FingerprintVersion != AudioSourceFingerprintV1 {
		return errors.New("audio source record version is unsupported")
	}
	fingerprint := audioSourceFingerprint(record.RootPath, record.RelativePath, record.SizeBytes, record.ModifiedAt)
	if record.SourceFingerprint != fingerprint || record.Ref != audioSourceReference(record.AccountScopeID, record.WorkspaceID, record.RootPath, record.RelativePath, fingerprint) {
		return errors.New("audio source identity metadata is inconsistent")
	}
	file, err := OpenValidatedAudioSource(record)
	if err != nil {
		return err
	}
	return file.Close()
}

// OpenValidatedAudioSource opens and validates a private audio source file.
// Callers must close the returned file when finished.
func OpenValidatedAudioSource(record AudioSourceRecord) (*os.File, error) {
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
		return nil, errors.New("audio source fingerprint changed")
	}
	mimeType, err := DetectSupportedAudioMIME(file, filepath.Ext(record.RelativePath))
	if err != nil || mimeType != record.MIMEType {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("audio source MIME type changed")
	}
	return file, nil
}

// DetectSupportedAudioMIME validates the extension and a bounded container or
// codec signature. It never accepts a file solely because of its extension.
func DetectSupportedAudioMIME(file *os.File, extension string) (string, error) {
	if file == nil {
		return "", errors.New("audio source file is required")
	}
	var header [512]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	data := header[:n]
	extension = strings.ToLower(strings.TrimSpace(extension))
	mimeType, ok := supportedAudioMIMEForExtension(extension)
	if !ok {
		return "", errors.New("audio extension is not supported")
	}
	matched := false
	switch extension {
	case ".mp3":
		matched = bytes.HasPrefix(data, []byte("ID3")) || (len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0)
	case ".wav":
		matched = len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE"))
	case ".m4a":
		matched = len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) &&
			(bytes.Contains(data[:min(len(data), 64)], []byte("M4A ")) ||
				bytes.Contains(data[:min(len(data), 64)], []byte("M4B ")) ||
				bytes.Contains(data[:min(len(data), 64)], []byte("M4P ")))
	case ".aac":
		matched = len(data) >= 2 && data[0] == 0xff && data[1]&0xf6 == 0xf0
	case ".flac":
		matched = bytes.HasPrefix(data, []byte("fLaC"))
	case ".ogg", ".oga", ".opus":
		matched = bytes.HasPrefix(data, []byte("OggS")) && (bytes.Contains(data, []byte("vorbis")) || bytes.Contains(data, []byte("OpusHead")) || bytes.Contains(data, []byte("Speex   ")) || bytes.Contains(data, []byte("fLaC")))
	}
	if !matched {
		return "", errors.New("file content does not match the supported audio extension")
	}
	return mimeType, nil
}

func supportedAudioMIMEForExtension(extension string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(extension)) {
	case ".mp3":
		return "audio/mpeg", true
	case ".wav":
		return "audio/wav", true
	case ".m4a":
		return "audio/mp4", true
	case ".aac":
		return "audio/aac", true
	case ".flac":
		return "audio/flac", true
	case ".ogg", ".oga", ".opus":
		return "audio/ogg", true
	default:
		return "", false
	}
}

func audioSourceReference(accountScopeID, workspaceID, root, relative, fingerprint string) string {
	return "audiosrc_" + audioSourceDigest(strings.Join([]string{strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID), filepath.Clean(root), filepath.Clean(relative), fingerprint}, "\x00"))
}

func audioSourceFingerprint(root, relative string, size, modifiedAt int64) string {
	return audioSourceDigest(strings.Join([]string{filepath.Clean(root), filepath.Clean(relative), fmt.Sprint(size), fmt.Sprint(modifiedAt)}, "\x00"))
}

func audioSourceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
