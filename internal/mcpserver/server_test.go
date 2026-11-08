package mcpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ilodezis/yandex-station-mcp/internal/mcpserver"
	"github.com/ilodezis/yandex-station-mcp/internal/service"
	"github.com/ilodezis/yandex-station-mcp/internal/station"
	"github.com/ilodezis/yandex-station-mcp/internal/stationtest"
)

const (
	agentTok = "agent-token-aaaaaaaaaaaaaaaa"
	brainTok = "brain-token-bbbbbbbbbbbbbbbb"
)

type fixedQuiet bool

func (q fixedQuiet) IsQuiet(time.Time) bool { return bool(q) }

type rig struct {
	ts   *httptest.Server
	fake *stationtest.Fake
}

func newRig(t *testing.T, quiet, led bool) *rig {
	t.Helper()
	fake := &stationtest.Fake{Replies: map[string]string{
		"amixer sget":        "Mono: Playback 128 [50%]\n--\n100.0 1.0\n--\n5\n--\n 00 00 00 00\n--\n\n--\n0.266666\n",
		"in_intensity_input": "0.266666\n",
	}}
	svc := service.New(station.New(fake, station.DefaultConfig()), fixedQuiet(quiet),
		service.Options{LEDEnabled: led})
	opt := mcpserver.Options{LEDEnabled: led, Version: "test"}
	servers := map[mcpserver.Profile]*mcp.Server{
		mcpserver.Agent: mcpserver.New(mcpserver.Agent, svc, opt),
		mcpserver.Brain: mcpserver.New(mcpserver.Brain, svc, opt),
	}
	auth := mcpserver.NewAuth(map[mcpserver.Profile]string{mcpserver.Agent: agentTok, mcpserver.Brain: brainTok})
	ts := httptest.NewServer(mcpserver.Handler(auth, servers))
	t.Cleanup(ts.Close)
	return &rig{ts: ts, fake: fake}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (r *rig) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             r.ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func toolNames(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

func TestAgentGetsEveryTool(t *testing.T) {
	got := strings.Join(toolNames(t, newRig(t, false, true).connect(t, agentTok)), ",")
	if want := "ambient_light,announce,dnd,led,play_sound,player_control,set_volume,status,timer"; got != want {
		t.Errorf("agent tools = %s, want %s", got, want)
	}
}

func TestBrainGetsNoAnnounce(t *testing.T) {
	got := strings.Join(toolNames(t, newRig(t, false, true).connect(t, brainTok)), ",")
	if want := "ambient_light,dnd,led,play_sound,player_control,set_volume,status,timer"; got != want {
		t.Errorf("brain tools = %s, want %s", got, want)
	}
}

func TestLEDToolHiddenWhenDisabled(t *testing.T) {
	got := strings.Join(toolNames(t, newRig(t, false, false).connect(t, agentTok)), ",")
	if strings.Contains(got, "led") {
		t.Errorf("led must be absent, got %s", got)
	}
}

func TestToolSetsMatchDeclaration(t *testing.T) {
	if n := len(mcpserver.ToolNames(mcpserver.Agent)); n != 9 {
		t.Errorf("agent declares %d tools", n)
	}
	for _, n := range mcpserver.ToolNames(mcpserver.Brain) {
		if n == "announce" {
			t.Error("brain must never get announce")
		}
	}
}

func TestAuthRejects(t *testing.T) {
	r := newRig(t, false, true)
	for name, hdr := range map[string]string{"none": "", "wrong": "Bearer nope-nope-nope-nope-nope", "scheme": "Basic " + agentTok} {
		req, _ := http.NewRequest(http.MethodPost, r.ts.URL+"/mcp", strings.NewReader("{}"))
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
	resp, err := http.Get(r.ts.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz must be open: %v %v", resp, err)
	}
	resp.Body.Close()
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func TestAnnounceByDayAndNight(t *testing.T) {
	day := newRig(t, false, true)
	res := call(t, day.connect(t, agentTok), "announce", map[string]any{"text": "сессия ждёт", "urgency": "important"})
	if res.IsError {
		t.Fatalf("error: %+v", res.Content)
	}
	if sc, _ := res.StructuredContent.(map[string]any); sc["delivered"] != "voice" {
		t.Errorf("structured = %v", res.StructuredContent)
	}
	if c := day.fake.Commands(); len(c) != 1 || !strings.HasPrefix(c[0], "say ") {
		t.Errorf("commands = %v", c)
	}

	night := newRig(t, true, true)
	res = call(t, night.connect(t, agentTok), "announce", map[string]any{"text": "сессия ждёт", "urgency": "important"})
	if sc, _ := res.StructuredContent.(map[string]any); sc["delivered"] != "ring" {
		t.Errorf("night structured = %v", res.StructuredContent)
	}
	for _, c := range night.fake.Commands() {
		if strings.HasPrefix(c, "say ") {
			t.Errorf("voice at night: %s", c)
		}
	}
}

func TestBadArgumentsComeBackAsToolErrors(t *testing.T) {
	r := newRig(t, false, true)
	s := r.connect(t, agentTok)
	if res := call(t, s, "announce", map[string]any{"text": "x", "urgency": "panic"}); !res.IsError {
		t.Error("bad urgency must be a tool error")
	}
	if res := call(t, s, "set_volume", map[string]any{"percent": 500}); !res.IsError {
		t.Error("bad volume must be a tool error")
	}
	if res := call(t, s, "led", map[string]any{"color": "pink"}); !res.IsError {
		t.Error("bad colour must be a tool error")
	}
	if len(r.fake.Commands()) != 0 {
		t.Errorf("nothing may reach the speaker: %v", r.fake.Commands())
	}
}

func TestStatusTool(t *testing.T) {
	r := newRig(t, true, true)
	res := call(t, r.connect(t, brainTok), "status", nil)
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if sc["reachable"] != true || sc["volume_percent"] != float64(50) || sc["quiet_mode"] != true || sc["ring_dark"] != true {
		t.Errorf("status = %v", sc)
	}
	if sc["ambient_lux"] != 0.266666 || sc["is_dark"] != true {
		t.Errorf("status ambient light = %v, %v; want 0.266666, true", sc["ambient_lux"], sc["is_dark"])
	}
}

func TestAmbientLightTool(t *testing.T) {
	r := newRig(t, false, true)
	res := call(t, r.connect(t, agentTok), "ambient_light", nil)
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if sc["lux"] != 0.266666 || sc["dark"] != true {
		t.Errorf("ambient_light = %v, want lux=0.266666 dark=true", sc)
	}
}

func TestPlayerControlTool(t *testing.T) {
	r := newRig(t, false, true)
	s := r.connect(t, agentTok)

	res := call(t, s, "player_control", map[string]any{"action": "pause"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if c := r.fake.Commands(); len(c) != 1 || c[0] != "station-ctl player pause" {
		t.Errorf("commands = %v", c)
	}

	r.fake.Clear()
	res = call(t, s, "player_control", map[string]any{"action": "next"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if c := r.fake.Commands(); len(c) != 1 || c[0] != "station-ctl player next" {
		t.Errorf("commands = %v", c)
	}

	if res := call(t, s, "player_control", map[string]any{"action": "unknown"}); !res.IsError {
		t.Error("unknown action must return tool error")
	}
}

func TestDNDTool(t *testing.T) {
	r := newRig(t, false, true)
	s := r.connect(t, agentTok)

	res := call(t, s, "dnd", map[string]any{"enabled": true})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if c := r.fake.Commands(); len(c) != 1 || c[0] != "station-ctl dnd enable" {
		t.Errorf("commands = %v", c)
	}

	r.fake.Clear()
	res = call(t, s, "dnd", map[string]any{"enabled": false})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if c := r.fake.Commands(); len(c) != 1 || c[0] != "station-ctl dnd disable" {
		t.Errorf("commands = %v", c)
	}
}

func TestAnnounceKeepsCyrillicBytesFromMCPToSpeaker(t *testing.T) {
	r := newRig(t, false, true)
	const phrase = "Сессия ждёт ответа: проверь, пожалуйста — ёжик, щука, Ъ"
	res := call(t, r.connect(t, agentTok), "announce", map[string]any{"text": phrase})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if c := r.fake.Commands(); len(c) != 1 || c[0] != "say '"+phrase+"'" {
		t.Errorf("commands = %q", c)
	}
}
