// Package stationtest is the stand-in speaker for tests.
package stationtest

import (
	"context"
	"strings"
	"sync"
)

// Fake records every command and answers from Replies (first key that is a substring of the command).
type Fake struct {
	mu      sync.Mutex
	Cmds    []string
	Replies map[string]string
	Err     error
	Stdins  [][]byte
}

// Run implements station.Runner.
func (f *Fake) Run(ctx context.Context, cmd string) (string, error) {
	return f.RunStdin(ctx, cmd, nil)
}

// RunStdin implements station.Runner and keeps what was sent on stdin.
func (f *Fake) RunStdin(_ context.Context, cmd string, stdin []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Cmds = append(f.Cmds, cmd)
	f.Stdins = append(f.Stdins, stdin)
	if f.Err != nil {
		return "", f.Err
	}
	// Exact match first
	if v, ok := f.Replies[cmd]; ok {
		return v, nil
	}
	// Prefix match next
	for k, v := range f.Replies {
		if strings.HasPrefix(cmd, k) {
			return v, nil
		}
	}
	// Substring match
	for k, v := range f.Replies {
		if strings.Contains(cmd, k) {
			return v, nil
		}
	}
	return "", nil
}

// Commands returns a copy of what was run.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Cmds...)
}

// Clear empties the recorded commands and stdins.
func (f *Fake) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Cmds = nil
	f.Stdins = nil
}
