package station_test

import (
	"strings"
	"testing"

	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

func TestStyled(t *testing.T) {
	cases := []struct {
		voice, effect, want string
	}{
		{"", "", "привет"},
		{"zahar", "", `<speaker voice="zahar">привет`},
		{" Zahar ", "", `<speaker voice="zahar">привет`},
		{"", "hamster", `<speaker effect="hamster">привет`},
		{"ermil", "megaphone", `<speaker voice="ermil" effect="megaphone">привет`},
	}
	for _, c := range cases {
		got, err := station.Styled("привет", c.voice, c.effect)
		if err != nil {
			t.Fatalf("Styled(%q, %q): %v", c.voice, c.effect, err)
		}
		if got != c.want {
			t.Errorf("Styled(%q, %q) = %q, want %q", c.voice, c.effect, got, c.want)
		}
	}
}

func TestStyledRejectsUnknown(t *testing.T) {
	// The speaker drops a phrase with an unknown voice without any error, so the
	// service must refuse it instead of reporting a delivery that never sounded.
	if _, err := station.Styled("x", "filipp", ""); err == nil || !strings.Contains(err.Error(), "zahar") {
		t.Fatalf("unknown voice: err = %v, want an error listing the voices", err)
	}
	if _, err := station.Styled("x", "", "echo\" onload=\"x"); err == nil {
		t.Fatal("unknown effect accepted")
	}
}
