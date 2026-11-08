package station

import (
	"context"
	"time"
)

// Backend handles transport-specific speaker operations.
// Supported backends are SSH (default, direct shell access) and Luna (HTTP API).
type Backend interface {
	Say(ctx context.Context, text string) error
	SetVolume(ctx context.Context, percent int) (int, error)
	PlaySound(ctx context.Context, name string) error
	Timer(ctx context.Context, d time.Duration, text string) (TimerInfo, error)
	Status(ctx context.Context) Status
	AmbientLight(ctx context.Context) (float64, bool, error)
	PlayerControl(ctx context.Context, action string) error
	SetDND(ctx context.Context, enable bool) error
	LED(ctx context.Context, spec LEDSpec) error
}
