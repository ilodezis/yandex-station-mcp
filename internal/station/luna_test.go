package station_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

func TestResolveLunaAddr(t *testing.T) {
	cases := []struct {
		lunaAddr    string
		stationAddr string
		want        string
	}{
		{"", "192.168.1.100:2222", "http://192.168.1.100:8099/api"},
		{"", "192.168.1.100", "http://192.168.1.100:8099/api"},
		{"", "", "http://127.0.0.1:8099/api"},
		{"http://10.0.0.5:8099", "", "http://10.0.0.5:8099/api"},
		{"http://10.0.0.5:8099/api", "", "http://10.0.0.5:8099/api"},
		{"10.0.0.5:8099", "", "http://10.0.0.5:8099/api"},
		{"https://speaker.local:8099/api/", "", "https://speaker.local:8099/api"},
	}

	for _, tc := range cases {
		got := station.ResolveLunaAddr(tc.lunaAddr, tc.stationAddr)
		if got != tc.want {
			t.Errorf("ResolveLunaAddr(%q, %q) = %q, want %q", tc.lunaAddr, tc.stationAddr, got, tc.want)
		}
	}
}

func TestNewLunaBackendValidation(t *testing.T) {
	cfg := station.DefaultConfig()
	if _, err := station.NewLunaBackend(station.LunaConfig{BaseURL: ""}, cfg); err == nil {
		t.Error("empty BaseURL must fail")
	}

	b, err := station.NewLunaBackend(station.LunaConfig{BaseURL: "http://127.0.0.1:8099"}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer b.Close()
}

type mockLunaServer struct {
	mu           sync.Mutex
	server       *httptest.Server
	volume       int
	deviceInfo   map[string]any
	sensors      []map[string]any
	media        map[string]any
	lastAction   string
	lastLEDFrame []string
	lastLEDOff   bool
	audioPlayed  bool
}

func newMockLunaServer() *mockLunaServer {
	m := &mockLunaServer{
		volume: 35,
		deviceInfo: map[string]any{
			"model":        "yandexmidi",
			"architecture": "aarch64",
			"features":     []string{"led.ring.rgb", "led.logo"},
			"led": map[string]any{
				"ring_count": 24,
				"has_logo":   true,
				"logo_count": 6,
			},
		},
		sensors: []map[string]any{
			{
				"id":    "ltr308",
				"kind":  "illuminance",
				"unit":  "lux",
				"value": 15.5,
			},
		},
		media: map[string]any{
			"provider": "radio",
			"state":    "playing",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/device", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.deviceInfo)
	})

	mux.HandleFunc("/api/volume/get", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"percent": m.volume})
	})

	mux.HandleFunc("/api/volume/set", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var req struct {
			Percent int `json:"percent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.volume = req.Percent
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"percent": m.volume})
	})

	mux.HandleFunc("/api/sensors", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.sensors)
	})

	mux.HandleFunc("/api/media/command", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var req struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.lastAction = req.Action
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"cmd_1"}`))
	})

	mux.HandleFunc("/api/media", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.media)
	})

	mux.HandleFunc("/api/audio/test", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.audioPlayed = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"played":true,"duration_ms":4000}`))
	})

	mux.HandleFunc("/api/led/ring", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var req struct {
			Colors []string `json:"colors"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.lastLEDFrame = req.Colors
		m.lastLEDOff = false
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/led/off/ring", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.lastLEDOff = true
		m.lastLEDFrame = nil
		w.WriteHeader(http.StatusOK)
	})

	m.server = httptest.NewServer(mux)
	return m
}

func (m *mockLunaServer) Close() {
	m.server.Close()
}

func TestLunaStatus(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	cfg := station.DefaultConfig()
	cfg.Now = func() time.Time { return time.Unix(1_000_000, 0) }
	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, cfg)

	status := st.Status(context.Background())
	if !status.Reachable {
		t.Fatalf("expected reachable, got error: %s", status.Error)
	}
	if status.VolumePercent != 35 {
		t.Errorf("volume = %d, want 35", status.VolumePercent)
	}
	if status.AmbientLux != 15.5 {
		t.Errorf("ambient lux = %f, want 15.5", status.AmbientLux)
	}
	if status.IsDark {
		t.Errorf("is_dark = true, want false for 15.5 lux")
	}
}

func TestLunaStatusUnreachable(t *testing.T) {
	mock := newMockLunaServer()
	url := mock.server.URL
	mock.Close() // immediately close to make it unreachable

	cfg := station.DefaultConfig()
	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: url,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, cfg)

	status := st.Status(context.Background())
	if status.Reachable {
		t.Error("expected unreachable status when server is down")
	}
	if status.Error == "" {
		t.Error("expected error message in unreachable status")
	}
}

func TestLunaSetVolume(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	cfg := station.DefaultConfig()
	cfg.MaxVolume = 70
	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, cfg)

	// Set to 50%
	applied, err := st.SetVolume(context.Background(), 50)
	if err != nil || applied != 50 {
		t.Fatalf("SetVolume(50) = %d, %v; want 50", applied, err)
	}
	mock.mu.Lock()
	if mock.volume != 50 {
		t.Errorf("mock volume = %d, want 50", mock.volume)
	}
	mock.mu.Unlock()

	// Set to 100%, should be capped at MaxVolume (70)
	applied, err = st.SetVolume(context.Background(), 100)
	if err != nil || applied != 70 {
		t.Fatalf("SetVolume(100) = %d, %v; want 70", applied, err)
	}
	mock.mu.Lock()
	if mock.volume != 70 {
		t.Errorf("mock volume = %d, want 70", mock.volume)
	}
	mock.mu.Unlock()

	// Out of bounds
	if _, err := st.SetVolume(context.Background(), -5); err == nil {
		t.Error("-5 must fail")
	}
	if _, err := st.SetVolume(context.Background(), 105); err == nil {
		t.Error("105 must fail")
	}
}

func TestLunaPlayerControl(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, station.DefaultConfig())

	actions := map[string]string{
		"play":     "play",
		"resume":   "play",
		"continue": "play",
		"pause":    "pause",
		"stop":     "pause",
		"next":     "next",
		"prev":     "prev",
		"previous": "prev",
		"toggle":   "toggle",
	}

	for in, want := range actions {
		if err := st.PlayerControl(context.Background(), in); err != nil {
			t.Fatalf("PlayerControl(%q): %v", in, err)
		}
		mock.mu.Lock()
		got := mock.lastAction
		mock.mu.Unlock()
		if got != want {
			t.Errorf("PlayerControl(%q) sent %q, want %q", in, got, want)
		}
	}

	if err := st.PlayerControl(context.Background(), "invalid_act"); err == nil {
		t.Error("invalid action must return an error")
	}
}

func TestLunaPlaySound(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, station.DefaultConfig())

	if err := st.PlaySound(context.Background(), "unknown_sound"); err == nil {
		t.Error("unknown sound must fail")
	}

	if err := st.PlaySound(context.Background(), "timer"); err != nil {
		t.Fatalf("PlaySound(timer): %v", err)
	}

	mock.mu.Lock()
	played := mock.audioPlayed
	mock.mu.Unlock()
	if !played {
		t.Error("audio test endpoint was not called")
	}
}

func TestLunaSay(t *testing.T) {
	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: "http://127.0.0.1:8099",
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, station.DefaultConfig())

	// Empty text fails CleanText
	if err := st.Say(context.Background(), ""); err == nil {
		t.Error("empty text must fail CleanText")
	}

	// Valid text reports unsupported on Luna HTTP backend
	err = st.Say(context.Background(), "Hello world")
	if err == nil || !strings.Contains(err.Error(), "speech synthesis is not supported") {
		t.Fatalf("Say must return unsupported error, got %v", err)
	}
}

func TestLunaSetDND(t *testing.T) {
	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: "http://127.0.0.1:8099",
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, station.DefaultConfig())

	err = st.SetDND(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "dnd is not supported") {
		t.Fatalf("SetDND must return unsupported error, got %v", err)
	}
}

func TestLunaLED(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL:   mock.server.URL,
		RingCount: 24,
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, station.DefaultConfig())

	// Test lighting ring full
	if err := st.LED(context.Background(), station.LEDSpec{Color: "red", Brightness: 50}); err != nil {
		t.Fatalf("LED full: %v", err)
	}
	mock.mu.Lock()
	if len(mock.lastLEDFrame) != 1 || mock.lastLEDFrame[0] != "#7f0000" { // 255*50/100 = 127 = 0x7f
		t.Errorf("LED frame = %v, want [#7f0000]", mock.lastLEDFrame)
	}
	mock.mu.Unlock()

	// Test fraction
	if err := st.LED(context.Background(), station.LEDSpec{Color: "blue", Brightness: 50, Fraction: 0.5}); err != nil {
		t.Fatalf("LED fraction: %v", err)
	}
	mock.mu.Lock()
	if len(mock.lastLEDFrame) != 24 {
		t.Fatalf("LED fraction frame len = %d, want 24", len(mock.lastLEDFrame))
	}
	// 0.5 of 24 is 12 lit, 12 dark
	for i := 0; i < 12; i++ {
		if mock.lastLEDFrame[i] != "#00007f" {
			t.Errorf("pixel %d = %s, want #00007f", i, mock.lastLEDFrame[i])
		}
	}
	for i := 12; i < 24; i++ {
		if mock.lastLEDFrame[i] != "#000000" {
			t.Errorf("pixel %d = %s, want #000000", i, mock.lastLEDFrame[i])
		}
	}
	mock.mu.Unlock()

	// Test turn off
	if err := st.LED(context.Background(), station.LEDSpec{Color: "off"}); err != nil {
		t.Fatalf("LED off: %v", err)
	}
	mock.mu.Lock()
	if !mock.lastLEDOff {
		t.Error("expected LED off to be called")
	}
	mock.mu.Unlock()

	// Test hold timer clears ring
	if err := st.LED(context.Background(), station.LEDSpec{Color: "green", Brightness: 20, Hold: 50 * time.Millisecond}); err != nil {
		t.Fatalf("LED hold: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	mock.mu.Lock()
	if !mock.lastLEDOff {
		t.Error("expected hold timer to turn off LED")
	}
	mock.mu.Unlock()
}

func TestLunaAmbientLight(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, station.DefaultConfig())

	// Test normal reading (15.5 lux -> not dark)
	lux, dark, err := st.AmbientLight(context.Background())
	if err != nil {
		t.Fatalf("AmbientLight: %v", err)
	}
	if lux != 15.5 || dark {
		t.Errorf("lux = %f, dark = %v, want 15.5, false", lux, dark)
	}

	// Test dark reading (0.5 lux -> dark)
	mock.mu.Lock()
	val := 0.5
	mock.sensors[0]["value"] = val
	mock.mu.Unlock()

	lux, dark, err = st.AmbientLight(context.Background())
	if err != nil {
		t.Fatalf("AmbientLight: %v", err)
	}
	if lux != 0.5 || !dark {
		t.Errorf("lux = %f, dark = %v, want 0.5, true", lux, dark)
	}

	// Test missing sensor
	mock.mu.Lock()
	mock.sensors = nil
	mock.mu.Unlock()

	if _, _, err := st.AmbientLight(context.Background()); err == nil {
		t.Error("missing sensor must return error")
	}
}

func TestLunaTimer(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	now := time.Unix(1_000_000, 0)
	cfg := station.DefaultConfig()
	cfg.Now = func() time.Time { return now }

	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	st := station.NewWithBackend(backend, cfg)

	info, err := st.Timer(context.Background(), 10*time.Second, "Tea is ready")
	if err != nil {
		t.Fatalf("Timer: %v", err)
	}
	if info.Seconds != 10 {
		t.Errorf("info.Seconds = %d, want 10", info.Seconds)
	}

	status := st.Status(context.Background())
	if len(status.Timers) != 1 {
		t.Fatalf("status.Timers len = %d, want 1", len(status.Timers))
	}
	if status.Timers[0].Text != "Tea is ready" {
		t.Errorf("timer text = %q, want %q", status.Timers[0].Text, "Tea is ready")
	}
}

func TestLunaMedia(t *testing.T) {
	mock := newMockLunaServer()
	defer mock.Close()

	backend, err := station.NewLunaBackend(station.LunaConfig{
		BaseURL: mock.server.URL,
	}, station.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	med, err := backend.Media(context.Background())
	if err != nil {
		t.Fatalf("Media: %v", err)
	}
	if med["provider"] != "radio" || med["state"] != "playing" {
		t.Errorf("unexpected media response: %v", med)
	}
}
