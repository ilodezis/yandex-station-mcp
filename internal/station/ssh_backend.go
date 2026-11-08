package station

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SSHBackend drives a rooted Yandex Station 2 over SSH.
type SSHBackend struct {
	r     Runner
	cfg   Config
	sayMu sync.Mutex // one phrase at a time over SSH; Alice queues the rest anyway
}

// NewSSHBackend returns an SSH backend that executes commands through r.
func NewSSHBackend(r Runner, cfg Config) *SSHBackend {
	return &SSHBackend{r: r, cfg: cfg}
}

// Runner returns the underlying Runner.
func (b *SSHBackend) Runner() Runner {
	return b.r
}

// Say speaks text in Alice's voice and returns when the speaker reports TTS completed.
func (b *SSHBackend) Say(ctx context.Context, text string) error {
	clean, err := CleanText(text)
	if err != nil {
		return err
	}
	b.sayMu.Lock()
	defer b.sayMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	_, err = b.r.Run(ctx, "say "+Quote(clean))
	return err
}

// SetVolume sets the Master volume, percent 0..MaxVolume.
func (b *SSHBackend) SetVolume(ctx context.Context, percent int) (int, error) {
	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("volume %d is out of range 0..100", percent)
	}
	applied := percent
	if applied > b.cfg.MaxVolume {
		applied = b.cfg.MaxVolume
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := b.r.Run(ctx, fmt.Sprintf("amixer set Master %d%%", applied))
	return applied, err
}

// PlaySound plays one of the allowlisted signals.
func (b *SSHBackend) PlaySound(ctx context.Context, name string) error {
	if _, ok := Sounds[name]; !ok {
		return fmt.Errorf("unknown sound %q, known: %s", name, strings.Join(SoundNames(), ", "))
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := b.r.RunStdin(ctx, "aplay -q", soundWav(name))
	return err
}

// Timer sets a timer that lives on the speaker (it survives a restart of this service):
// after d it plays the timer sound and speaks text.
func (b *SSHBackend) Timer(ctx context.Context, d time.Duration, text string) (TimerInfo, error) {
	if d < time.Second || d > MaxTimer {
		return TimerInfo{}, fmt.Errorf("timer must be between 1 second and %s", MaxTimer)
	}
	clean, err := CleanText(text)
	if err != nil {
		return TimerInfo{}, err
	}
	secs := int(d.Round(time.Second) / time.Second)
	cmd := fmt.Sprintf(
		`mkdir -p %[1]s; w=%[1]s/../timer.wav; cat > "$w"; id=$(date +%%s)$$; f=%[1]s/$id; echo "$(( $(date +%%s) + %[2]d ))|"%[3]s > "$f"; `+
			`nohup setsid sh -c 'sleep "$1"; aplay -q "$2"; say "$3"; rm -f "$4"' sh %[2]d "$w" %[3]s "$f" >/dev/null 2>&1 & echo "$id"`,
		timersDir, secs, Quote(clean))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := b.r.RunStdin(ctx, cmd, soundWav("timer"))
	if err != nil {
		return TimerInfo{}, err
	}
	return TimerInfo{ID: strings.TrimSpace(out), Seconds: secs}, nil
}

// Status queries the speaker. An unreachable speaker is a result, not an error.
func (b *SSHBackend) Status(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := b.r.Run(ctx, statusCmd)
	if err != nil {
		return Status{Reachable: false, Error: err.Error()}
	}
	return parseStatus(out)
}

// AmbientLight reads the ambient illuminance in lux from the LTR-308 sensor,
// and returns whether the environment is considered dark (< 2.0 lux).
func (b *SSHBackend) AmbientLight(ctx context.Context) (float64, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := b.r.Run(ctx, "cat /sys/bus/iio/devices/iio:device0/in_intensity_input 2>/dev/null")
	if err != nil {
		return 0, false, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return 0, false, errors.New("ambient light sensor unavailable")
	}
	lux, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse ambient lux %q: %w", trimmed, err)
	}
	return lux, lux < 2.0, nil
}

// PlayerControl controls music/audio playback on the speaker.
func (b *SSHBackend) PlayerControl(ctx context.Context, action string) error {
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := b.r.Run(ctx, "station-ctl player "+act)
	return err
}

// SetDND enables or disables Do Not Disturb mode on the speaker.
func (b *SSHBackend) SetDND(ctx context.Context, enable bool) error {
	action := "disable"
	if enable {
		action = "enable"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := b.r.Run(ctx, "station-ctl dnd "+action)
	return err
}

// LED writes a frame to the ring over SSH.
func (b *SSHBackend) LED(ctx context.Context, spec LEDSpec) error {
	if spec.Hold < 0 || spec.Hold > MaxLEDHold {
		return fmt.Errorf("hold must be between 0 and %s", MaxLEDHold)
	}
	if spec.Fraction < 0 || spec.Fraction > 1 {
		return fmt.Errorf("fraction %v is out of range 0..1", spec.Fraction)
	}
	var data []byte
	if strings.EqualFold(strings.TrimSpace(spec.Color), "off") {
		data = make([]byte, ledFrameSize)
		spec.Hold = 0
	} else {
		c, err := ParseColor(spec.Color)
		if err != nil {
			return err
		}
		data = b.frame(c, spec.Brightness, spec.Fraction)
	}
	esc := printfEscape(data)
	id := strconv.FormatInt(b.cfg.Now().UnixNano(), 36)
	file := fmt.Sprintf("%s/led-%s.bin", workDir, id)
	secs := int(spec.Hold.Round(time.Second) / time.Second)
	cmd := fmt.Sprintf(`mkdir -p %[1]s; echo %[2]s > %[1]s/led.latest; `, workDir, id)
	if spec.Hold == 0 {
		cmd += fmt.Sprintf(`printf '%s' | dd of=/dev/fb0 bs=%d count=1 2>/dev/null`, esc, ledFrameSize)
	} else {
		cmd += fmt.Sprintf(`printf '%[1]s' > %[2]s; dd if=%[2]s of=/dev/fb0 bs=%[3]d count=1 2>/dev/null; `, esc, file, ledFrameSize)
		cmd += fmt.Sprintf(`nohup setsid sh -c %s sh %d %s %s >/dev/null 2>&1 &`, Quote(holdScript(ledFrameSize)), secs, file, id)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := b.r.Run(ctx, cmd)
	return err
}

// frame builds the 356 bytes for /dev/fb0.
func (b *SSHBackend) frame(c RGBW, brightness int, fraction float64) []byte {
	if brightness <= 0 {
		brightness = 40
	}
	if brightness > 100 {
		brightness = 100
	}
	scale := func(v uint8) byte {
		out := int(v) * brightness / 100
		if out > b.cfg.MaxLEDLevel {
			out = b.cfg.MaxLEDLevel
		}
		return byte(out)
	}
	lit := LEDPixels
	if fraction > 0 && fraction < 1 {
		lit = int(fraction*float64(LEDPixels) + 0.5)
		if lit < 1 {
			lit = 1
		}
	}
	buf := make([]byte, ledFrameSize)
	px := [4]byte{scale(c.R), scale(c.G), scale(c.B), scale(c.W)}
	for i := 0; i < lit; i++ {
		for ch, off := range b.cfg.LEDOrder {
			buf[i*4+off] = px[ch]
		}
	}
	return buf
}
