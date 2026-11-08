// Package oauth is a single-owner OAuth 2.1 gate (authorization code + PKCE S256) that lets
// claude.ai connect to the MCP endpoint. The owner proves themselves with one password on the
// consent page; access tokens are stateless HMAC-signed blobs, so a restart keeps them valid and
// rotating the signing secret revokes all of them.
package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultClientID = "station-claude-connector"
	// DefaultTokenTTL is shorter than the usual year: the speaker is shared, and there is no
	// refresh grant, so an expired token just means pressing "connect" in claude.ai again.
	DefaultTokenTTL = 90 * 24 * 3600
	DefaultCodeTTL  = 300

	maxFailures = 5
	lockWindow  = 10 * time.Minute
	maxBody     = 64 << 10
)

// DefaultRedirectURIs are the callbacks claude.ai uses for custom connectors.
var DefaultRedirectURIs = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
}

type Config struct {
	Password     string
	TokenSecret  string
	ClientID     string
	ClientSecret string
	// PublicURL is the externally visible origin, e.g. https://station.example.com. Set it:
	// without it the origin is derived from request headers, which a client can influence.
	PublicURL    string
	RedirectURIs []string
	TokenTTL     int64 // seconds
	CodeTTL      int64 // seconds
}

type codeEntry struct {
	expiresAt     int64
	redirectURI   string
	codeChallenge string
}

type Server struct {
	cfg          Config
	redirectURIs map[string]bool

	mu    sync.Mutex
	codes map[string]codeEntry
	fails []time.Time // wrong-password attempts inside lockWindow
}

func NewServer(cfg Config) (*Server, error) {
	switch {
	case len(cfg.Password) < 12:
		return nil, errors.New("OAuth password must be at least 12 characters")
	case len(cfg.TokenSecret) < 32:
		return nil, errors.New("OAuth token secret must be at least 32 characters")
	case len(cfg.ClientSecret) < 24:
		return nil, errors.New("OAuth client secret must be at least 24 characters")
	}
	if cfg.PublicURL != "" {
		cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
		if !strings.HasPrefix(cfg.PublicURL, "https://") {
			return nil, errors.New("OAuth public URL must start with https://")
		}
	}
	if cfg.ClientID == "" {
		cfg.ClientID = DefaultClientID
	}
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = DefaultTokenTTL
	}
	if cfg.CodeTTL <= 0 {
		cfg.CodeTTL = DefaultCodeTTL
	}
	uris := cfg.RedirectURIs
	if len(uris) == 0 {
		uris = DefaultRedirectURIs
	}
	allowed := map[string]bool{}
	for _, u := range uris {
		if u = strings.TrimSpace(u); u != "" {
			allowed[u] = true
		}
	}
	return &Server{cfg: cfg, redirectURIs: allowed, codes: map[string]codeEntry{}}, nil
}

// Register mounts the OAuth endpoints and the discovery documents.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.handleProtectedResource)
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/token", s.handleToken)
}

// --- tokens ---

func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func mac(secret, body string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("station-mcp/oauth-token/v1\n" + body))
	return b64(h.Sum(nil))
}

type tokenPayload struct {
	ClientID string `json:"cid"`
	Exp      int64  `json:"exp"`
}

// Sign issues an access token for clientID that expires ttl seconds from now.
func Sign(secret, clientID string, ttl int64) string {
	raw, _ := json.Marshal(tokenPayload{ClientID: clientID, Exp: time.Now().Unix() + ttl})
	body := b64(raw)
	return body + "." + mac(secret, body)
}

// TokenValid reports whether token is one of ours and not expired.
func (s *Server) TokenValid(token string) bool {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || strings.Contains(sig, ".") {
		return false
	}
	if !hmac.Equal([]byte(sig), []byte(mac(s.cfg.TokenSecret, body))) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	var p tokenPayload
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	return p.ClientID == s.cfg.ClientID && p.Exp > time.Now().Unix()
}

// --- discovery ---

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

// Challenge is the WWW-Authenticate value for a 401 on /mcp: it tells the client where to start OAuth.
func (s *Server) Challenge(r *http.Request) string {
	return fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, s.baseURL(r))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (s *Server) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
	})
}

// --- authorization ---

var authFields = []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method"}

func (s *Server) requestError(p map[string]string) string {
	switch {
	case p["client_id"] != s.cfg.ClientID:
		return "Неизвестный client_id."
	case p["response_type"] != "code":
		return "Поддерживается только response_type=code."
	case !s.redirectURIs[p["redirect_uri"]]:
		return "Этот redirect_uri не в списке разрешённых."
	case p["code_challenge"] == "" || p["code_challenge_method"] != "S256":
		return "Нужен PKCE с методом S256."
	}
	return ""
}

func sameSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// locked reports whether too many wrong passwords were typed recently; it also forgets old ones.
func (s *Server) locked(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.fails[:0]
	for _, t := range s.fails {
		if now.Sub(t) < lockWindow {
			keep = append(keep, t)
		}
	}
	s.fails = keep
	return len(s.fails) >= maxFailures
}

func (s *Server) recordFailure(now time.Time) {
	s.mu.Lock()
	s.fails = append(s.fails, now)
	s.mu.Unlock()
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	_ = r.ParseForm()
	src := r.Form // query for GET, body wins over query for POST
	p := make(map[string]string, len(authFields))
	for _, f := range authFields {
		p[f] = src.Get(f)
	}
	view := consentView{TTLDays: int(s.cfg.TokenTTL / 86400)}
	if u, err := url.Parse(p["redirect_uri"]); err == nil {
		view.Host = u.Host
	}
	for _, f := range authFields {
		view.Hidden = append(view.Hidden, field{f, p[f]})
	}

	if msg := s.requestError(p); msg != "" {
		view.Fatal, view.Message = true, msg
		renderConsent(w, http.StatusBadRequest, view)
		return
	}
	if r.Method == http.MethodGet {
		renderConsent(w, http.StatusOK, view)
		return
	}

	now := time.Now()
	if s.locked(now) {
		view.Locked, view.Message = true, "Слишком много неверных паролей. Подождите несколько минут."
		renderConsent(w, http.StatusTooManyRequests, view)
		return
	}
	if !sameSecret(r.PostFormValue("password"), s.cfg.Password) {
		s.recordFailure(now)
		view.Message = "Неверный пароль."
		renderConsent(w, http.StatusUnauthorized, view)
		return
	}

	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	code := b64(buf)
	s.mu.Lock()
	for k, v := range s.codes {
		if v.expiresAt <= now.Unix() {
			delete(s.codes, k)
		}
	}
	s.codes[code] = codeEntry{expiresAt: now.Unix() + s.cfg.CodeTTL, redirectURI: p["redirect_uri"], codeChallenge: p["code_challenge"]}
	s.mu.Unlock()

	redir, _ := url.Parse(p["redirect_uri"])
	q := redir.Query()
	q.Set("code", code)
	if st := p["state"]; st != "" {
		q.Set("state", st)
	}
	redir.RawQuery = q.Encode()
	http.Redirect(w, r, redir.String(), http.StatusSeeOther)
}

// --- token endpoint ---

func tokenError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	body := map[string]string{}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				body[k] = fmt.Sprint(v)
			}
		}
	} else {
		_ = r.ParseForm()
		for k := range r.PostForm {
			body[k] = r.PostFormValue(k)
		}
	}

	id, sec := body["client_id"], body["client_secret"]
	if u, p, ok := r.BasicAuth(); ok && sec == "" {
		if id == "" {
			id = u
		}
		sec = p
	}
	if !sameSecret(id, s.cfg.ClientID) || !sameSecret(sec, s.cfg.ClientSecret) {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if body["grant_type"] != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}

	s.mu.Lock()
	entry, found := s.codes[body["code"]]
	delete(s.codes, body["code"]) // single use, even when the rest fails
	s.mu.Unlock()
	if !found || entry.expiresAt <= time.Now().Unix() || body["redirect_uri"] != entry.redirectURI {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	v := body["code_verifier"]
	sum := sha256.Sum256([]byte(v))
	if len(v) < 43 || len(v) > 128 || !hmac.Equal([]byte(b64(sum[:])), []byte(entry.codeChallenge)) {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": Sign(s.cfg.TokenSecret, s.cfg.ClientID, s.cfg.TokenTTL),
		"token_type":   "Bearer",
		"expires_in":   s.cfg.TokenTTL,
	})
}
