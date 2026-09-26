package api

import (
	"errors"
	"net/http"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) handleStorageBuckets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	if s.storageHub == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("storage hub service unavailable"))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/storage/buckets")
	path = strings.TrimPrefix(path, "/")

	switch r.Method {
	case http.MethodGet:
		buckets, err := s.storageHub.ListBuckets(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if buckets == nil {
			buckets = []pebblestore.StorageBucketRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"buckets": buckets,
			"count":   len(buckets),
		})

	case http.MethodPost:
		if strings.HasSuffix(path, "/scan") {
			bucketID := strings.TrimSuffix(path, "/scan")
			bucketID = strings.TrimSpace(bucketID)
			if bucketID == "" {
				writeError(w, http.StatusBadRequest, errors.New("bucket id required"))
				return
			}
			summary, err := s.storageHub.ScanBucket(r.Context(), accountScopeID, bucketID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"scan_summary": summary,
			})
			return
		}

		var input pebblestore.StorageBucketRecord
		if err := decodeJSONLimited(w, r, &input, 512*1024); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.AccountScopeID = accountScopeID
		registered, err := s.storageHub.RegisterBucket(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"bucket": registered,
		})

	case http.MethodDelete:
		if path == "" {
			writeError(w, http.StatusBadRequest, errors.New("bucket id required"))
			return
		}
		if err := s.storageHub.DeleteBucket(accountScopeID, path); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": true,
			"id":      path,
		})

	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleStorageBucketScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	if s.storageHub == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("storage hub service unavailable"))
		return
	}

	// Route: /v1/storage/buckets/{id}/scan
	path := strings.TrimPrefix(r.URL.Path, "/v1/storage/buckets/")
	bucketID := strings.TrimSuffix(path, "/scan")
	bucketID = strings.TrimSpace(bucketID)
	if bucketID == "" {
		writeError(w, http.StatusBadRequest, errors.New("bucket id required"))
		return
	}

	summary, err := s.storageHub.ScanBucket(r.Context(), accountScopeID, bucketID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scan_summary": summary,
	})
}

func (s *Server) handleStorageWorkers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	if s.storageHub == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("storage hub service unavailable"))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/storage/workers")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		workers, err := s.storageHub.ListDiscoveredWorkers(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if workers == nil {
			workers = []pebblestore.StorageDiscoveredWorkerRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"workers": workers,
			"count":   len(workers),
		})
		return
	}

	// Route: /v1/storage/workers/{workerId}/import
	if strings.HasSuffix(path, "/import") && r.Method == http.MethodPost {
		workerID := strings.TrimSuffix(path, "/import")
		workerID = strings.TrimSpace(workerID)
		if workerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("worker id required"))
			return
		}
		imported, err := s.storageHub.ImportWorker(r.Context(), accountScopeID, workerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"worker": imported,
		})
		return
	}

	methodNotAllowed(w)
}

func (s *Server) handleStorageDeliverables(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	accountScopeID, ok := s.notificationAccountScopeID(w, r)
	if !ok {
		return
	}
	if s.storageHub == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("storage hub service unavailable"))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/storage/deliverables")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		deliverables, err := s.storageHub.ListDiscoveredDeliverables(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if deliverables == nil {
			deliverables = []pebblestore.StorageDiscoveredDeliverableRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deliverables": deliverables,
			"count":        len(deliverables),
		})
		return
	}

	// Route: /v1/storage/deliverables/{id}/import
	if strings.HasSuffix(path, "/import") && r.Method == http.MethodPost {
		deliverableID := strings.TrimSuffix(path, "/import")
		deliverableID = strings.TrimSpace(deliverableID)
		if deliverableID == "" {
			writeError(w, http.StatusBadRequest, errors.New("deliverable id required"))
			return
		}

		var req struct {
			TargetWorkspacePath string `json:"target_workspace_path"`
		}
		_ = decodeJSONLimited(w, r, &req, 64*1024)

		targetDir := req.TargetWorkspacePath
		if targetDir == "" && s.workspace != nil {
			// Default to current workspace root if available
			if res, ok, _ := s.workspace.CurrentBinding(); ok && res.WorkspacePath != "" {
				targetDir = res.WorkspacePath
			}
		}

		imported, err := s.storageHub.ImportDeliverable(r.Context(), accountScopeID, deliverableID, targetDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deliverable": imported,
			"target_dir":  targetDir,
		})
		return
	}

	methodNotAllowed(w)
}
