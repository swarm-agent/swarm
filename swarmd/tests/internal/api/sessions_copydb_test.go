package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "swarm/packages/swarmd/internal/api"
	providerdefaults "swarm/packages/swarmd/internal/provider/defaults"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestListSessionsEndpointReturnsAllSessionsFromCopiedDB(t *testing.T) {
	for _, sessionCount := range []int{250, 500} {
		t.Run(fmt.Sprintf("sessions_%d", sessionCount), func(t *testing.T) {
			handler, cleanup := newCopiedSessionHandler(t, sessionCount)
			t.Cleanup(cleanup)

			resp := listSessionsViaHandler(t, handler, sessionCount+32)
			if len(resp.Sessions) != sessionCount {
				t.Fatalf("sessions returned = %d, want %d", len(resp.Sessions), sessionCount)
			}

			seen := make(map[string]struct{}, len(resp.Sessions))
			workspaceHits := make(map[string]int)
			for _, session := range resp.Sessions {
				if strings.TrimSpace(session.ID) == "" {
					t.Fatal("expected every session to have an id")
				}
				if _, ok := seen[session.ID]; ok {
					t.Fatalf("duplicate session id %q", session.ID)
				}
				seen[session.ID] = struct{}{}
				workspaceHits[session.WorkspacePath]++
			}
			if len(workspaceHits) < 8 {
				t.Fatalf("expected sessions to span multiple workspaces, got %d", len(workspaceHits))
			}
		})
	}
}

func BenchmarkListSessionsEndpointFromCopiedDB(b *testing.B) {
	for _, sessionCount := range []int{250, 500} {
		b.Run(fmt.Sprintf("sessions_%d", sessionCount), func(b *testing.B) {
			handler, cleanup := newCopiedSessionHandler(b, sessionCount)
			defer cleanup()

			requestPath := fmt.Sprintf("/v1/sessions?limit=%d", sessionCount+32)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, requestPath, nil)
				handler.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					b.Fatalf("list sessions status = %d, want %d", recorder.Code, http.StatusOK)
				}

				var resp listSessionsResponse
				if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
					b.Fatalf("decode response: %v", err)
				}
				if len(resp.Sessions) != sessionCount {
					b.Fatalf("sessions returned = %d, want %d", len(resp.Sessions), sessionCount)
				}
			}
		})
	}
}

type listSessionsResponse struct {
	OK       bool                          `json:"ok"`
	Sessions []pebblestore.SessionSnapshot `json:"sessions"`
}

func listSessionsViaHandler(t *testing.T, handler http.Handler, limit int) listSessionsResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/sessions?limit=%d", limit), nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list sessions status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var resp listSessionsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func newCopiedSessionHandler(tb testing.TB, sessionCount int) (http.Handler, func()) {
	tb.Helper()

	sourceDBPath := filepath.Join(tb.TempDir(), "source.pebble")
	seedSessionsDB(tb, sourceDBPath, sessionCount)

	copiedDBPath := filepath.Join(tb.TempDir(), "copied.pebble")
	copyDir(tb, sourceDBPath, copiedDBPath)

	store, err := pebblestore.Open(copiedDBPath)
	if err != nil {
		tb.Fatalf("open copied store: %v", err)
	}
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		tb.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := api.NewServer("test", nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	return server.Handler(), func() {
		_ = store.Close()
	}
}

func seedSessionsDB(tb testing.TB, dbPath string, sessionCount int) {
	tb.Helper()

	store, err := pebblestore.Open(dbPath)
	if err != nil {
		tb.Fatalf("open source store: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			tb.Fatalf("close source store: %v", err)
		}
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		tb.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)

	workspaceRoot := filepath.Join(tb.TempDir(), "workspaces")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		tb.Fatalf("mkdir workspace root: %v", err)
	}

	for i := 0; i < sessionCount; i++ {
		workspaceIndex := i % 12
		workspacePath := filepath.Join(workspaceRoot, fmt.Sprintf("workspace-%02d", workspaceIndex))
		if err := os.MkdirAll(workspacePath, 0o755); err != nil {
			tb.Fatalf("mkdir workspace: %v", err)
		}

		defaults := providerdefaults.MustLookup("codex")
		session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			Title:         fmt.Sprintf("Session %03d", i),
			WorkspacePath: workspacePath,
			WorkspaceName: fmt.Sprintf("Workspace %02d", workspaceIndex),
			Preference: &pebblestore.ModelPreference{
				Provider: defaults.ProviderID,
				Model:    defaults.PrimaryModel,
				Thinking: defaults.PrimaryThinking,
			},
		})
		if err != nil {
			tb.Fatalf("create session %d: %v", i, err)
		}

		if i%3 == 0 {
			if _, _, _, err := sessionSvc.AppendMessage(session.ID, "user", fmt.Sprintf("message %03d", i), nil); err != nil {
				tb.Fatalf("append message %d: %v", i, err)
			}
		}
	}
}

func copyDir(tb testing.TB, sourceDir, targetDir string) {
	tb.Helper()

	if err := filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(targetDir, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		sourceFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sourceFile.Close()

		targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer targetFile.Close()

		if _, err := io.Copy(targetFile, sourceFile); err != nil {
			return err
		}
		return nil
	}); err != nil {
		tb.Fatalf("copy db directory: %v", err)
	}
}
