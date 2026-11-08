// Package quiet decides when the speaker must not talk.
package quiet

import (
	"fmt"
	"time"
)

// Source answers whether the household is in quiet mode at t. The clock is the first
// implementation; a Vitals-backed sleep source replaces it later without touching callers.
type Source interface {
	IsQuiet(t time.Time) bool
}

// Clock is a daily window in a time zone. The window may wrap past midnight (23:00-08:00).
type Clock struct {
	from, to int // minutes since midnight
	loc      *time.Location
}

// NewClock builds a Clock from "HH:MM" strings and a zone name. Equal bounds mean never quiet.
func NewClock(from, to, zone string) (*Clock, error) {
	f, err := parseHM(from)
	if err != nil {
		return nil, fmt.Errorf("quiet from: %w", err)
	}
	t, err := parseHM(to)
	if err != nil {
		return nil, fmt.Errorf("quiet to: %w", err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("time zone: %w", err)
	}
	return &Clock{from: f, to: t, loc: loc}, nil
}

func parseHM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// IsQuiet implements Source.
func (c *Clock) IsQuiet(t time.Time) bool {
	t = t.In(c.loc)
	m := t.Hour()*60 + t.Minute()
	switch {
	case c.from == c.to:
		return false
	case c.from < c.to:
		return m >= c.from && m < c.to
	default:
		return m >= c.from || m < c.to
	}
}
