package api

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type providerKeyTailnetContext struct{}

var providerKeyTailnetHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.([a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.ts\.net)$`)

// SetProviderKeyTailnetOrigin enables browser capabilities only for the verified
// local Serve gateway. Never derive this authority from request headers.
func (s *Server) SetProviderKeyTailnetOrigin(origin string) bool {
	u, err := url.Parse(origin)
	valid := err == nil && u.Scheme == "https" && u.Port() == "8444" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && providerKeyTailnetHost.MatchString(u.Hostname()) && u.String() == origin
	s.providerKeys.mu.Lock()
	defer s.providerKeys.mu.Unlock()
	s.providerKeys.tailnetOrigin = ""
	if valid {
		s.providerKeys.tailnetOrigin = origin
	}
	return valid
}

func (reg *providerKeyRegistry) gatewayOrigin() string {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.tailnetOrigin
}

func providerKeyTailnetRequest(r *http.Request) bool {
	v, _ := r.Context().Value(providerKeyTailnetContext{}).(bool)
	return v
}

func (s *Server) providerKeyAllowedOrigin(origin string) bool {
	own, err := url.Parse(s.providerKeys.gatewayOrigin())
	if err != nil || own.Host == "" {
		return false
	}
	other, err := url.Parse(origin)
	if err != nil || other.Scheme != "https" || other.Port() != "8444" || other.User != nil || other.Path != "" || other.RawQuery != "" || other.Fragment != "" || other.String() != origin {
		return false
	}
	a, b := providerKeyTailnetHost.FindStringSubmatch(own.Hostname()), providerKeyTailnetHost.FindStringSubmatch(other.Hostname())
	return len(a) == 2 && len(b) == 2 && a[1] == b[1]
}

// Only Serve's loopback hop and user-device identity may redeem a capability.
// No app capability grant is needed by the human browser.
func (s *Server) serveTailnetProviderKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	code := strings.TrimPrefix(r.URL.Path, providerKeyPagePrefix)
	origin := s.providerKeys.gatewayOrigin()
	login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login"))
	if !remoteIsLoopback(r.RemoteAddr) || login == "" || len(login) > 320 || origin == "" || "https://"+r.Host != origin || !providerKeyCodePattern.MatchString(code) {
		http.Error(w, "key page unavailable", http.StatusForbidden)
		return
	}
	if _, ok := s.providerKeys.lookup(code); !ok {
		http.Error(w, providerKeyInvalidLink, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodOptions {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if supplied := r.Header.Get("Origin"); supplied != "" {
		if !s.providerKeyAllowedOrigin(supplied) {
			http.Error(w, "origin refused", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", supplied)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		if !s.providerKeyAllowedOrigin(r.Header.Get("Origin")) || r.Header.Get("Access-Control-Request-Method") != "POST" || (r.Header.Get("Access-Control-Request-Headers") != "" && !strings.EqualFold(r.Header.Get("Access-Control-Request-Headers"), "content-type")) {
			http.Error(w, "preflight refused", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), providerKeyTailnetContext{}, true))
	s.handleProviderKeyPage(w, r)
	if r.Method == http.MethodPost {
		// Outcome only: never log a code, URL, provider response or request body.
		outcome := "unavailable"
		s.providerKeys.mu.Lock()
		for _, entry := range s.providerKeys.latest {
			if entry.codeHash == providerKeyCodeHash(code) {
				outcome = entry.status
				break
			}
		}
		s.providerKeys.mu.Unlock()
		log.Printf("swarmd provider key: caller=%q outcome=%s", login, outcome)
	}
}

// The fragment stays in the browser. Validate every destination before reading
// the password; redirects are forbidden so a peer cannot redirect the key.
const providerKeyPageJS = `(() => {
const form = document.querySelector('form');
if (!form || !location.hash.startsWith('#also=')) return;
let pages;
try {
 const encoded = location.hash.slice(6);
 if (encoded.length > 32768) throw Error();
 pages = JSON.parse(atob(encoded.replace(/-/g, '+').replace(/_/g, '/')));
 const own = new URL(location.href);
 const host = /^[a-z0-9][a-z0-9-]*\.([a-z0-9][a-z0-9-]*\.ts\.net)$/;
 const tail = own.hostname.match(host);
 if (!tail || own.protocol !== 'https:' || own.port !== '8444' || !Array.isArray(pages) || pages.length > 49) throw Error();
 pages = pages.map(value => {
  const u = new URL(value), match = u.hostname.match(host);
  if (!match || match[1] !== tail[1] || u.protocol !== 'https:' || u.port !== '8444' || u.username || u.password || u.search || u.hash || !/^\/keys\/[A-Za-z0-9_-]{43}$/.test(u.pathname)) throw Error();
  return u;
 });
 pages.unshift(new URL(location.origin + location.pathname));
 if (new Set(pages.map(u => u.origin)).size !== pages.length) throw Error();
} catch {
 form.remove(); const p = document.createElement('p'); p.textContent = 'Invalid fleet link. Ask the AI for a new one.'; document.querySelector('main').append(p); return;
}
history.replaceState(null, '', location.pathname);
const results = document.createElement('div'); form.after(results);
const note = document.createElement('p'); note.textContent = 'This key will be sent directly from your browser to: ' + pages.map(u => u.hostname).join(', '); form.before(note);
form.addEventListener('submit', async event => {
 event.preventDefault();
 const input = form.querySelector('input'); let key = input.value; input.value = '';
 form.querySelector('button').disabled = true; results.replaceChildren();
 for (const u of pages) {
  const p = document.createElement('p'); results.append(p);
  p.textContent = u.hostname + ': saving…';
  try {
   const response = await fetch(u.href, {method:'POST', credentials:'omit', redirect:'error', referrerPolicy:'no-referrer', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:new URLSearchParams({api_key:key}), signal:AbortSignal.timeout(60000)});
   const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
   const message = doc.querySelector('[role="status"]')?.textContent;
   p.textContent = u.hostname + ': ' + (message || 'Request refused; ask the AI for the machine status.');
  } catch { p.textContent = u.hostname + ': Could not reach the machine. Ask the AI for its status before retrying.'; }
 }
 key = ''; form.remove();
});
})();`
