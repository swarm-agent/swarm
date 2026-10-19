package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// No same-origin privileges, external resources, navigation or credentials are
// granted to generated HTML. Desktop uses sandbox="" for the PNG-only wrapper.
const designPreviewCSP = "sandbox; default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

func designAPIError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, pebblestore.ErrDesignNotFound) {
		code = http.StatusNotFound
	}
	if errors.Is(err, pebblestore.ErrDesignConflict) {
		code = http.StatusConflict
	}
	writeError(w, code, err)
}

func decodeDesignAPI(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 70000))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return pebblestore.ErrDesignInvalid
	}
	return nil
}

func (s *Server) handleSessionV3Designs(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, tail string) {
	parent, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil || !found || parent.AccountScopeID != principal.AccountScopeID || parent.UserID != principal.UserID {
		writeSessionNotFound(w)
		return
	}
	db := s.sessions.DesignStore()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("design store unavailable"))
		return
	}
	p := pebblestore.DesignPrincipal{AccountID: principal.AccountScopeID, PrincipalID: principal.UserID}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	tail = strings.Trim(tail, "/")
	if tail == "" && r.Method == http.MethodGet {
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			limit, err = strconv.Atoi(v)
		}
		if err != nil {
			designAPIError(w, pebblestore.ErrDesignInvalid)
			return
		}
		rows, cursor, err := db.ListSessionDesignRequests(p, sessionID, r.URL.Query().Get("after"), limit)
		if err != nil {
			designAPIError(w, err)
			return
		}
		archived, err := designArchiveView(r)
		if err != nil {
			designAPIError(w, err)
			return
		}
		rows = pebblestore.FilterDesignCatalogView(rows, archived)
		writeJSON(w, http.StatusOK, map[string]any{"requests": rows, "next_cursor": cursor})
		return
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[0] != "artifacts" {
		http.NotFound(w, r)
		return
	}
	artifactID := parts[1]
	a, err := db.RequireDesignArtifactSession(p, sessionID, artifactID)
	if err != nil {
		designAPIError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		after := uint64(0)
		if v := r.URL.Query().Get("after"); v != "" {
			after, err = strconv.ParseUint(v, 10, 64)
		}
		if err != nil {
			designAPIError(w, pebblestore.ErrDesignInvalid)
			return
		}
		history, err := db.DesignHistory(p, artifactID, after, 50)
		if err != nil {
			designAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifact": a, "revisions": history})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in struct {
		Action          string                        `json:"action"`
		Ref             pebblestore.DesignRef         `json:"ref"`
		Preview         *pebblestore.DesignPreviewRef `json:"preview,omitempty"`
		IdempotencyKey  string                        `json:"idempotency_key,omitempty"`
		ExpectedVersion *uint64                       `json:"expected_version"`
		ExpectedCurrent json.RawMessage               `json:"expected_current"`
		Brief           string                        `json:"brief,omitempty"`
	}
	if err := decodeDesignAPI(w, r, &in); err != nil {
		designAPIError(w, err)
		return
	}
	if in.Ref.ArtifactID != artifactID {
		designAPIError(w, pebblestore.ErrDesignInvalid)
		return
	}
	rev, err := db.ReadDesignRevision(p, in.Ref)
	if err != nil {
		designAPIError(w, err)
		return
	}
	switch in.Action {
	case "archive", "restore":
		if !s.requireScopeAny(w, r, "sessions:write") {
			return
		}
		if in.ExpectedVersion == nil {
			designAPIError(w, pebblestore.ErrDesignInvalid)
			return
		}
		updated, err := db.SetDesignArchived(p, sessionID, pebblestore.DesignArchive{IdempotencyKey: in.IdempotencyKey, ExpectedVersion: *in.ExpectedVersion, Ref: in.Ref, Archived: in.Action == "archive"})
		if err != nil {
			designAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifact": updated})
	case "select":
		if in.ExpectedVersion == nil || len(in.ExpectedCurrent) == 0 {
			designAPIError(w, pebblestore.ErrDesignInvalid)
			return
		}
		var current *pebblestore.DesignRef
		d := json.NewDecoder(bytes.NewReader(in.ExpectedCurrent))
		d.DisallowUnknownFields()
		if err := d.Decode(&current); err != nil {
			designAPIError(w, err)
			return
		}
		selected, err := db.SelectDesignRevision(p, pebblestore.DesignSelection{IdempotencyKey: in.IdempotencyKey, ExpectedVersion: *in.ExpectedVersion, ExpectedCurrent: current, Ref: in.Ref})
		if err != nil {
			designAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifact": selected})
	case "preview_png":
		if in.Preview == nil || rev.Attempt.Validation == nil || rev.Attempt.Validation.Preview == nil || *rev.Attempt.Validation.Preview != *in.Preview {
			designAPIError(w, pebblestore.ErrDesignConflict)
			return
		}
		data, err := db.ReadSessionDesignPreview(p, sessionID, *in.Preview)
		if err != nil {
			designAPIError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	case "preview_html":
		if rev.Attempt.Validation == nil || rev.Attempt.Validation.Preview == nil {
			designAPIError(w, pebblestore.ErrDesignNotFound)
			return
		}
		data, err := db.ReadSessionDesignPreview(p, sessionID, *rev.Attempt.Validation.Preview)
		if err != nil {
			designAPIError(w, err)
			return
		}
		w.Header().Set("Content-Security-Policy", designPreviewCSP)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Never execute authored HTML in the credential-bearing Desktop browser.
		_, _ = io.WriteString(w, `<!doctype html><html><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><title>Design preview</title><style>body{margin:0}img{display:block;max-width:100%;height:auto}</style><img alt="Design preview" src="data:image/png;base64,`+base64.StdEncoding.EncodeToString(data)+`"></html>`)
	case "live_source":
		// Exact immutable bytes, never an executable response on the authenticated
		// daemon origin. Desktop wraps these in its nested opaque-origin preview;
		// preview_html/preview_png remain static screenshot APIs.
		if rev.Kind == pebblestore.DesignPlan {
			designAPIError(w, pebblestore.ErrDesignInvalid)
			return
		}
		w.Header().Set("Content-Security-Policy", designPreviewCSP)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="design.html"`)
		_, _ = w.Write(rev.Content)
	case "read", "download":
		w.Header().Set("Content-Security-Policy", designPreviewCSP)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if in.Action == "download" {
			filename := "design.html"
			if rev.Kind == pebblestore.DesignPlan {
				filename = "design.txt"
			}
			w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		}
		_, _ = w.Write(rev.Content)
	case "edit":
		// Design edits were delegated through manage_design, which swarm_design
		// replaced. swarm_design edits a workspace HTML file in place (edit_path);
		// a stored design revision is not a workspace file, so nothing is queued.
		writeError(w, http.StatusGone, errors.New("design edit was removed with manage_design: save the design as an .html file in the workspace and edit it with swarm_design (edit_path)"))
	default:
		designAPIError(w, pebblestore.ErrDesignInvalid)
	}
}
