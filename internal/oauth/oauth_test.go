package oauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ilodezis/yandex-station-mcp/internal/oauth"
)

const (
	pass         = "owner-password-123"
	secret       = "token-secret-token-secret-token-secret-0123"
	clientID     = "station-claude-connector"
	clientSecret = "client-secret-client-secret-01"
	callback     = "https://claude.ai/api/mcp/auth_callback"
	verifier     = "verifier-verifier-verifier-verifier-verifier-0123"
)

func challengeOf(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func newGate(t *testing.T, mut ...func(*oauth.Config)) (*oauth.Server, *httptest.Server) {
	t.Helper()
	cfg := oauth.Config{Password: pass, TokenSecret: secret, ClientSecret: clientSecret}
	for _, m := range mut {
		m(&cfg)
	}
	g, err := oauth.NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	g.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return g, ts
}

// noRedirect keeps the 303 to the callback visible instead of following it.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func authParams() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {callback},
		"state":                 {"st-1"},
		"code_challenge":        {challengeOf(verifier)},
		"code_challenge_method": {"S256"},
	}
}

func post(t *testing.T, ts *httptest.Server, path string, v url.Values) *http.Response {
	t.Helper()
	resp, err := noRedirect.PostForm(ts.URL+path, v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// authorize submits the consent form and returns the code from the callback redirect.
func authorize(t *testing.T, ts *httptest.Server, password string) (code string, resp *http.Response) {
	t.Helper()
	v := authParams()
	v.Set("password", password)
	resp = post(t, ts, "/oauth/authorize", v)
	if resp.StatusCode != http.StatusSeeOther {
		return "", resp
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("state"); got != "st-1" {
		t.Errorf("state = %q, want st-1", got)
	}
	return loc.Query().Get("code"), resp
}

func tokenForm(code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback},
		"code_verifier": {verifier}, "client_id": {clientID}, "client_secret": {clientSecret},
	}
}

func exchange(t *testing.T, ts *httptest.Server, v url.Values) (int, map[string]any) {
	t.Helper()
	resp := post(t, ts, "/oauth/token", v)
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestFullFlowIssuesValidToken(t *testing.T) {
	g, ts := newGate(t)
	code, resp := authorize(t, ts, pass)
	if code == "" {
		t.Fatalf("no code, status %d", resp.StatusCode)
	}
	status, out := exchange(t, ts, tokenForm(code))
	if status != 200 {
		t.Fatalf("token status %d: %v", status, out)
	}
	tok, _ := out["access_token"].(string)
	if !g.TokenValid(tok) {
		t.Error("issued token is not valid")
	}
	if out["token_type"] != "Bearer" {
		t.Errorf("token_type = %v", out["token_type"])
	}
}

func TestConsentPageIsStationThemedAndSelfContained(t *testing.T) {
	_, ts := newGate(t)
	resp, err := http.Get(ts.URL + "/oauth/authorize?" + authParams().Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	page := sb.String()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"Станция", "claude.ai", `name="password"`, `name="code_challenge"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "fonts.googleapis.com") || strings.Contains(page, "<script") {
		t.Error("page must not load external fonts or run scripts")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("consent page must not be cached")
	}
}

func getConsent(t *testing.T, ts *httptest.Server, v url.Values) (int, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/oauth/authorize?" + v.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, readBody(t, resp)
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustContain(t *testing.T, page string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestConsentPageLoadsNothingFromOutside(t *testing.T) {
	_, ts := newGate(t)
	_, page := getConsent(t, ts, authParams())
	for _, bad := range []string{"<script", "<link", "<img", "<iframe", "@import", " src=", " href="} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	// SVG paint servers are fine as long as they point inside the document.
	for rest := page; ; {
		i := strings.Index(rest, "url(")
		if i < 0 {
			break
		}
		if rest = rest[i+4:]; !strings.HasPrefix(rest, "#") {
			t.Errorf("url() points outside the page: %.40q", rest)
		}
	}
}

func TestConsentFormCarriesTheWholeRequest(t *testing.T) {
	_, ts := newGate(t)
	_, page := getConsent(t, ts, authParams())
	for name := range authParams() {
		mustContain(t, page, `type="hidden" name="`+name+`"`)
	}
	mustContain(t, page,
		`method="post" action="/oauth/authorize"`,
		`name="username" value="station" autocomplete="username"`, // password managers key on it
		`value="st-1"`,
		`autocomplete="current-password"`,
		`class="is-ready"`,
	)
}

func TestConsentPageDrawsTheRealRing(t *testing.T) {
	_, ts := newGate(t)
	_, page := getConsent(t, ts, authParams())
	if n := strings.Count(page, `class="led"`); n != 88 {
		t.Errorf("ring has %d LEDs, the speaker has 88", n)
	}
}

func TestConsentPageStatesTokenLifetime(t *testing.T) {
	for days, want := range map[int]string{1: "1 день", 3: "3 дня", 5: "5 дней", 11: "11 дней", 12: "12 дней", 21: "21 день", 90: "90 дней"} {
		_, ts := newGate(t, func(c *oauth.Config) { c.TokenTTL = int64(days) * 86400 })
		_, page := getConsent(t, ts, authParams())
		mustContain(t, page, "<dd>"+want+"</dd>")
	}
}

func TestWrongPasswordPageKeepsTheFormAndSaysWhy(t *testing.T) {
	_, ts := newGate(t)
	_, resp := authorize(t, ts, "nope")
	page := readBody(t, resp)
	mustContain(t, page, `class="is-wrong"`, "Неверный пароль.",
		`aria-invalid="true"`, `name="password"`, `type="hidden" name="code_challenge"`)
}

func TestLockoutPageSaysToWait(t *testing.T) {
	_, ts := newGate(t)
	for i := 0; i < 5; i++ {
		authorize(t, ts, "nope")
	}
	_, resp := authorize(t, ts, "nope")
	page := readBody(t, resp)
	mustContain(t, page, `class="is-locked"`, "Подождите несколько минут.", `name="password"`)
}

func TestBrokenRequestPageHasNoForm(t *testing.T) {
	_, ts := newGate(t)
	v := authParams()
	v.Set("client_id", "evil")
	status, page := getConsent(t, ts, v)
	if status != http.StatusBadRequest {
		t.Errorf("status %d, want 400", status)
	}
	mustContain(t, page, `class="is-fatal"`, "Станция", "Неизвестный client_id.")
	for _, bad := range []string{"<form", `name="password"`} {
		if strings.Contains(page, bad) {
			t.Errorf("broken request page contains %q", bad)
		}
	}
}

func TestWrongPasswordGivesNoCode(t *testing.T) {
	_, ts := newGate(t)
	code, resp := authorize(t, ts, "nope")
	if code != "" || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code=%q status=%d, want no code and 401", code, resp.StatusCode)
	}
}

func TestPasswordGuessesAreLockedOut(t *testing.T) {
	_, ts := newGate(t)
	for i := 0; i < 5; i++ {
		authorize(t, ts, "nope")
	}
	code, resp := authorize(t, ts, pass)
	if code != "" || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("after 5 failures: code=%q status=%d, want locked out (429)", code, resp.StatusCode)
	}
}

func TestAuthorizeRejectsBadRequestsWithoutRedirect(t *testing.T) {
	_, ts := newGate(t)
	cases := map[string]func(url.Values){
		"unknown client":   func(v url.Values) { v.Set("client_id", "evil") },
		"foreign redirect": func(v url.Values) { v.Set("redirect_uri", "https://evil.example/cb") },
		"no pkce":          func(v url.Values) { v.Del("code_challenge") },
		"plain pkce":       func(v url.Values) { v.Set("code_challenge_method", "plain") },
		"token flow":       func(v url.Values) { v.Set("response_type", "token") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			v := authParams()
			mut(v)
			v.Set("password", pass)
			resp := post(t, ts, "/oauth/authorize", v)
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Errorf("status=%d location=%q, want 400 and no redirect", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

func TestCodeIsSingleUse(t *testing.T) {
	_, ts := newGate(t)
	code, _ := authorize(t, ts, pass)
	if s, _ := exchange(t, ts, tokenForm(code)); s != 200 {
		t.Fatalf("first exchange %d", s)
	}
	if s, out := exchange(t, ts, tokenForm(code)); s != 400 || out["error"] != "invalid_grant" {
		t.Errorf("second exchange: %d %v, want 400 invalid_grant", s, out)
	}
}

func TestTokenEndpointRejections(t *testing.T) {
	_, ts := newGate(t)
	cases := map[string]struct {
		mut    func(url.Values)
		status int
		errStr string
	}{
		"wrong client secret": {func(v url.Values) { v.Set("client_secret", "x") }, 401, "invalid_client"},
		"wrong client id":     {func(v url.Values) { v.Set("client_id", "x") }, 401, "invalid_client"},
		"wrong verifier":      {func(v url.Values) { v.Set("code_verifier", "wrong-verifier-wrong-verifier-wrong-verifier-0") }, 400, "invalid_grant"},
		"wrong redirect":      {func(v url.Values) { v.Set("redirect_uri", "https://claude.com/other") }, 400, "invalid_grant"},
		"wrong grant type":    {func(v url.Values) { v.Set("grant_type", "password") }, 400, "unsupported_grant_type"},
		"unknown code":        {func(v url.Values) { v.Set("code", "nope") }, 400, "invalid_grant"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			code, _ := authorize(t, ts, pass)
			v := tokenForm(code)
			c.mut(v)
			s, out := exchange(t, ts, v)
			if s != c.status || out["error"] != c.errStr {
				t.Errorf("got %d %v, want %d %s", s, out, c.status, c.errStr)
			}
		})
	}
}

func TestTokenEndpointAcceptsBasicAuthAndJSON(t *testing.T) {
	g, ts := newGate(t)
	code, _ := authorize(t, ts, pass)
	body, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code", "code": code, "redirect_uri": callback, "code_verifier": verifier,
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(clientID, clientSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	tok, _ := out["access_token"].(string)
	if resp.StatusCode != 200 || !g.TokenValid(tok) {
		t.Errorf("status %d, out %v", resp.StatusCode, out)
	}
}

func TestTokenValidity(t *testing.T) {
	g, _ := newGate(t)
	good := oauth.Sign(secret, clientID, 3600)
	if !g.TokenValid(good) {
		t.Fatal("fresh token must be valid")
	}
	body := strings.Split(good, ".")[0]
	for name, tok := range map[string]string{
		"expired":        oauth.Sign(secret, clientID, -10),
		"other secret":   oauth.Sign("another-secret-another-secret-0123456", clientID, 3600),
		"other client":   oauth.Sign(secret, "other-client", 3600),
		"tampered sig":   body + ".AAAA",
		"no signature":   body,
		"empty":          "",
		"static lookalk": "agent-token-aaaaaaaaaaaaaaaa",
	} {
		if g.TokenValid(tok) {
			t.Errorf("%s token accepted", name)
		}
	}
}

func TestTokenTTLComesFromConfig(t *testing.T) {
	_, ts := newGate(t, func(c *oauth.Config) { c.TokenTTL = 7 * 24 * 3600 })
	code, _ := authorize(t, ts, pass)
	_, out := exchange(t, ts, tokenForm(code))
	if out["expires_in"] != float64(7*24*3600) {
		t.Errorf("expires_in = %v", out["expires_in"])
	}
}

func TestMetadataUsesPublicURL(t *testing.T) {
	_, ts := newGate(t, func(c *oauth.Config) { c.PublicURL = "https://station.example.com/" })
	for path, key := range map[string]string{
		"/.well-known/oauth-authorization-server": "authorization_endpoint",
		"/.well-known/oauth-protected-resource":   "resource",
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("X-Forwarded-Host", "evil.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		got, _ := out[key].(string)
		if !strings.HasPrefix(got, "https://station.example.com/") || strings.Contains(got, "evil") || strings.Contains(got, "//oauth") {
			t.Errorf("%s: %s = %q", path, key, got)
		}
	}
}

func TestProtectedResourcePointsAtMCPAndIssuer(t *testing.T) {
	_, ts := newGate(t, func(c *oauth.Config) { c.PublicURL = "https://station.example.com" })
	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Resource != "https://station.example.com/mcp" || len(out.Servers) != 1 || out.Servers[0] != "https://station.example.com" {
		t.Errorf("got %+v", out)
	}
}

func TestChallengeNamesResourceMetadata(t *testing.T) {
	g, _ := newGate(t, func(c *oauth.Config) { c.PublicURL = "https://station.example.com" })
	got := g.Challenge(httptest.NewRequest(http.MethodGet, "/mcp", nil))
	want := `Bearer resource_metadata="https://station.example.com/.well-known/oauth-protected-resource/mcp"`
	if got != want {
		t.Errorf("Challenge = %q, want %q", got, want)
	}
}

func TestConfigValidation(t *testing.T) {
	ok := oauth.Config{Password: pass, TokenSecret: secret, ClientSecret: clientSecret}
	if _, err := oauth.NewServer(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, mut := range map[string]func(*oauth.Config){
		"no password":         func(c *oauth.Config) { c.Password = "" },
		"short password":      func(c *oauth.Config) { c.Password = "short" },
		"short token secret":  func(c *oauth.Config) { c.TokenSecret = "short" },
		"short client secret": func(c *oauth.Config) { c.ClientSecret = "short" },
		"http public url":     func(c *oauth.Config) { c.PublicURL = "http://station.example.com" },
	} {
		c := ok
		mut(&c)
		if _, err := oauth.NewServer(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
