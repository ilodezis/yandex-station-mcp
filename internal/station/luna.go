package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LunaConfig tunes the Luna HTTP API client.
type LunaConfig struct {
	BaseURL    string       // e.g. "http://192.168.1.100:8099/api"
	HTTPClient *http.Client // optional custom HTTP client; default has 10s timeout
	RingCount  int          // LED ring count (default 24)
}

// LunaBackend drives a speaker running nyakokitsu/luna over HTTP.
type LunaBackend struct {
	baseURL    string
	client     *http.Client
	stationCfg Config

	mu                sync.Mutex
	ringCount         int
	currentFirstPixel string
	holdTimer         *time.Timer
	holdID            int64

	timersMu sync.Mutex
	timers   map[string]*lunaPendingTimer
}

type lunaPendingTimer struct {
	id      string
	firesAt time.Time
	text    string
	timer   *time.Timer
}

// NewLunaBackend creates a new backend talking to Luna over HTTP.
func NewLunaBackend(cfg LunaConfig, stationCfg Config) (*LunaBackend, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("luna backend: BaseURL is required")
	}
	baseURL := NormalizeLunaURL(cfg.BaseURL)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ringCount := cfg.RingCount
	if ringCount <= 0 {
		ringCount = 24
	}
	stationCfg = normalizeConfig(stationCfg)

	return &LunaBackend{
		baseURL:           baseURL,
		client:            client,
		stationCfg:        stationCfg,
		ringCount:         ringCount,
		currentFirstPixel: "00000000",
		timers:            make(map[string]*lunaPendingTimer),
	}, nil
}

// NormalizeLunaURL ensures the URL starts with http:// or https://, trims trailing slashes,
// and appends /api if not present.
func NormalizeLunaURL(raw string) string {
	u := strings.TrimSpace(raw)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "http://" + u
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/api") {
		u += "/api"
	}
	return u
}

// ResolveLunaAddr determines the Luna API URL from LUNA_ADDR or falls back to STATION_ADDR with port 8099.
func ResolveLunaAddr(lunaAddr, stationAddr string) string {
	if v := strings.TrimSpace(lunaAddr); v != "" {
		return NormalizeLunaURL(v)
	}
	host := "127.0.0.1"
	if s := strings.TrimSpace(stationAddr); s != "" {
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "https://")
		if h, _, err := net.SplitHostPort(s); err == nil {
			host = h
		} else {
			host = strings.Split(s, "/")[0]
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s/api", net.JoinHostPort(host, "8099"))
}

// Close stops all background timers.
func (l *LunaBackend) Close() {
	l.mu.Lock()
	if l.holdTimer != nil {
		l.holdTimer.Stop()
		l.holdTimer = nil
	}
	l.mu.Unlock()

	l.timersMu.Lock()
	for _, t := range l.timers {
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	l.timers = make(map[string]*lunaPendingTimer)
	l.timersMu.Unlock()
}

type lunaDeviceInfo struct {
	Model        string   `json:"model"`
	Architecture string   `json:"architecture"`
	Features     []string `json:"features"`
	LED          struct {
		RingCount int  `json:"ring_count"`
		HasLogo   bool `json:"has_logo"`
		LogoCount int  `json:"logo_count"`
	} `json:"led"`
}

type volumeResponse struct {
	Percent int `json:"percent"`
	Volume  int `json:"volume"`
}

// Status queries the speaker status via Luna HTTP API.
func (l *LunaBackend) Status(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var dev lunaDeviceInfo
	if err := l.getJSON(ctx, "/device", &dev); err != nil {
		return Status{Reachable: false, Error: err.Error()}
	}

	if dev.LED.RingCount > 0 {
		l.mu.Lock()
		l.ringCount = dev.LED.RingCount
		l.mu.Unlock()
	}

	st := Status{
		Reachable:     true,
		LEDFirstPixel: l.currentPixel(),
		Timers:        []PendingTimer{},
	}

	var vol volumeResponse
	if err := l.getJSON(ctx, "/volume/get", &vol); err != nil {
		_ = l.getJSON(ctx, "/volume", &vol)
	}
	if vol.Percent > 0 {
		st.VolumePercent = vol.Percent
	} else if vol.Volume > 0 {
		st.VolumePercent = vol.Volume
	}

	if lux, dark, err := l.AmbientLight(ctx); err == nil {
		st.AmbientLux = lux
		st.IsDark = dark
	}

	l.timersMu.Lock()
	now := l.stationCfg.Now()
	for _, t := range l.timers {
		firesIn := int(t.firesAt.Sub(now).Seconds())
		if firesIn < 0 {
			firesIn = 0
		}
		st.Timers = append(st.Timers, PendingTimer{
			ID:        t.id,
			FiresInS:  firesIn,
			Text:      t.text,
			firesAtTS: t.firesAt.Unix(),
		})
	}
	l.timersMu.Unlock()

	return st
}

func (l *LunaBackend) currentPixel() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.currentFirstPixel == "" {
		return "00000000"
	}
	return l.currentFirstPixel
}

// SetVolume sets the volume, percent 0..MaxVolume.
func (l *LunaBackend) SetVolume(ctx context.Context, percent int) (int, error) {
	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("volume %d is out of range 0..100", percent)
	}
	applied := percent
	if applied > l.stationCfg.MaxVolume {
		applied = l.stationCfg.MaxVolume
	}

	reqBody := struct {
		Percent int `json:"percent"`
		Volume  int `json:"volume"`
	}{
		Percent: applied,
		Volume:  applied,
	}

	err := l.postJSON(ctx, "/volume/set", reqBody)
	if err != nil {
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			err = l.postJSON(ctx, "/volume", reqBody)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("luna set volume: %w", err)
	}
	return applied, nil
}

// PlayerControl controls media playback on the speaker.
func (l *LunaBackend) PlayerControl(ctx context.Context, action string) error {
	act := strings.ToLower(strings.TrimSpace(action))
	switch act {
	case "play", "resume", "continue":
		act = "play"
	case "pause", "stop":
		act = "pause"
	case "next":
		act = "next"
	case "prev", "previous":
		act = "prev"
	case "toggle":
		act = "toggle"
	default:
		return fmt.Errorf("unknown player action %q, allowed: pause, play, next, prev, toggle", action)
	}

	reqBody := struct {
		Action string `json:"action"`
	}{Action: act}

	if err := l.postJSON(ctx, "/media/command", reqBody); err != nil {
		return fmt.Errorf("luna player control: %w", err)
	}
	return nil
}

// PlaySound plays a short beep signal. Uses Luna's audio test endpoint.
func (l *LunaBackend) PlaySound(ctx context.Context, name string) error {
	if _, ok := Sounds[name]; !ok {
		return fmt.Errorf("unknown sound %q, known: %s", name, strings.Join(SoundNames(), ", "))
	}
	if err := l.postJSON(ctx, "/audio/test", nil); err != nil {
		return fmt.Errorf("luna play sound: %w", err)
	}
	return nil
}

// Say validates text but reports that speech synthesis is not supported over Luna HTTP API.
func (l *LunaBackend) Say(ctx context.Context, text string) error {
	if _, err := CleanText(text); err != nil {
		return err
	}
	return errors.New("speech synthesis is not supported by luna HTTP API; requires ssh backend")
}

// SetDND reports that DND is not supported over Luna HTTP API.
func (l *LunaBackend) SetDND(ctx context.Context, enable bool) error {
	return errors.New("dnd is not supported by luna HTTP API")
}

type lunaSensorInfo struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Unit  string   `json:"unit"`
	Value *float64 `json:"value"`
}

// AmbientLight reads the ambient light level in lux from Luna's sensor endpoint.
func (l *LunaBackend) AmbientLight(ctx context.Context) (float64, bool, error) {
	var sensors []lunaSensorInfo
	if err := l.getJSON(ctx, "/sensors", &sensors); err != nil {
		return 0, false, fmt.Errorf("luna sensors: %w", err)
	}
	for _, s := range sensors {
		idLower := strings.ToLower(s.ID)
		kindLower := strings.ToLower(s.Kind)
		unitLower := strings.ToLower(s.Unit)
		if kindLower == "illuminance" || kindLower == "light" || unitLower == "lux" ||
			strings.Contains(idLower, "ltr") || strings.Contains(idLower, "lux") || strings.Contains(idLower, "light") {
			if s.Value != nil {
				lux := *s.Value
				return lux, lux < 2.0, nil
			}
		}
	}
	return 0, false, errors.New("ambient light sensor unavailable")
}

// LED writes colors to the LED ring.
func (l *LunaBackend) LED(ctx context.Context, spec LEDSpec) error {
	if spec.Hold < 0 || spec.Hold > MaxLEDHold {
		return fmt.Errorf("hold must be between 0 and %s", MaxLEDHold)
	}
	if spec.Fraction < 0 || spec.Fraction > 1 {
		return fmt.Errorf("fraction %v is out of range 0..1", spec.Fraction)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.holdTimer != nil {
		l.holdTimer.Stop()
		l.holdTimer = nil
	}
	l.holdID++
	currentHold := l.holdID

	if strings.EqualFold(strings.TrimSpace(spec.Color), "off") {
		err := l.postJSON(ctx, "/led/off/ring", nil)
		if err != nil {
			var httpErr *httpStatusError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				err = l.postJSON(ctx, "/led/off", nil)
			}
		}
		if err != nil {
			return fmt.Errorf("luna led off: %w", err)
		}
		l.currentFirstPixel = "00000000"
		return nil
	}

	c, err := ParseColor(spec.Color)
	if err != nil {
		return err
	}

	brightness := spec.Brightness
	if brightness <= 0 {
		brightness = 40
	}
	if brightness > 100 {
		brightness = 100
	}
	scale := func(v uint8) byte {
		out := int(v) * brightness / 100
		if out > l.stationCfg.MaxLEDLevel {
			out = l.stationCfg.MaxLEDLevel
		}
		return byte(out)
	}
	r, g, b := scale(c.R), scale(c.G), scale(c.B)
	if c.W > 0 && c.R == 0 && c.G == 0 && c.B == 0 {
		w := scale(c.W)
		r, g, b = w, w, w
	}
	hexColor := fmt.Sprintf("#%02x%02x%02x", r, g, b)
	l.currentFirstPixel = fmt.Sprintf("%02x%02x%02x00", r, g, b)

	ringCount := l.ringCount
	if ringCount <= 0 {
		ringCount = 24
	}

	var colors []string
	if spec.Fraction > 0 && spec.Fraction < 1 {
		lit := int(spec.Fraction*float64(ringCount) + 0.5)
		if lit < 1 {
			lit = 1
		}
		if lit > ringCount {
			lit = ringCount
		}
		colors = make([]string, ringCount)
		for i := 0; i < lit; i++ {
			colors[i] = hexColor
		}
		for i := lit; i < ringCount; i++ {
			colors[i] = "#000000"
		}
	} else {
		colors = []string{hexColor}
	}

	reqBody := struct {
		Colors []string `json:"colors"`
	}{Colors: colors}

	if err := l.postJSON(ctx, "/led/ring", reqBody); err != nil {
		return fmt.Errorf("luna led ring: %w", err)
	}

	if spec.Hold > 0 {
		l.holdTimer = time.AfterFunc(spec.Hold, func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.holdID == currentHold {
				bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = l.postJSON(bgCtx, "/led/off/ring", nil)
				l.currentFirstPixel = "00000000"
			}
		})
	}

	return nil
}

// Timer schedules a timer in memory that triggers an audio alert and amber LED pulse on Luna when done.
func (l *LunaBackend) Timer(ctx context.Context, d time.Duration, text string) (TimerInfo, error) {
	if d < time.Second || d > MaxTimer {
		return TimerInfo{}, fmt.Errorf("timer must be between 1 second and %s", MaxTimer)
	}
	clean, err := CleanText(text)
	if err != nil {
		return TimerInfo{}, err
	}
	secs := int(d.Round(time.Second) / time.Second)
	now := l.stationCfg.Now()
	id := strconv.FormatInt(now.UnixNano(), 36)

	l.timersMu.Lock()
	pt := &lunaPendingTimer{
		id:      id,
		firesAt: now.Add(d),
		text:    clean,
	}
	pt.timer = time.AfterFunc(d, func() {
		l.timersMu.Lock()
		delete(l.timers, id)
		l.timersMu.Unlock()

		bgCtx, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer bgCancel()
		_ = l.PlaySound(bgCtx, "timer")
		_ = l.LED(bgCtx, LEDSpec{Color: "amber", Brightness: 50, Hold: 5 * time.Second})
	})
	l.timers[id] = pt
	l.timersMu.Unlock()

	return TimerInfo{ID: id, Seconds: secs}, nil
}

// Media retrieves media information from GET /api/media.
func (l *LunaBackend) Media(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := l.getJSON(ctx, "/media", &out); err != nil {
		return nil, fmt.Errorf("luna media: %w", err)
	}
	return out, nil
}

type httpStatusError struct {
	StatusCode int
	Body       string
}

func (e *httpStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func (l *LunaBackend) getJSON(ctx context.Context, path string, out any) error {
	url := l.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Luna-Local", "1")
	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &httpStatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (l *LunaBackend) postJSON(ctx context.Context, path string, body any) error {
	url := l.baseURL + path
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Luna-Local", "1")
	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &httpStatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}
	return nil
}
