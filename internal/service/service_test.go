package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/service"
	"github.com/ilodezis/yandex-station-mcp/internal/station"
	"github.com/ilodezis/yandex-station-mcp/internal/stationtest"
)

type fixedQuiet bool

func (q fixedQuiet) IsQuiet(time.Time) bool { return bool(q) }

func setup(quiet bool, led bool, budget int) (*service.Service, *stationtest.Fake, *time.Time) {
	now := time.Unix(1_000_000, 0)
	f := &stationtest.Fake{}
	st := station.New(f, station.DefaultConfig())
	svc := service.New(st, fixedQuiet(quiet), service.Options{
		LEDEnabled: led, VoiceBudget: budget, VoiceWindow: 10 * time.Minute,
		Now: func() time.Time { return now },
	})
	return svc, f, &now
}

func kinds(cmds []string) string {
	var out []string
	for _, c := range cmds {
		switch {
		case strings.HasPrefix(c, "say "):
			out = append(out, "say")
		case strings.Contains(c, "/dev/fb0"):
			out = append(out, "led")
		default:
			out = append(out, "other")
		}
	}
	return strings.Join(out, ",")
}

func TestDaytimeInfoSpeaks(t *testing.T) {
	svc, f, _ := setup(false, true, 10)
	d, err := svc.Announce(context.Background(), "сессия ждёт ответа", service.Info)
	if err != nil || d.Mode != "voice" {
		t.Fatalf("%+v %v", d, err)
	}
	if kinds(f.Commands()) != "say" {
		t.Errorf("commands = %v", f.Commands())
	}
}

func TestDaytimeCriticalSpeaksThenGlowsRed(t *testing.T) {
	svc, f, _ := setup(false, true, 10)
	d, err := svc.Announce(context.Background(), "воздух плохой", service.Critical)
	if err != nil || d.Mode != "voice+ring" {
		t.Fatalf("%+v %v", d, err)
	}
	// speech first: Alice's speaking animation would otherwise clear the ring
	if got := kinds(f.Commands()); got != "say,led" {
		t.Errorf("order = %s", got)
	}
}

func TestDaytimeCriticalWithoutLEDJustSpeaks(t *testing.T) {
	svc, f, _ := setup(false, false, 10)
	d, _ := svc.Announce(context.Background(), "воздух плохой", service.Critical)
	if d.Mode != "voice" || kinds(f.Commands()) != "say" {
		t.Errorf("%+v %v", d, f.Commands())
	}
}

func TestNightInfoIsDropped(t *testing.T) {
	svc, f, _ := setup(true, true, 10)
	d, err := svc.Announce(context.Background(), "мелочь", service.Info)
	if err != nil || d.Mode != "silent" || len(f.Commands()) != 0 {
		t.Fatalf("%+v %v %v", d, err, f.Commands())
	}
}

func TestNightImportantGlowsAmberNoVoice(t *testing.T) {
	svc, f, _ := setup(true, true, 10)
	d, err := svc.Announce(context.Background(), "ждёт тебя", service.Important)
	if err != nil || d.Mode != "ring" || kinds(f.Commands()) != "led" {
		t.Fatalf("%+v %v %v", d, err, f.Commands())
	}
	if strings.Contains(f.Commands()[0], "say ") {
		t.Error("no voice at night")
	}
}

func TestNightCriticalGlowsRedNoVoice(t *testing.T) {
	svc, f, _ := setup(true, true, 10)
	d, _ := svc.Announce(context.Background(), "pm2.5 высоко", service.Critical)
	if d.Mode != "ring" || kinds(f.Commands()) != "led" {
		t.Fatalf("%+v %v", d, f.Commands())
	}
}

func TestNightWithoutLEDIsSilent(t *testing.T) {
	svc, f, _ := setup(true, false, 10)
	d, _ := svc.Announce(context.Background(), "ждёт тебя", service.Important)
	if d.Mode != "silent" || len(f.Commands()) != 0 {
		t.Fatalf("%+v %v", d, f.Commands())
	}
}

func TestVoiceBudgetDowngradesThenRecovers(t *testing.T) {
	svc, f, now := setup(false, true, 2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if d, _ := svc.Announce(ctx, "раз", service.Important); d.Mode != "voice" {
			t.Fatalf("call %d: %+v", i, d)
		}
	}
	if d, _ := svc.Announce(ctx, "три", service.Important); d.Mode != "ring" {
		t.Errorf("over budget important must fall back to ring, got %+v", d)
	}
	if d, _ := svc.Announce(ctx, "три", service.Info); d.Mode != "silent" {
		t.Errorf("over budget info must be silent, got %+v", d)
	}
	if d, _ := svc.Announce(ctx, "срочно", service.Critical); d.Mode != "voice+ring" {
		t.Errorf("critical ignores the budget, got %+v", d)
	}
	*now = now.Add(11 * time.Minute)
	if d, _ := svc.Announce(ctx, "снова", service.Info); d.Mode != "voice" {
		t.Errorf("budget must recover after the window, got %+v", d)
	}
	_ = f
}

func TestEmptyTextRejectedBeforeAnything(t *testing.T) {
	svc, f, _ := setup(false, true, 10)
	if _, err := svc.Announce(context.Background(), "  ", service.Critical); err == nil {
		t.Fatal("empty text must fail")
	}
	if len(f.Commands()) != 0 {
		t.Error("nothing may reach the speaker")
	}
}

func TestParseUrgency(t *testing.T) {
	for in, want := range map[string]service.Urgency{"": service.Info, "INFO": service.Info, "important": service.Important, " critical ": service.Critical} {
		if got, err := service.ParseUrgency(in); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	if _, err := service.ParseUrgency("panic"); err == nil {
		t.Error("unknown urgency must fail")
	}
}
