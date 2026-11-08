package station_test

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/station"
	"github.com/ilodezis/yandex-station-mcp/internal/stationtest"
)

var escRe = regexp.MustCompile(`printf '((?:\\[0-7]{3})+)'`)

// frameOf pulls the 356 bytes out of the printf in a command.
func frameOf(t *testing.T, cmd string) []byte {
	t.Helper()
	m := escRe.FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("no printf frame in %q", cmd)
	}
	var out []byte
	for _, e := range strings.Split(strings.TrimPrefix(m[1], `\`), `\`) {
		v, err := strconv.ParseUint(e, 8, 8)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, byte(v))
	}
	return out
}

func TestParseColor(t *testing.T) {
	c, err := station.ParseColor("RED")
	if err != nil || c != (station.RGBW{R: 255}) {
		t.Errorf("red = %+v, %v", c, err)
	}
	c, err = station.ParseColor("#102030")
	if err != nil || c != (station.RGBW{R: 0x10, G: 0x20, B: 0x30}) {
		t.Errorf("hex = %+v, %v", c, err)
	}
	for _, bad := range []string{"", "pink", "#12", "#gggggg", "rgb(1,2,3)"} {
		if _, err := station.ParseColor(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestLEDFrameLayoutAndCaps(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)
	// red at 50%: 255*50/100 = 127, under the 128 cap; RGBW puts red in byte 0.
	if err := s.LED(context.Background(), station.LEDSpec{Color: "red", Brightness: 50}); err != nil {
		t.Fatal(err)
	}
	fr := frameOf(t, f.Commands()[0])
	if len(fr) != 356 {
		t.Fatalf("frame is %d bytes, want 356", len(fr))
	}
	for i := 0; i < 89; i++ {
		px := fr[i*4 : i*4+4]
		if px[0] != 127 || px[1] != 0 || px[2] != 0 || px[3] != 0 {
			t.Fatalf("pixel %d = %v", i, px)
		}
	}
	// 100% white must be capped at the configured ceiling (128) on the W byte.
	f2 := &stationtest.Fake{}
	if err := newStation(f2).LED(context.Background(), station.LEDSpec{Color: "white", Brightness: 100}); err != nil {
		t.Fatal(err)
	}
	if px := frameOf(t, f2.Commands()[0])[:4]; px[3] != 128 {
		t.Errorf("white pixel = %v, want W capped at 128", px)
	}
}

func TestLEDFraction(t *testing.T) {
	f := &stationtest.Fake{}
	if err := newStation(f).LED(context.Background(), station.LEDSpec{Color: "green", Brightness: 20, Fraction: 0.5}); err != nil {
		t.Fatal(err)
	}
	fr := frameOf(t, f.Commands()[0])
	lit := 0
	for i := 0; i < 89; i++ {
		if fr[i*4+1] != 0 { // green sits in byte 1
			lit++
		}
	}
	if lit != 45 { // 0.5*89 = 44.5 rounds to 45
		t.Errorf("lit = %d, want 45", lit)
	}
	if fr[44*4+1] == 0 || fr[45*4+1] != 0 {
		t.Error("the lit part must start at pixel 0 and be contiguous")
	}
}

func TestLEDHoldClearsOnlyOurFrame(t *testing.T) {
	f := &stationtest.Fake{}
	if err := newStation(f).LED(context.Background(), station.LEDSpec{Color: "amber", Hold: 5 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	cmd := f.Commands()[0]
	for _, part := range []string{"sleep 5", "cmp -s -", "/dev/zero", "sh 300 /tmp/stmcp/led-", "nohup setsid", "led.latest", "tr -d"} {
		if !strings.Contains(cmd, part) {
			t.Errorf("hold command lacks %q:\n%s", part, cmd)
		}
	}
}

func TestLEDNoHoldHasNoBackgroundJob(t *testing.T) {
	f := &stationtest.Fake{}
	if err := newStation(f).LED(context.Background(), station.LEDSpec{Color: "blue"}); err != nil {
		t.Fatal(err)
	}
	if cmd := f.Commands()[0]; strings.Contains(cmd, "nohup") || !strings.Contains(cmd, "of=/dev/fb0") {
		t.Errorf("cmd = %q", cmd)
	}
}

func TestLEDOffWritesZeros(t *testing.T) {
	f := &stationtest.Fake{}
	if err := newStation(f).LED(context.Background(), station.LEDSpec{Color: "off", Hold: time.Minute}); err != nil {
		t.Fatal(err)
	}
	cmd := f.Commands()[0]
	for _, b := range frameOf(t, cmd) {
		if b != 0 {
			t.Fatal("off frame must be all zeros")
		}
	}
	if strings.Contains(cmd, "nohup") {
		t.Error("off needs no hold timer")
	}
}

func TestLEDRejectsBadInput(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)
	for _, spec := range []station.LEDSpec{
		{Color: "pink"},
		{Color: "red", Hold: 2 * time.Hour},
		{Color: "red", Hold: -time.Second},
		{Color: "red", Fraction: 1.5},
		{Color: "red", Fraction: -0.1},
	} {
		if err := s.LED(context.Background(), spec); err == nil {
			t.Errorf("%+v must fail", spec)
		}
	}
	if len(f.Commands()) != 0 {
		t.Error("rejected specs must not reach the speaker")
	}
}
