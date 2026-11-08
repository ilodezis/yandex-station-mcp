//go:build live

// Smoke test against the real speaker. Run by hand, never in CI:
//
//	STATION_KEY_FILE=~/.ssh/id_ed25519_win STATION_HOST_KEY=SHA256:... go test -tags live -run Live -v ./internal/station
package station_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

func liveStation(t *testing.T) *station.Station {
	t.Helper()
	key, host := os.Getenv("STATION_KEY_FILE"), os.Getenv("STATION_HOST_KEY")
	if key == "" || host == "" {
		t.Skip("STATION_KEY_FILE and STATION_HOST_KEY not set")
	}
	addr := os.Getenv("STATION_ADDR")
	if addr == "" {
		t.Skip("STATION_ADDR not set")
	}
	r, err := station.NewSSHRunner(station.SSHConfig{Addr: addr, User: "root", KeyFile: key, HostKeySHA: host})
	if err != nil {
		t.Fatal(err)
	}
	return station.New(r, station.DefaultConfig())
}

func TestLiveStatus(t *testing.T) {
	st := liveStation(t).Status(context.Background())
	if !st.Reachable {
		t.Fatalf("unreachable: %s", st.Error)
	}
	t.Logf("status: %+v", st)
}

func TestLiveWrongHostKeyRefused(t *testing.T) {
	key := os.Getenv("STATION_KEY_FILE")
	addr := os.Getenv("STATION_ADDR")
	if key == "" || addr == "" {
		t.Skip("STATION_KEY_FILE or STATION_ADDR not set")
	}
	r, err := station.NewSSHRunner(station.SSHConfig{Addr: addr, User: "root", KeyFile: key,
		HostKeySHA: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "echo hi"); err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("a wrong host key must be refused, got %v", err)
	}
}

func liveRunner(t *testing.T) station.Runner {
	t.Helper()
	key, host := os.Getenv("STATION_KEY_FILE"), os.Getenv("STATION_HOST_KEY")
	addr := os.Getenv("STATION_ADDR")
	if key == "" || host == "" || addr == "" {
		t.Skip("STATION_KEY_FILE, STATION_HOST_KEY, and STATION_ADDR must all be set")
	}
	r, err := station.NewSSHRunner(station.SSHConfig{Addr: addr, User: "root", KeyFile: key, HostKeySHA: host})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const wipeCmd = "dd if=/dev/zero of=/dev/fb0 bs=356 count=1 2>/dev/null"

// A station event wipes the ring behind our back: the hold loop must restore the frame,
// and darken the ring when the hold is over.
func TestLiveLEDHoldRestoresAndExpires(t *testing.T) {
	r := liveRunner(t)
	s := station.New(r, station.DefaultConfig())
	ctx := context.Background()
	if err := s.LED(ctx, station.LEDSpec{Color: "amber", Brightness: 10, Hold: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if s.Status(ctx).LEDFirstPixel == "00000000" {
		t.Fatal("ring dark right after the write: the frame was lost")
	}
	if _, err := r.Run(ctx, wipeCmd); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	if s.Status(ctx).LEDFirstPixel == "00000000" {
		t.Fatal("the hold loop did not restore the wiped frame")
	}
	time.Sleep(30 * time.Second)
	if px := s.Status(ctx).LEDFirstPixel; px != "00000000" {
		t.Fatalf("ring not darkened after the hold: %s", px)
	}
}

// A newer write must stop an older hold loop from touching the ring.
func TestLiveLEDNewerWriteWins(t *testing.T) {
	r := liveRunner(t)
	s := station.New(r, station.DefaultConfig())
	ctx := context.Background()
	if err := s.LED(ctx, station.LEDSpec{Color: "amber", Brightness: 10, Hold: 40 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := s.LED(ctx, station.LEDSpec{Color: "off"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	if px := s.Status(ctx).LEDFirstPixel; px != "00000000" {
		t.Fatalf("the superseded hold loop re-lit the ring: %s", px)
	}
}

// Silent: asks the real speaker's shell to print a Cyrillic phrase back; nothing is played.
func TestLiveCyrillicSurvivesTheShell(t *testing.T) {
	r := liveRunner(t)
	const phrase = "Сессия ждёт ответа: проверь, пожалуйста — ёжик, щука, Ъ. It's fine."
	out, err := r.Run(context.Background(), "printf %s "+station.Quote(phrase))
	if err != nil || out != phrase {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestLiveAmbientLight(t *testing.T) {
	s := liveStation(t)
	lux, dark, err := s.AmbientLight(context.Background())
	if err != nil {
		t.Fatalf("ambient light: %v", err)
	}
	t.Logf("live ambient light: lux=%.4f dark=%v", lux, dark)
	if lux < 0 {
		t.Errorf("lux cannot be negative: %f", lux)
	}
}

func TestLivePlayerPause(t *testing.T) {
	s := liveStation(t)
	if err := s.PlayerControl(context.Background(), "pause"); err != nil {
		t.Fatalf("player pause: %v", err)
	}
}

func TestLiveDND(t *testing.T) {
	s := liveStation(t)
	if err := s.SetDND(context.Background(), false); err != nil {
		t.Fatalf("dnd disable: %v", err)
	}
}
