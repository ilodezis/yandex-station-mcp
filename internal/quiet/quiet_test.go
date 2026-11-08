package quiet_test

import (
	"testing"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/quiet"
)

func TestWrapAroundMidnight(t *testing.T) {
	c, err := quiet.NewClock("23:00", "08:00", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 8, h, m, 0, 0, time.UTC) }
	cases := []struct {
		h, m int
		want bool
	}{{22, 59, false}, {23, 0, true}, {3, 30, true}, {7, 59, true}, {8, 0, false}, {12, 0, false}}
	for _, tc := range cases {
		if got := c.IsQuiet(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("%02d:%02d quiet = %v, want %v", tc.h, tc.m, got, tc.want)
		}
	}
}

func TestSameDayWindowAndZone(t *testing.T) {
	c, err := quiet.NewClock("13:00", "15:00", "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	// 11:00 UTC is 14:00 in Moscow (UTC+3, no DST).
	if !c.IsQuiet(time.Date(2026, 11, 8, 11, 0, 0, 0, time.UTC)) {
		t.Error("14:00 Moscow must be quiet")
	}
	if c.IsQuiet(time.Date(2026, 11, 8, 13, 0, 0, 0, time.UTC)) {
		t.Error("16:00 Moscow must not be quiet")
	}
}

func TestEqualBoundsNeverQuiet(t *testing.T) {
	c, _ := quiet.NewClock("00:00", "00:00", "UTC")
	if c.IsQuiet(time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)) {
		t.Error("equal bounds mean quiet mode is off")
	}
}

func TestBadConfig(t *testing.T) {
	for _, tc := range [][3]string{{"25:00", "08:00", "UTC"}, {"23:00", "8am", "UTC"}, {"23:00", "08:00", "Mars/Base"}} {
		if _, err := quiet.NewClock(tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("%v must fail", tc)
		}
	}
}
