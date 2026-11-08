package station

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RGBW is one LED colour, each channel 0..255.
type RGBW struct{ R, G, B, W uint8 }

var namedColors = map[string]RGBW{
	"red":    {R: 255},
	"green":  {G: 255},
	"blue":   {B: 255},
	"amber":  {R: 255, G: 110},
	"yellow": {R: 255, G: 190},
	"purple": {R: 140, B: 105},
	"cyan":   {G: 200, B: 255},
	"white":  {W: 255},
}

// ColorNames lists the named colours, for tool descriptions.
func ColorNames() []string {
	return []string{"red", "amber", "yellow", "green", "cyan", "blue", "purple", "white"}
}

// ParseColor accepts a colour name, "#rrggbb" or "off".
func ParseColor(s string) (RGBW, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := namedColors[s]; ok {
		return c, nil
	}
	if len(s) == 7 && s[0] == '#' {
		v, err := strconv.ParseUint(s[1:], 16, 32)
		if err == nil {
			return RGBW{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, nil
		}
	}
	return RGBW{}, fmt.Errorf("unknown colour %q: use %s or #rrggbb", s, strings.Join(ColorNames(), ", "))
}

// LEDSpec is a request to light the ring.
type LEDSpec struct {
	Color      string  // name, #rrggbb; "off" darkens the ring
	Brightness int     // percent 1..100, default 40
	Fraction   float64 // share of the ring lit from pixel 0, (0,1]; 0 means the whole ring
	Hold       time.Duration
}

// frame builds the 356 bytes for /dev/fb0.
func (s *Station) frame(c RGBW, brightness int, fraction float64) []byte {
	if brightness <= 0 {
		brightness = 40
	}
	if brightness > 100 {
		brightness = 100
	}
	scale := func(v uint8) byte {
		out := int(v) * brightness / 100
		if out > s.cfg.MaxLEDLevel {
			out = s.cfg.MaxLEDLevel
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
		for ch, off := range s.cfg.LEDOrder {
			buf[i*4+off] = px[ch]
		}
	}
	return buf
}

// printfEscape renders bytes as POSIX printf octal escapes (every byte, so the string is plain ASCII).
func printfEscape(b []byte) string {
	var sb strings.Builder
	for _, v := range b {
		fmt.Fprintf(&sb, `\%03o`, v)
	}
	return sb.String()
}

// LED writes a frame to the ring. The speaker's own animations (listening, speaking) draw over
// it and clear the ring when they end, so this is best effort until Alice next does something.
// With Hold > 0 the speaker itself darkens the ring afterwards, but only if the ring still shows
// exactly our frame (a later write or one of Alice's animations is left alone).
func (s *Station) LED(ctx context.Context, spec LEDSpec) error {
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
		data = s.frame(c, spec.Brightness, spec.Fraction)
	}
	esc := printfEscape(data)
	id := strconv.FormatInt(s.cfg.Now().UnixNano(), 36)
	file := fmt.Sprintf("%s/led-%s.bin", workDir, id)
	secs := int(spec.Hold.Round(time.Second) / time.Second)
	// Every write first claims "latest", so an older hold loop notices it was superseded and leaves
	// the ring alone. Without a hold the frame goes straight to the ring and nothing is left behind.
	cmd := fmt.Sprintf(`mkdir -p %[1]s; echo %[2]s > %[1]s/led.latest; `, workDir, id)
	if spec.Hold == 0 {
		cmd += fmt.Sprintf(`printf '%s' | dd of=/dev/fb0 bs=%d count=1 2>/dev/null`, esc, ledFrameSize)
	} else {
		cmd += fmt.Sprintf(`printf '%[1]s' > %[2]s; dd if=%[2]s of=/dev/fb0 bs=%[3]d count=1 2>/dev/null; `, esc, file, ledFrameSize)
		cmd += fmt.Sprintf(`nohup setsid sh -c %s sh %d %s %s >/dev/null 2>&1 &`, Quote(holdScript(ledFrameSize)), secs, file, id)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.r.Run(ctx, cmd)
	return err
}

// holdScript runs on the speaker for the length of a hold ($1 seconds, $2 frame file, $3 write id).
// Station events (a network blip, Alice waking up) clear the ring behind our back, so every 5 s it
// restores the frame if the ring went completely dark. Anything else on the ring is Alice's own
// animation: it is left alone. A newer led write ends the loop at once. At the end the ring is
// darkened, but only if it still shows exactly our frame.
func holdScript(frame int) string {
	return fmt.Sprintf(`end=$(( $(date +%%s) + $1 ))
while [ "$(date +%%s)" -lt "$end" ]; do
  sleep 5
  [ "$(cat %[1]s/led.latest 2>/dev/null)" = "$3" ] || { rm -f "$2"; exit 0; }
  [ -z "$(dd if=/dev/fb0 bs=%[2]d count=1 2>/dev/null | tr -d '\000')" ] && dd if="$2" of=/dev/fb0 bs=%[2]d count=1 2>/dev/null
done
[ "$(cat %[1]s/led.latest 2>/dev/null)" = "$3" ] && dd if=/dev/fb0 bs=%[2]d count=1 2>/dev/null | cmp -s - "$2" && dd if=/dev/zero of=/dev/fb0 bs=%[2]d count=1 2>/dev/null
rm -f "$2"`, workDir, frame)
}
