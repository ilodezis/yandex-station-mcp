package mcpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ilodezis/yandex-station-mcp/internal/mcpserver"
	"github.com/ilodezis/yandex-station-mcp/internal/oauth"
	"github.com/ilodezis/yandex-station-mcp/internal/service"
	"github.com/ilodezis/yandex-station-mcp/internal/station"
	"github.com/ilodezis/yandex-station-mcp/internal/stationtest"
)

const oauthSecret = "token-secret-token-secret-token-secret-0123"

// newOAuthRig is the production wiring: static bearer tokens plus the OAuth gate beside them.
func newOAuthRig(t *testing.T) (*rig, *oauth.Server) {
	t.Helper()
	gate, err := oauth.NewServer(oauth.Config{
		Password: "owner-password-123", TokenSecret: oauthSecret, ClientSecret: "client-secret-client-secret-01",
		PublicURL: "https://station.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &stationtest.Fake{Replies: map[string]string{
		"amixer sget": "Mono: Playback 128 [50%]\n--\n100.0 1.0\n--\n5\n--\n 00 00 00 00\n--\n",
	}}
	svc := service.New(station.New(fake, station.DefaultConfig()), fixedQuiet(false), service.Options{LEDEnabled: true})
	opt := mcpserver.Options{LEDEnabled: true, Version: "test"}
	servers := map[mcpserver.Profile]*mcp.Server{
		mcpserver.Agent: mcpserver.New(mcpserver.Agent, svc, opt),
		mcpserver.Brain: mcpserver.New(mcpserver.Brain, svc, opt),
	}
	auth := mcpserver.NewAuth(map[mcpserver.Profile]string{mcpserver.Agent: agentTok, mcpserver.Brain: brainTok})
	auth.EnableOAuth(gate.TokenValid, gate.Challenge)
	ts := httptest.NewServer(mcpserver.Handler(auth, servers))
	t.Cleanup(ts.Close)
	return &rig{ts: ts, fake: fake}, gate
}

func TestOAuthTokenGetsTheAgentToolSet(t *testing.T) {
	r, _ := newOAuthRig(t)
	tok := oauth.Sign(oauthSecret, oauth.DefaultClientID, 3600)
	got := strings.Join(toolNames(t, r.connect(t, tok)), ",")
	if want := "ambient_light,announce,dnd,led,play_sound,player_control,set_volume,status,timer"; got != want {
		t.Errorf("oauth tools = %s, want %s", got, want)
	}
}

func TestStaticTokensKeepTheirProfilesBesideOAuth(t *testing.T) {
	r, _ := newOAuthRig(t)
	if got := strings.Join(toolNames(t, r.connect(t, agentTok)), ","); !strings.Contains(got, "announce") {
		t.Errorf("agent token lost announce: %s", got)
	}
	if got := strings.Join(toolNames(t, r.connect(t, brainTok)), ","); strings.Contains(got, "announce") {
		t.Errorf("brain token gained announce: %s", got)
	}
}

func TestBadOAuthTokensAreRefused(t *testing.T) {
	r, _ := newOAuthRig(t)
	for name, tok := range map[string]string{
		"expired":      oauth.Sign(oauthSecret, oauth.DefaultClientID, -5),
		"wrong secret": oauth.Sign("another-secret-another-secret-0123456", oauth.DefaultClientID, 3600),
		"garbage":      "x.y",
	} {
		req, _ := http.NewRequest(http.MethodPost, r.ts.URL+"/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
}

func TestUnauthorizedPointsClientsAtOAuthOnlyWhenEnabled(t *testing.T) {
	r, _ := newOAuthRig(t)
	resp, err := http.Post(r.ts.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := `Bearer resource_metadata="https://station.example.com/.well-known/oauth-protected-resource/mcp"`
	if got := resp.Header.Get("WWW-Authenticate"); resp.StatusCode != 401 || got != want {
		t.Errorf("oauth on: %d %q, want 401 %q", resp.StatusCode, got, want)
	}

	plain := newRig(t, false, true)
	resp, err = http.Post(plain.ts.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("oauth off: WWW-Authenticate = %q, want plain Bearer", got)
	}
}
