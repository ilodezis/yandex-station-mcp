package station

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Paths on the speaker. /tmp is RAM: state is lost on reboot, which is what we want.
	workDir   = "/tmp/stmcp"
	timersDir = workDir + "/timers"

	// LED ring geometry from /dev/fb0: 89 pixels, 32 bpp, 356 bytes per frame.
	LEDPixels    = 89
	ledFrameSize = LEDPixels * 4

	MaxTextRunes = 500
	MaxTimer     = 24 * time.Hour
	MaxLEDHold   = time.Hour
)

// Config tunes the speaker driver.
type Config struct {
	MaxVolume   int    // ceiling for set_volume, percent (protects ears and neighbours)
	MaxLEDLevel int    // ceiling for one LED channel byte, 1..255
	LEDOrder    [4]int // byte offset inside a pixel of R, G, B, W
	Now         func() time.Time
}

// DefaultConfig: pixel layout is RGBW (byte 2 is blue, seen on the ring 2026-11-08; see knowledge/station-setup).
func DefaultConfig() Config {
	return Config{MaxVolume: 80, MaxLEDLevel: 128, LEDOrder: [4]int{0, 1, 2, 3}, Now: time.Now}
}

func normalizeConfig(cfg Config) Config {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxVolume <= 0 || cfg.MaxVolume > 100 {
		cfg.MaxVolume = 80
	}
	if cfg.MaxLEDLevel <= 0 || cfg.MaxLEDLevel > 255 {
		cfg.MaxLEDLevel = 128
	}
	return cfg
}

// Station is the speaker as the rest of the service sees it.
type Station struct {
	backend Backend
	cfg     Config
}

// New returns a Station that talks through r over SSH.
func New(r Runner, cfg Config) *Station {
	cfg = normalizeConfig(cfg)
	return &Station{
		backend: NewSSHBackend(r, cfg),
		cfg:     cfg,
	}
}

// NewWithBackend returns a Station driven by any Backend implementation.
func NewWithBackend(b Backend, cfg Config) *Station {
	cfg = normalizeConfig(cfg)
	return &Station{
		backend: b,
		cfg:     cfg,
	}
}

// Backend returns the underlying backend driver.
func (s *Station) Backend() Backend {
	return s.backend
}

// Runner returns the underlying Runner if the station is backed by SSH, or nil otherwise.
func (s *Station) Runner() Runner {
	if sb, ok := s.backend.(*SSHBackend); ok {
		return sb.r
	}
	return nil
}

// Config returns the speaker driver configuration.
func (s *Station) Config() Config {
	return s.cfg
}

// Quote makes s one literal word for the speaker's POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CleanText prepares speech text: newlines and control characters become spaces, ends are
// trimmed, and the length is capped. An empty result is an error.
func CleanText(s string) (string, error) {
	// Garbled input (a console in the wrong code page, a lossy hop) must not be spoken as mush.
	if !utf8.ValidString(s) || strings.ContainsRune(s, utf8.RuneError) {
		return "", errors.New("text is not valid UTF-8")
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if out == "" {
		return "", errors.New("text is empty")
	}
	if n := len([]rune(out)); n > MaxTextRunes {
		return "", fmt.Errorf("text is %d characters, the limit is %d", n, MaxTextRunes)
	}
	return out, nil
}

// Say speaks text in Alice's voice and returns when TTS completes.
func (s *Station) Say(ctx context.Context, text string) error {
	return s.backend.Say(ctx, text)
}

// SetVolume sets the Master volume, percent 0..MaxVolume.
func (s *Station) SetVolume(ctx context.Context, percent int) (int, error) {
	return s.backend.SetVolume(ctx, percent)
}

// PlaySound plays one of the allowlisted signals.
func (s *Station) PlaySound(ctx context.Context, name string) error {
	return s.backend.PlaySound(ctx, name)
}

// SoundNames lists the allowlisted sounds, sorted.
func SoundNames() []string {
	names := make([]string, 0, len(Sounds))
	for n := range Sounds {
		names = append(names, n)
	}
	// tiny list: insertion sort keeps this dependency free
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// TimerInfo describes a timer set on the speaker.
type TimerInfo struct {
	ID      string
	Seconds int
}

// Timer sets a timer that lives on the speaker.
func (s *Station) Timer(ctx context.Context, d time.Duration, text string) (TimerInfo, error) {
	return s.backend.Timer(ctx, d, text)
}

// Status is what the speaker reports about itself.
type Status struct {
	Reachable     bool
	Error         string
	VolumePercent int
	UptimeSeconds int
	LEDFirstPixel string // hex of pixel 0, all zeros when the ring is dark
	Timers        []PendingTimer
	AmbientLux    float64
	IsDark        bool
}

// PendingTimer is a timer waiting on the speaker.
type PendingTimer struct {
	ID        string
	FiresInS  int
	Text      string
	firesAtTS int64
}

var volRe = regexp.MustCompile(`\[(\d+)%\]`)

const statusCmd = `amixer sget Master 2>/dev/null | tail -1; echo '--'; cat /proc/uptime; echo '--'; ` +
	`date +%s; echo '--'; dd if=/dev/fb0 bs=4 count=1 2>/dev/null | od -An -tx1; echo '--'; ` +
	`for f in ` + timersDir + `/*; do [ -f "$f" ] && echo "$(basename "$f")|$(cat "$f")"; done; echo '--'; ` +
	`cat /sys/bus/iio/devices/iio:device0/in_intensity_input 2>/dev/null; true`

// Status queries the speaker. An unreachable speaker is a result, not an error.
func (s *Station) Status(ctx context.Context) Status {
	return s.backend.Status(ctx)
}

func parseStatus(out string) Status {
	parts := strings.Split(out, "--\n")
	st := Status{Reachable: true}
	get := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	if m := volRe.FindStringSubmatch(get(0)); m != nil {
		st.VolumePercent, _ = strconv.Atoi(m[1])
	}
	if f := strings.Fields(get(1)); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			st.UptimeSeconds = int(v)
		}
	}
	now, _ := strconv.ParseInt(get(2), 10, 64)
	st.LEDFirstPixel = strings.Join(strings.Fields(get(3)), "")
	st.Timers = []PendingTimer{}
	for _, line := range strings.Split(get(4), "\n") {
		id, rest, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			continue
		}
		fire, text, ok := strings.Cut(rest, "|")
		if !ok {
			continue
		}
		at, err := strconv.ParseInt(fire, 10, 64)
		if err != nil {
			continue
		}
		in := int(at - now)
		if in < 0 {
			in = 0
		}
		st.Timers = append(st.Timers, PendingTimer{ID: id, FiresInS: in, Text: text, firesAtTS: at})
	}
	if s := get(5); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			st.AmbientLux = v
			st.IsDark = v < 2.0
		}
	}
	return st
}

// AmbientLight reads the ambient illuminance in lux from the LTR-308 sensor,
// and returns whether the environment is considered dark (< 2.0 lux).
func (s *Station) AmbientLight(ctx context.Context) (float64, bool, error) {
	return s.backend.AmbientLight(ctx)
}

// PlayerControl controls music/audio playback on the speaker.
func (s *Station) PlayerControl(ctx context.Context, action string) error {
	return s.backend.PlayerControl(ctx, action)
}

// SetDND enables or disables Do Not Disturb mode on the speaker.
func (s *Station) SetDND(ctx context.Context, enable bool) error {
	return s.backend.SetDND(ctx, enable)
}

// LED writes a frame to the ring.
func (s *Station) LED(ctx context.Context, spec LEDSpec) error {
	return s.backend.LED(ctx, spec)
}
