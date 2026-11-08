package station

import (
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

// printfEscape renders bytes as POSIX printf octal escapes (every byte, so the string is plain ASCII).
func printfEscape(b []byte) string {
	var sb strings.Builder
	for _, v := range b {
		fmt.Fprintf(&sb, `\%03o`, v)
	}
	return sb.String()
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
