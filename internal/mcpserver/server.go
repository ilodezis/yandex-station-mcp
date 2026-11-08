// Package mcpserver exposes the speaker as MCP tools, with a different tool set per client profile.
package mcpserver

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ilodezis/yandex-station-mcp/internal/service"
	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

// Profile is a client class. The token decides which one a request gets.
type Profile string

const (
	// Agent is Claude Code, Claude Web and Antigravity: every tool.
	Agent Profile = "agent"
	// Brain is the voice brain behind the speaker (phase 4): no way to make the speaker
	// announce things on its own, only the controls and the status.
	Brain Profile = "brain"
)

// toolSets is the single place that decides who may call what.
var toolSets = map[Profile][]string{
	Agent: {"announce", "set_volume", "status", "play_sound", "timer", "led", "ambient_light", "player_control", "dnd"},
	Brain: {"set_volume", "led", "timer", "status", "play_sound", "ambient_light", "player_control", "dnd"},
}

// ToolNames returns the tool names a profile gets.
func ToolNames(p Profile) []string { return append([]string(nil), toolSets[p]...) }

func allowed(p Profile, name string) bool {
	for _, n := range toolSets[p] {
		if n == name {
			return true
		}
	}
	return false
}

// Options configures the MCP layer.
type Options struct {
	LEDEnabled bool
	Version    string
}

// AnnounceIn is the input of the announce tool.
type AnnounceIn struct {
	Text    string `json:"text" jsonschema:"what to say, up to 500 characters, plain sentences without markup"`
	Urgency string `json:"urgency,omitempty" jsonschema:"info (default), important or critical. The speaker decides by itself whether this is voice, a coloured ring or silence, depending on the time of day"`
}

// AnnounceOut is the output of the announce tool.
type AnnounceOut struct {
	Delivered string `json:"delivered" jsonschema:"voice, voice+ring, ring or silent: what really happened"`
	Reason    string `json:"reason"`
}

// VolumeIn is the input of set_volume.
type VolumeIn struct {
	Percent int `json:"percent" jsonschema:"volume 0-100; the service caps it at its configured ceiling"`
}

// VolumeOut is the output of set_volume.
type VolumeOut struct {
	Applied int `json:"applied" jsonschema:"percent actually set, may be lower than asked because of the ceiling"`
}

// SoundIn is the input of play_sound.
type SoundIn struct {
	Name string `json:"name" jsonschema:"one of: timer, success, notice, error"`
}

// OKOut is a plain confirmation.
type OKOut struct {
	OK bool `json:"ok"`
}

// TimerIn is the input of timer.
type TimerIn struct {
	Seconds int    `json:"seconds" jsonschema:"delay in seconds, 1 to 86400"`
	Text    string `json:"text" jsonschema:"what the speaker says when the time is up"`
}

// TimerOut is the output of timer.
type TimerOut struct {
	ID      string `json:"id"`
	Seconds int    `json:"seconds"`
}

// LEDIn is the input of led.
type LEDIn struct {
	Color       string  `json:"color" jsonschema:"red, amber, yellow, green, cyan, blue, purple, white, #rrggbb, or off to darken the ring"`
	Brightness  int     `json:"brightness,omitempty" jsonschema:"percent 1-100, default 40; the service caps the absolute level"`
	Fraction    float64 `json:"fraction,omitempty" jsonschema:"share of the ring to light from the start, 0-1, for a level bar; default the whole ring"`
	HoldSeconds int     `json:"hold_seconds,omitempty" jsonschema:"darken the ring after this many seconds (max 3600); 0 keeps it until something else draws on it. Alice's own animations override and clear it"`
}

// StatusOut is the output of status.
type StatusOut struct {
	Reachable     bool          `json:"reachable"`
	Error         string        `json:"error,omitempty"`
	VolumePercent int           `json:"volume_percent"`
	UptimeSeconds int           `json:"uptime_seconds"`
	RingDark      bool          `json:"ring_dark" jsonschema:"true when pixel 0 of the ring is black"`
	QuietMode     bool          `json:"quiet_mode" jsonschema:"true when the speaker will not talk now"`
	Timers        []StatusTimer `json:"timers"`
	AmbientLux    float64       `json:"ambient_lux" jsonschema:"ambient illuminance in lux from LTR-308 sensor"`
	IsDark        bool          `json:"is_dark" jsonschema:"true when ambient illuminance < 2.0 lux"`
}

// AmbientLightOut is the output of ambient_light.
type AmbientLightOut struct {
	Lux  float64 `json:"lux" jsonschema:"ambient illuminance in lux from LTR-308 sensor"`
	Dark bool    `json:"dark" jsonschema:"true when ambient illuminance is below 2.0 lux"`
}

// PlayerControlIn is the input of player_control.
type PlayerControlIn struct {
	Action string `json:"action" jsonschema:"playback command: pause, play, next, prev, toggle"`
}

// DNDIn is the input of dnd.
type DNDIn struct {
	Enabled bool `json:"enabled" jsonschema:"true to enable Do Not Disturb mode, false to disable"`
}

// StatusTimer is a pending timer.
type StatusTimer struct {
	ID       string `json:"id"`
	FiresInS int    `json:"fires_in_s"`
	Text     string `json:"text"`
}

type empty struct{}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// New builds the MCP server for one profile.
func New(p Profile, svc *service.Service, opt Options) *mcp.Server {
	ver := opt.Version
	if ver == "" {
		ver = "dev"
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "station", Version: ver}, &mcp.ServerOptions{
		Instructions: "Controls a Yandex Station 2 smart speaker over SSH. The speaker is in a living space, so use it sparingly: " +
			"announce only what needs human attention (a task waiting or failed, an alert, an explicit reminder), never digests or routine progress chatter. " +
			"Nights are light only; announce decides that itself.",
	})
	st := svc.Station()
	t := true
	f := false

	if allowed(p, "announce") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "announce",
			Description: "Tell the user something through the speaker. Chooses voice, a coloured ring or silence by itself from urgency and the quiet mode; the result says which one happened. Voice is rationed, so do not call it for routine progress.",
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in AnnounceIn) (*mcp.CallToolResult, AnnounceOut, error) {
			u, err := service.ParseUrgency(in.Urgency)
			if err != nil {
				return nil, AnnounceOut{}, err
			}
			d, err := svc.Announce(ctx, in.Text, u)
			if err != nil {
				return nil, AnnounceOut{}, err
			}
			return nil, AnnounceOut{Delivered: d.Mode, Reason: d.Reason}, nil
		})
	}
	if allowed(p, "set_volume") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "set_volume",
			Description: "Set the speaker volume in percent (0-100). It is the speaker-wide Master volume, also used by Alice and AirPlay.",
			Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in VolumeIn) (*mcp.CallToolResult, VolumeOut, error) {
			n, err := st.SetVolume(ctx, in.Percent)
			return nil, VolumeOut{Applied: n}, err
		})
	}
	if allowed(p, "play_sound") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "play_sound",
			Description: "Play a short beep signal (timer, success, notice, error). No speech.",
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in SoundIn) (*mcp.CallToolResult, OKOut, error) {
			err := st.PlaySound(ctx, in.Name)
			return nil, OKOut{OK: err == nil}, err
		})
	}
	if allowed(p, "timer") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "timer",
			Description: "Set a timer on the speaker: after the delay it plays the timer sound and says the text. Always audible (an explicit reminder requested by the user), whatever the quiet mode. Survives a restart of this service, not a reboot of the speaker.",
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in TimerIn) (*mcp.CallToolResult, TimerOut, error) {
			info, err := st.Timer(ctx, time.Duration(in.Seconds)*time.Second, in.Text)
			return nil, TimerOut{ID: info.ID, Seconds: info.Seconds}, err
		})
	}
	if allowed(p, "led") && opt.LEDEnabled {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "led",
			Description: "Light the LED ring on top of the speaker with a colour. Best effort: Alice's own animations (listening, speaking) draw over it and clear it, so it lasts until she next does something.",
			Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in LEDIn) (*mcp.CallToolResult, OKOut, error) {
			err := st.LED(ctx, station.LEDSpec{
				Color: in.Color, Brightness: in.Brightness, Fraction: in.Fraction,
				Hold: time.Duration(in.HoldSeconds) * time.Second,
			})
			return nil, OKOut{OK: err == nil}, err
		})
	}
	if allowed(p, "status") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "status",
			Description: "Read the speaker state: reachability, volume, uptime, whether the ring is dark, pending timers and whether quiet mode is on.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &t},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, StatusOut, error) {
			s := st.Status(ctx)
			out := StatusOut{
				Reachable: s.Reachable, Error: s.Error, VolumePercent: s.VolumePercent,
				UptimeSeconds: s.UptimeSeconds, QuietMode: svc.Quiet(), Timers: []StatusTimer{},
				RingDark:   strings.Trim(s.LEDFirstPixel, "0") == "",
				AmbientLux: s.AmbientLux,
				IsDark:     s.IsDark,
			}
			for _, tm := range s.Timers {
				out.Timers = append(out.Timers, StatusTimer{ID: tm.ID, FiresInS: tm.FiresInS, Text: tm.Text})
			}
			return nil, out, nil
		})
	}
	if allowed(p, "ambient_light") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "ambient_light",
			Description: "Read the ambient light level from the speaker's LTR-308 sensor (lux and dark boolean).",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &t},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, AmbientLightOut, error) {
			lux, dark, err := st.AmbientLight(ctx)
			if err != nil {
				return nil, AmbientLightOut{}, err
			}
			return nil, AmbientLightOut{Lux: lux, Dark: dark}, nil
		})
	}
	if allowed(p, "player_control") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "player_control",
			Description: "Control music/audio playback on the speaker (pause, play, next, prev, toggle).",
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in PlayerControlIn) (*mcp.CallToolResult, OKOut, error) {
			err := st.PlayerControl(ctx, in.Action)
			return nil, OKOut{OK: err == nil}, err
		})
	}
	if allowed(p, "dnd") {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "dnd",
			Description: "Enable or disable Do Not Disturb (DND) mode on the speaker.",
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: &f, DestructiveHint: &f},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in DNDIn) (*mcp.CallToolResult, OKOut, error) {
			err := st.SetDND(ctx, in.Enabled)
			return nil, OKOut{OK: err == nil}, err
		})
	}
	return srv
}

// Auth maps bearer tokens to profiles.
type Auth struct {
	tokens map[Profile]string

	// OAuth, optional: tokens the gate issued count as the Agent profile, nothing else.
	oauthValid     func(token string) bool
	oauthChallenge func(*http.Request) string
}

// EnableOAuth accepts tokens that valid approves as Agent, beside the static ones. A client that
// went through OAuth is a person with full access; Brain is only ever reachable by its static
// token, so no OAuth path can hand out a tool set without announce (or with more than agent).
// challenge builds the WWW-Authenticate value that tells clients where OAuth starts.
func (a *Auth) EnableOAuth(valid func(token string) bool, challenge func(*http.Request) string) {
	a.oauthValid, a.oauthChallenge = valid, challenge
}

func (a *Auth) challenge(r *http.Request) string {
	if a.oauthChallenge != nil {
		return a.oauthChallenge(r)
	}
	return "Bearer"
}

// NewAuth takes one token per profile; an empty token disables that profile.
func NewAuth(tokens map[Profile]string) *Auth {
	a := &Auth{tokens: map[Profile]string{}}
	for p, t := range tokens {
		if t != "" {
			a.tokens[p] = t
		}
	}
	return a
}

// Profile resolves the request's bearer token, comparing every candidate in constant time.
func (a *Auth) Profile(r *http.Request) (Profile, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	got := []byte(strings.TrimSpace(h[7:]))
	var found Profile
	ok := false
	for p, t := range a.tokens {
		if subtle.ConstantTimeCompare(got, []byte(t)) == 1 {
			found, ok = p, true
		}
	}
	if !ok && a.oauthValid != nil && a.oauthValid(string(got)) {
		return Agent, true
	}
	return found, ok
}

type ctxKey struct{}

// Handler serves /mcp with per-profile servers and /healthz without auth.
func Handler(a *Auth, servers map[Profile]*mcp.Server) http.Handler {
	mcpH := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, _ := r.Context().Value(ctxKey{}).(Profile)
		return servers[p]
		// Stateless: every request is authenticated and bound to its own profile, so a session id
		// can never carry one client's tool set over to another token.
	}, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true, Stateless: true})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Profile(r)
		if !ok || servers[p] == nil {
			w.Header().Set("WWW-Authenticate", a.challenge(r))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpH.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
	mux.Handle("/mcp", gated)
	mux.Handle("/mcp/", gated)
	return mux
}
