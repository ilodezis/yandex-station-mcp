package station_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/station"
	"github.com/ilodezis/yandex-station-mcp/internal/stationtest"
)

func newStation(f *stationtest.Fake) *station.Station {
	cfg := station.DefaultConfig()
	cfg.Now = func() time.Time { return time.Unix(1_000_000, 0) }
	return station.New(f, cfg)
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       `'plain'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf)":   `'$(rm -rf)'`,
		"a; b `c`":    "'a; b `c`'",
		"Привет, мир": "'Привет, мир'",
	}
	for in, want := range cases {
		if got := station.Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestCleanText(t *testing.T) {
	got, err := station.CleanText("  line one\nline\ttwo \x00 end ")
	if err != nil || got != "line one line two end" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := station.CleanText(" \n\t "); err == nil {
		t.Error("blank text must fail")
	}
	if _, err := station.CleanText(strings.Repeat("я", station.MaxTextRunes+1)); err == nil {
		t.Error("overlong text must fail")
	}
	if _, err := station.CleanText(strings.Repeat("я", station.MaxTextRunes)); err != nil {
		t.Errorf("limit itself is allowed: %v", err)
	}
}

func TestSayQuotesInjection(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)
	if err := s.Say(context.Background(), "it's $(reboot); `id`"); err != nil {
		t.Fatal(err)
	}
	want := `say 'it'\''s $(reboot); ` + "`id`" + `'`
	if got := f.Commands(); len(got) != 1 || got[0] != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

func TestSayPropagatesError(t *testing.T) {
	f := &stationtest.Fake{Err: errors.New("station unreachable")}
	if err := newStation(f).Say(context.Background(), "hi"); err == nil {
		t.Fatal("want error")
	}
}

func TestSetVolumeCapsAtCeiling(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)
	n, err := s.SetVolume(context.Background(), 100)
	if err != nil || n != 80 {
		t.Fatalf("applied %d, %v; want 80", n, err)
	}
	if got := f.Commands()[0]; got != "amixer set Master 80%" {
		t.Errorf("cmd = %q", got)
	}
	if _, err := s.SetVolume(context.Background(), 101); err == nil {
		t.Error("101 must be rejected")
	}
	if _, err := s.SetVolume(context.Background(), -1); err == nil {
		t.Error("-1 must be rejected")
	}
}

func TestPlaySound(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)
	if err := s.PlaySound(context.Background(), "timer"); err != nil {
		t.Fatal(err)
	}
	if got := f.Commands()[0]; got != "aplay -q" {
		t.Errorf("cmd = %q", got)
	}
	if in := f.Stdins[0]; !bytes.HasPrefix(in, []byte("RIFF")) || !bytes.Contains(in[:12], []byte("WAVE")) {
		t.Errorf("aplay must get a real wav on stdin, got %.12q", in)
	}
	if err := s.PlaySound(context.Background(), "../../etc/passwd"); err == nil {
		t.Error("names outside the allowlist must fail")
	}
	if len(f.Commands()) != 1 {
		t.Error("a rejected sound must not reach the speaker")
	}
}

func TestTimer(t *testing.T) {
	f := &stationtest.Fake{Replies: map[string]string{"nohup": "1000000123\n"}}
	s := newStation(f)
	info, err := s.Timer(context.Background(), 90*time.Second, "чай готов")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "1000000123" || info.Seconds != 90 {
		t.Errorf("info = %+v", info)
	}
	cmd := f.Commands()[0]
	for _, part := range []string{"sleep \"$1\"", "sh 90 ", "'чай готов'", "aplay -q \"$2\"", "cat > \"$w\"", "/tmp/stmcp/timers"} {
		if !strings.Contains(cmd, part) {
			t.Errorf("command lacks %q:\n%s", part, cmd)
		}
	}
	if len(cmd) > 8000 {
		t.Errorf("command is %d bytes: dropbear refuses command lines over 9000, sound data goes on stdin", len(cmd))
	}
	if in := f.Stdins[0]; !bytes.HasPrefix(in, []byte("RIFF")) {
		t.Errorf("timer sound must be a wav on stdin, got %.12q", in)
	}
	for _, bad := range []time.Duration{0, 500 * time.Millisecond, 25 * time.Hour} {
		if _, err := s.Timer(context.Background(), bad, "x"); err == nil {
			t.Errorf("duration %v must fail", bad)
		}
	}
	if _, err := s.Timer(context.Background(), time.Minute, " "); err == nil {
		t.Error("empty text must fail")
	}
}

func TestStatusParse(t *testing.T) {
	out := "  Mono: Playback 128 [50%]\n--\n" +
		"73561.22 280000.11\n--\n" +
		"1000000\n--\n" +
		" 10 00 00 00\n--\n" +
		"99|1000090|чай готов\n" +
		"98|999990|просрочен\n--\n" +
		"0.266666\n"
	f := &stationtest.Fake{Replies: map[string]string{"amixer sget": out}}
	st := newStation(f).Status(context.Background())
	if !st.Reachable || st.VolumePercent != 50 || st.UptimeSeconds != 73561 || st.LEDFirstPixel != "10000000" {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Timers) != 2 || st.Timers[0].ID != "99" || st.Timers[0].FiresInS != 90 || st.Timers[0].Text != "чай готов" {
		t.Errorf("timers = %+v", st.Timers)
	}
	if st.Timers[1].FiresInS != 0 {
		t.Errorf("an overdue timer clamps to 0, got %d", st.Timers[1].FiresInS)
	}
	if st.AmbientLux != 0.266666 || !st.IsDark {
		t.Errorf("ambient lux = %f, isDark = %v; want 0.266666, true", st.AmbientLux, st.IsDark)
	}
}

func TestAmbientLight(t *testing.T) {
	f := &stationtest.Fake{Replies: map[string]string{
		"in_intensity_input": "0.150000\n",
	}}
	s := newStation(f)
	lux, dark, err := s.AmbientLight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lux != 0.15 || !dark {
		t.Errorf("lux = %f, dark = %v; want 0.15, true", lux, dark)
	}

	// Bright room test (> 2.0 lux)
	f.Replies["in_intensity_input"] = "150.5\n"
	lux, dark, err = s.AmbientLight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lux != 150.5 || dark {
		t.Errorf("lux = %f, dark = %v; want 150.5, false", lux, dark)
	}

	// Unavailable/empty sensor error
	f.Replies["in_intensity_input"] = "\n"
	if _, _, err := s.AmbientLight(context.Background()); err == nil {
		t.Error("empty output must fail")
	}
}

func TestPlayerControl(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)

	cases := map[string]string{
		"pause":    "station-ctl player pause",
		"stop":     "station-ctl player pause",
		"play":     "station-ctl player play",
		"resume":   "station-ctl player play",
		"continue": "station-ctl player play",
		"next":     "station-ctl player next",
		"prev":     "station-ctl player prev",
		"previous": "station-ctl player prev",
		"toggle":   "station-ctl player toggle",
	}

	for action, wantCmd := range cases {
		f.Clear()
		if err := s.PlayerControl(context.Background(), action); err != nil {
			t.Fatalf("action %q failed: %v", action, err)
		}
		if got := f.Commands()[0]; got != wantCmd {
			t.Errorf("action %q: got %q, want %q", action, got, wantCmd)
		}
	}

	if err := s.PlayerControl(context.Background(), "invalid-action"); err == nil {
		t.Error("invalid action must return error")
	}
}

func TestSetDND(t *testing.T) {
	f := &stationtest.Fake{}
	s := newStation(f)

	if err := s.SetDND(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := f.Commands()[0]; got != "station-ctl dnd enable" {
		t.Errorf("cmd = %q, want station-ctl dnd enable", got)
	}

	f.Clear()
	if err := s.SetDND(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := f.Commands()[0]; got != "station-ctl dnd disable" {
		t.Errorf("cmd = %q, want station-ctl dnd disable", got)
	}
}

func TestStatusUnreachableIsAResult(t *testing.T) {
	f := &stationtest.Fake{Err: errors.New("station unreachable: timeout")}
	st := newStation(f).Status(context.Background())
	if st.Reachable || !strings.Contains(st.Error, "unreachable") {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusNoTimersIsEmptyNotNil(t *testing.T) {
	f := &stationtest.Fake{Replies: map[string]string{"amixer sget": "Mono: Playback 255 [100%]\n--\n10.0 1.0\n--\n5\n--\n 00 00 00 00\n--\n"}}
	st := newStation(f).Status(context.Background())
	if st.Timers == nil || len(st.Timers) != 0 {
		t.Errorf("timers = %#v", st.Timers)
	}
}

func TestCleanTextRejectsBrokenUTF8(t *testing.T) {
	for _, bad := range []string{"\xff\xfe", "ok \xd0", "mojibake \ufffd\ufffd\ufffd"} {
		if _, err := station.CleanText(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	for _, good := range []string{"Привет, мир", "Воздух: PM2.5 — выше нормы ёж", "日本語", "emoji 🙂"} {
		if got, err := station.CleanText(good); err != nil || got != good {
			t.Errorf("%q: got %q, %v", good, got, err)
		}
	}
}

func TestSayKeepsCyrillicBytesIntact(t *testing.T) {
	f := &stationtest.Fake{}
	const phrase = "Сессия ждёт ответа: проверь, пожалуйста — ёжик, щука, Ъ"
	if err := newStation(f).Say(context.Background(), phrase); err != nil {
		t.Fatal(err)
	}
	if got, want := f.Commands()[0], "say '"+phrase+"'"; got != want {
		t.Errorf("command bytes changed:\n got %q\nwant %q", got, want)
	}
}
