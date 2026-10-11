package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Provider key page. An AI connected to Swarm Control asks for a link
// (swarm_request_provider_key); the person opens it in a browser on this
// machine and pastes the key into Swarm's own form, so the key never passes
// through the AI. The link carries a single-use, 256-bit code that expires
// after providerKeyCodeTTL and is bound to one provider and to the account and
// key that asked for it. The form posts through the canonical credential
// route (POST /v1/auth/credentials, active), so the owner-only scope, vault,
// onboarding and verify-before-save rules apply unchanged.
//
// The page is served only by LoopbackAPIHandler (the loopback API listener):
// never by Handler (peer transport), the private socket, Desktop, the
// scoped-token listener, the tailnet gateway or the relay. Codes live in
// memory only; a restart forgets them.
const (
	providerKeyPagePrefix       = "/keys/"
	providerKeyCodeTTL          = 10 * time.Minute
	providerKeyOutcomeRetention = time.Hour
	providerKeyMaxAttempts      = 5
	providerKeyMaxPending       = 32
	providerKeyMaxBodyBytes     = 16 << 10
	providerKeyMaxLength        = 4096
	providerKeyMaxLabel         = 64
)

var providerKeyCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type providerKeyRegistry struct {
	mu     sync.Mutex
	addr   string
	byCode map[string]*providerKeyRequest
	latest map[string]*providerKeyRequest
	now    func() time.Time
}

type providerKeyRequest struct {
	provider       string
	label          string
	accountScopeID string
	userID         string
	token          pebblestore.ScopedTokenRecord
	codeHash       string
	expiresAt      time.Time
	status         string // waiting, submitting, saved, failed, expired
	reason         string
	attempts       int
}

// providerKeyOutcome is what swarm_list_providers reports about the latest
// key request for one provider: never the code, the link or the key.
type providerKeyOutcome struct {
	Status           string `json:"status"`
	Reason           string `json:"reason,omitempty"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
}

// SetProviderKeyPageAddr records the loopback API listener address once it is
// bound. A non-loopback address leaves the page off.
func (s *Server) SetProviderKeyPageAddr(addr string) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || port == "" || port == "0" {
		addr = ""
	}
	s.providerKeys.mu.Lock()
	s.providerKeys.addr = addr
	s.providerKeys.mu.Unlock()
}

func (reg *providerKeyRegistry) clock() time.Time {
	if reg.now != nil {
		return reg.now()
	}
	return time.Now()
}

func providerKeyCodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func providerKeySlot(accountScopeID, provider string) string {
	return accountScopeID + "\x00" + provider
}

// prune drops expired codes and old outcomes. Callers hold mu.
func (reg *providerKeyRegistry) prune(now time.Time) {
	for hash, request := range reg.byCode {
		if !now.Before(request.expiresAt) && request.status != "submitting" {
			if request.status == "waiting" || request.status == "failed" {
				request.status = "expired"
			}
			delete(reg.byCode, hash)
		}
	}
	for slot, request := range reg.latest {
		if now.After(request.expiresAt.Add(providerKeyOutcomeRetention)) {
			delete(reg.latest, slot)
		}
	}
}

// issue mints a code for one provider. A newer request for the same account
// and provider supersedes a pending one.
func (reg *providerKeyRegistry) issue(request providerKeyRequest) (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", errors.New("could not create a key link")
	}
	code := base64.RawURLEncoding.EncodeToString(raw[:])
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.addr == "" {
		return "", "", errors.New("the key page needs this machine's loopback API listener, which is not running")
	}
	now := reg.clock()
	reg.prune(now)
	if reg.byCode == nil {
		reg.byCode = map[string]*providerKeyRequest{}
		reg.latest = map[string]*providerKeyRequest{}
	}
	slot := providerKeySlot(request.accountScopeID, request.provider)
	if previous := reg.latest[slot]; previous != nil && previous.status != "submitting" {
		delete(reg.byCode, previous.codeHash)
		if previous.status == "waiting" || previous.status == "failed" {
			previous.status, previous.reason = "expired", "replaced by a newer link"
		}
	}
	if len(reg.byCode) >= providerKeyMaxPending {
		return "", "", errors.New("too many key links are open; wait for them to expire")
	}
	request.codeHash = providerKeyCodeHash(code)
	request.expiresAt = now.Add(providerKeyCodeTTL)
	request.status = "waiting"
	entry := request
	reg.byCode[entry.codeHash] = &entry
	reg.latest[slot] = &entry
	return code, "http://" + reg.addr + providerKeyPagePrefix + code, nil
}

// lookup returns a copy of a usable request for the form page.
func (reg *providerKeyRegistry) lookup(code string) (providerKeyRequest, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.prune(reg.clock())
	request, ok := reg.byCode[providerKeyCodeHash(code)]
	if !ok || (request.status != "waiting" && request.status != "failed") {
		return providerKeyRequest{}, false
	}
	return *request, true
}

// begin claims a code for one submission at a time.
func (reg *providerKeyRegistry) begin(code string) (*providerKeyRequest, providerKeyRequest, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.prune(reg.clock())
	request, ok := reg.byCode[providerKeyCodeHash(code)]
	if !ok || (request.status != "waiting" && request.status != "failed") {
		return nil, providerKeyRequest{}, false
	}
	request.status = "submitting"
	return request, *request, true
}

// finish records a submission. A saved key consumes the code; a failed one
// leaves it usable until it expires or runs out of attempts.
func (reg *providerKeyRegistry) finish(request *providerKeyRequest, saved bool, reason string) (retry bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	request.reason = reason
	if saved {
		request.status = "saved"
		delete(reg.byCode, request.codeHash)
		return false
	}
	request.attempts++
	request.status = "failed"
	if request.attempts >= providerKeyMaxAttempts || !reg.clock().Before(request.expiresAt) {
		delete(reg.byCode, request.codeHash)
		return false
	}
	return true
}

func (reg *providerKeyRegistry) outcome(accountScopeID, provider string) *providerKeyOutcome {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	now := reg.clock()
	reg.prune(now)
	request := reg.latest[providerKeySlot(accountScopeID, provider)]
	if request == nil {
		return nil
	}
	out := &providerKeyOutcome{Status: request.status, Reason: request.reason}
	_, open := reg.byCode[request.codeHash]
	if open && now.Before(request.expiresAt) {
		out.ExpiresInSeconds = int(request.expiresAt.Sub(now).Seconds())
	} else if request.status == "failed" || request.status == "waiting" {
		out.Status = "expired"
	}
	return out
}

func (reg *providerKeyRegistry) pageAddr() string {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.addr
}

// ---- page ----------------------------------------------------------------

const providerKeyPageCSS = `body{margin:0;font:15px/1.5 system-ui,-apple-system,sans-serif;background:#fafafa;color:#1a1a1a}` +
	`main{max-width:28rem;margin:4rem auto;padding:0 1rem}h1{font-size:1.25rem;margin:0 0 1rem}` +
	`label{display:block;margin:1rem 0 .25rem;font-weight:600}` +
	`input{width:100%;box-sizing:border-box;padding:.5rem;font:inherit;border:1px solid #bbb;border-radius:4px;background:#fff;color:inherit}` +
	`button{margin-top:.75rem;padding:.5rem 1rem;font:inherit;border:1px solid #222;border-radius:4px;background:#222;color:#fff;cursor:pointer}` +
	`.error{color:#a40000}.ok{color:#0a6b2d}.note{color:#666;font-size:13px}` +
	`@media (prefers-color-scheme:dark){body{background:#151515;color:#e8e8e8}input{background:#222;border-color:#555}` +
	`button{background:#e8e8e8;color:#151515;border-color:#e8e8e8}.error{color:#ff8a8a}.ok{color:#7ad69a}.note{color:#999}}`

var providerKeyPageCSP = func() string {
	sum := sha256.Sum256([]byte(providerKeyPageCSS))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; " +
		"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
}()

var providerKeyPageTemplate = template.Must(template.New("key").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Swarm: {{.Provider}} API key</title><style>{{.CSS}}</style></head>
<body><main>
<h1>{{if .Provider}}{{.Provider}} API key{{else}}Swarm{{end}}</h1>
{{if .Message}}<p class="{{.Class}}" role="status">{{.Message}}</p>{{end}}
{{if .Form}}<p>Paste your {{.Provider}} API key here. It goes straight to Swarm on this machine; the AI that sent you this link never sees it.</p>
<form method="post" autocomplete="off">
<label for="api_key">API key</label>
<input id="api_key" name="api_key" type="password" autocomplete="off" autocapitalize="off" spellcheck="false" maxlength="4096" required autofocus>
<button type="submit">Save key</button>
</form>
<p class="note">This link works once and expires {{.Expires}}. Do not paste the key into the chat.</p>{{end}}
</main></body></html>
`))

type providerKeyPage struct {
	Provider string
	Message  string
	Class    string
	Form     bool
	Expires  string
	CSS      template.CSS
}

func writeProviderKeyPage(w http.ResponseWriter, status int, page providerKeyPage) {
	page.CSS = template.CSS(providerKeyPageCSS)
	var body bytes.Buffer
	if err := providerKeyPageTemplate.Execute(&body, page); err != nil {
		http.Error(w, "page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

const providerKeyInvalidLink = "This link is not valid. It may have expired or already been used. Ask the AI for a new one."

func (s *Server) handleProviderKeyPage(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "same-origin")
	header.Set("Content-Security-Policy", providerKeyPageCSP)
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")

	if !s.providerKeyLoopbackRequest(r) {
		writeProviderKeyPage(w, http.StatusNotFound, providerKeyPage{Message: providerKeyInvalidLink, Class: "error"})
		return
	}
	code := strings.TrimPrefix(r.URL.Path, providerKeyPagePrefix)
	if !providerKeyCodePattern.MatchString(code) {
		writeProviderKeyPage(w, http.StatusNotFound, providerKeyPage{Message: providerKeyInvalidLink, Class: "error"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		request, ok := s.providerKeys.lookup(code)
		if !ok {
			writeProviderKeyPage(w, http.StatusNotFound, providerKeyPage{Message: providerKeyInvalidLink, Class: "error"})
			return
		}
		writeProviderKeyPage(w, http.StatusOK, providerKeyFormPage(request, s.providerKeys.clock(), ""))
	case http.MethodPost:
		s.submitProviderKey(w, r, code)
	default:
		header.Set("Allow", "GET, POST")
		writeProviderKeyPage(w, http.StatusMethodNotAllowed, providerKeyPage{Message: "Use the form.", Class: "error"})
	}
}

func providerKeyFormPage(request providerKeyRequest, now time.Time, message string) providerKeyPage {
	minutes := int(math.Ceil(request.expiresAt.Sub(now).Minutes()))
	expires := "in about a minute"
	if minutes > 1 {
		expires = "in " + strconv.Itoa(minutes) + " minutes"
	}
	page := providerKeyPage{Provider: request.provider, Form: true, Expires: expires}
	if message != "" {
		page.Message, page.Class = message, "error"
	}
	return page
}

// providerKeyLoopbackRequest admits only a direct request on the loopback
// listener: loopback peer, a Host naming that listener (so a DNS-rebinding
// name is refused), and no proxy headers (Tailscale Serve or any reverse
// proxy in front of the listener would add them).
func (s *Server) providerKeyLoopbackRequest(r *http.Request) bool {
	addr := s.providerKeys.pageAddr()
	if addr == "" || !remoteIsLoopback(r.RemoteAddr) {
		return false
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if r.Host != addr && !strings.EqualFold(r.Host, "localhost:"+port) {
		return false
	}
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip", TailscaleAppCapsHeader, "Tailscale-User-Login"} {
		if r.Header.Get(name) != "" {
			return false
		}
	}
	return true
}

// providerKeySameOrigin requires the form post to come from the page itself:
// Origin (or, without it, Referer) must be this loopback origin.
func providerKeySameOrigin(r *http.Request) bool {
	want := "http://" + r.Host
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return strings.EqualFold(origin, want)
	}
	referer, err := url.Parse(strings.TrimSpace(r.Header.Get("Referer")))
	if err != nil || referer.Host == "" {
		return false
	}
	return strings.EqualFold(referer.Scheme+"://"+referer.Host, want)
}

func providerKeyValid(key string) bool {
	if key == "" || utf8.RuneCountInString(key) > providerKeyMaxLength || !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Server) submitProviderKey(w http.ResponseWriter, r *http.Request, code string) {
	if !providerKeySameOrigin(r) {
		writeProviderKeyPage(w, http.StatusForbidden, providerKeyPage{Message: "This form must be sent from the page Swarm opened on this machine.", Class: "error"})
		return
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/x-www-form-urlencoded" {
		writeProviderKeyPage(w, http.StatusUnsupportedMediaType, providerKeyPage{Message: "Use the form.", Class: "error"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, providerKeyMaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeProviderKeyPage(w, http.StatusRequestEntityTooLarge, providerKeyPage{Message: "The form was too large.", Class: "error"})
		return
	}
	key := strings.TrimSpace(r.PostForm.Get("api_key"))
	request, snapshot, ok := s.providerKeys.begin(code)
	if !ok {
		writeProviderKeyPage(w, http.StatusNotFound, providerKeyPage{Message: providerKeyInvalidLink, Class: "error"})
		return
	}
	if !providerKeyValid(key) {
		retry := s.providerKeys.finish(request, false, "the key was empty or had spaces or control characters")
		s.writeProviderKeyFailure(w, snapshot, retry, "Paste the key alone, with no spaces or line breaks.")
		return
	}
	reason, err := s.saveProviderKey(r, snapshot, key)
	if err != nil {
		reason = providerKeyRedact(reason, key)
		retry := s.providerKeys.finish(request, false, reason)
		s.writeProviderKeyFailure(w, snapshot, retry, "The key was not saved: "+reason)
		return
	}
	s.providerKeys.finish(request, true, reason)
	writeProviderKeyPage(w, http.StatusOK, providerKeyPage{Provider: snapshot.provider, Message: "Saved. " + reason + " You can close this page and go back to the AI.", Class: "ok"})
}

func (s *Server) writeProviderKeyFailure(w http.ResponseWriter, request providerKeyRequest, retry bool, message string) {
	if retry {
		writeProviderKeyPage(w, http.StatusBadRequest, providerKeyFormPage(request, s.providerKeys.clock(), message))
		return
	}
	writeProviderKeyPage(w, http.StatusBadRequest, providerKeyPage{Provider: request.provider, Message: message + " This link is used up; ask the AI for a new one.", Class: "error"})
}

// saveProviderKey posts the key to the canonical credential route as the
// account and key that requested the link. It returns a plain outcome:
// on success how the key was verified, on failure the reason.
func (s *Server) saveProviderKey(r *http.Request, request providerKeyRequest, key string) (string, error) {
	if s.identitySessions == nil || s.security == nil {
		return "Swarm cannot check who asked for this link.", errors.New("identity unavailable")
	}
	actor, err := s.identitySessions.ActorForUserID(request.userID)
	if err != nil || !isCompleteProductActor(actor) || actor.AccountScopeID != request.accountScopeID {
		return "the account that asked for this link is no longer available", errors.New("actor unavailable")
	}
	if !s.providerKeyTokenStillValid(request.token) {
		return "the AI key that asked for this link was revoked or expired", errors.New("token invalid")
	}
	body, err := json.Marshal(map[string]any{
		"provider": request.provider,
		"type":     "api",
		"label":    request.label,
		"api_key":  key,
		"active":   true,
	})
	if err != nil {
		return "the key could not be sent to Swarm", err
	}
	sub, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "/v1/auth/credentials", bytes.NewReader(body))
	if err != nil {
		return "the key could not be sent to Swarm", err
	}
	sub.Host = r.Host
	sub.RemoteAddr = r.RemoteAddr
	sub.Header.Set("Content-Type", "application/json")
	token := request.token
	sub = requestWithScopedToken(requestWithActorContext(sub, actor), &token)
	recorder := &controlMCPRecorder{header: http.Header{}, status: http.StatusOK}
	s.internalRouteHandler().ServeHTTP(recorder, sub)
	var decoded struct {
		Error      string `json:"error"`
		Connection *struct {
			Connected bool   `json:"connected"`
			Message   string `json:"message"`
		} `json:"connection"`
	}
	_ = json.Unmarshal(recorder.body.Bytes(), &decoded)
	if recorder.overflow || recorder.status < 200 || recorder.status > 299 {
		reason := strings.TrimSpace(decoded.Error)
		if reason == "" {
			reason = strings.ToLower(http.StatusText(recorder.status))
		}
		return controlMCPTruncate(reason, 300), errors.New("save refused")
	}
	if decoded.Connection == nil {
		return request.provider + " offers no key check, so the key is saved unverified.", nil
	}
	return "Swarm verified the key with " + request.provider + ".", nil
}

// providerKeyTokenStillValid re-checks the requesting key at submit time: it
// and every key above it must still exist, unrevoked and unexpired.
func (s *Server) providerKeyTokenStillValid(token pebblestore.ScopedTokenRecord) bool {
	records, err := s.security.ListScopedTokens(token.AccountScopeID)
	if err != nil {
		return false
	}
	byID := make(map[string]pebblestore.ScopedTokenRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	now := time.Now().UnixMilli()
	id := token.ID
	for depth := 0; id != ""; depth++ {
		record, ok := byID[id]
		if !ok || depth > 16 || record.Revoked || (record.ExpiresAt > 0 && now >= record.ExpiresAt) {
			return false
		}
		id = record.ParentTokenID
	}
	return true
}

func (s *Server) internalRouteHandler() http.Handler {
	s.controlMCPRoutesOnce.Do(func() {
		s.controlMCPRoutes = s.withVaultGate(s.withJSON(s.apiMux()))
	})
	return s.controlMCPRoutes
}

// providerKeySecretish matches token-like words: long runs with a digit or a
// mask character, as provider errors echo partial keys ("sk-…****1234").
var providerKeySecretish = regexp.MustCompile(`[A-Za-z0-9_\-.*+/=]{12,}`)

// providerKeyRedact removes the submitted key and anything that looks like
// key material from a provider message before a person or an AI sees it.
func providerKeyRedact(message, key string) string {
	if key != "" {
		message = strings.ReplaceAll(message, key, "[redacted]")
	}
	message = providerKeySecretish.ReplaceAllStringFunc(message, func(word string) string {
		if strings.ContainsAny(word, "*0123456789") && !strings.HasPrefix(word, "http") && !strings.Contains(word, "//") {
			return "[redacted]"
		}
		return word
	})
	return controlMCPTruncate(strings.TrimSpace(message), 300)
}
