// Package service holds the policy on top of the speaker: when to talk, when to only glow.
package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ilodezis/yandex-station-mcp/internal/quiet"
	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

// Urgency is how urgently the message needs human attention.
type Urgency string

const (
	Info      Urgency = "info"      // nice to know: voice by day, nothing at night
	Important Urgency = "important" // needs him soon: voice by day, amber ring at night
	Critical  Urgency = "critical"  // act now: voice plus red ring by day, red ring at night
)

// ParseUrgency validates the tool argument; empty means info.
func ParseUrgency(s string) (Urgency, error) {
	switch u := Urgency(strings.ToLower(strings.TrimSpace(s))); u {
	case "", Info:
		return Info, nil
	case Important, Critical:
		return u, nil
	}
	return "", fmt.Errorf("urgency %q is not one of info, important, critical", s)
}

// Options configures the policy.
type Options struct {
	LEDEnabled  bool
	VoiceBudget int           // voice announcements allowed per VoiceWindow, critical excluded
	VoiceWindow time.Duration //
	Now         func() time.Time
}

// Service is the policy layer.
type Service struct {
	st    *station.Station
	quiet quiet.Source
	opt   Options

	mu    sync.Mutex
	voice []time.Time // recent voice announcements, for the budget
}

// New builds a Service.
func New(st *station.Station, q quiet.Source, opt Options) *Service {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.VoiceBudget <= 0 {
		opt.VoiceBudget = 10
	}
	if opt.VoiceWindow <= 0 {
		opt.VoiceWindow = 10 * time.Minute
	}
	return &Service{st: st, quiet: q, opt: opt}
}

// Station exposes the raw speaker for the tools that need no policy.
func (s *Service) Station() *station.Station { return s.st }

// Quiet reports the current quiet mode.
func (s *Service) Quiet() bool { return s.quiet.IsQuiet(s.opt.Now()) }

// Delivery tells the caller what actually happened to an announcement.
type Delivery struct {
	Mode   string // "voice", "voice+ring", "ring", "silent"
	Reason string
}

func (s *Service) takeVoiceSlot(u Urgency) bool {
	if u == Critical {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	kept := s.voice[:0]
	for _, t := range s.voice {
		if now.Sub(t) < s.opt.VoiceWindow {
			kept = append(kept, t)
		}
	}
	s.voice = kept
	if len(s.voice) >= s.opt.VoiceBudget {
		return false
	}
	s.voice = append(s.voice, now)
	return true
}

func (s *Service) ring(ctx context.Context, u Urgency, hold time.Duration) error {
	color := "amber"
	if u == Critical {
		color = "red"
	}
	return s.st.LED(ctx, station.LEDSpec{Color: color, Brightness: 50, Hold: hold})
}

// Announce delivers text according to urgency and quiet mode:
//
//	day:   info and important speak; critical speaks and lights the ring red
//	night: no voice at all; important glows amber, critical glows red, info is dropped
//
// The caller learns which of these happened. Voice is also rationed (VoiceBudget per window,
// critical exempt) so a chatty agent cannot turn the speaker into a notification feed.
func (s *Service) Announce(ctx context.Context, text string, u Urgency) (Delivery, error) {
	if _, err := station.CleanText(text); err != nil {
		return Delivery{}, err
	}
	if s.Quiet() {
		switch {
		case u == Info:
			return Delivery{"silent", "quiet mode: info is dropped"}, nil
		case !s.opt.LEDEnabled:
			return Delivery{"silent", "quiet mode and the ring is disabled"}, nil
		}
		hold := 5 * time.Minute
		if u == Critical {
			hold = 20 * time.Minute
		}
		if err := s.ring(ctx, u, hold); err != nil {
			return Delivery{}, err
		}
		return Delivery{"ring", "quiet mode: light only"}, nil
	}
	if !s.takeVoiceSlot(u) {
		if u != Info && s.opt.LEDEnabled {
			if err := s.ring(ctx, u, 5*time.Minute); err != nil {
				return Delivery{}, err
			}
			return Delivery{"ring", "voice budget spent: light only"}, nil
		}
		return Delivery{"silent", "voice budget spent"}, nil
	}
	if u == Critical && s.opt.LEDEnabled {
		// Speak first: Alice's speaking animation clears the ring when it ends.
		if err := s.st.Say(ctx, text); err != nil {
			return Delivery{}, err
		}
		if err := s.ring(ctx, u, time.Minute); err != nil {
			return Delivery{}, err
		}
		return Delivery{"voice+ring", "critical"}, nil
	}
	if err := s.st.Say(ctx, text); err != nil {
		return Delivery{}, err
	}
	return Delivery{"voice", "daytime"}, nil
}
