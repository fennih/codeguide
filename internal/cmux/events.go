package cmux

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Presence follows cmux's event stream to know which surface is in front and
// whether the cmux window has keyboard focus. Event content is untrusted;
// only validated ids and timestamps are kept.
type Presence struct {
	mu      sync.Mutex
	known   bool // a focus event has been seen
	surface string
	keyed   bool
	since   time.Time
}

// Front returns the surface in front and since when, if cmux is focused.
func (p *Presence) Front() (surface string, since time.Time, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.known || !p.keyed || p.surface == "" {
		return "", time.Time{}, false
	}
	return p.surface, p.since, true
}

// Seed sets the initial front surface before any event arrives. keyed says
// whether the cmux window is known to have focus.
func (p *Presence) Seed(surface string, keyed bool) {
	surface = strings.ToUpper(surface)
	if !ValidID(surface) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.known {
		p.known, p.surface, p.keyed, p.since = true, surface, keyed, time.Now()
	}
}

type event struct {
	Name       string    `json:"name"`
	OccurredAt time.Time `json:"occurred_at"`
	Payload    struct {
		SurfaceID string `json:"surface_id"`
	} `json:"payload"`
}

func (p *Presence) apply(e event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Name {
	case "surface.focused":
		id := strings.ToUpper(e.Payload.SurfaceID)
		if !ValidID(id) {
			return
		}
		// Switching tabs almost always means the user is in the cmux window.
		p.known, p.surface, p.since, p.keyed = true, id, e.OccurredAt, true
	case "window.keyed":
		p.keyed = true
	case "window.unkeyed":
		p.keyed = false
	}
}

// Follow streams events until ctx ends, restarting the stream with backoff.
func (c Client) Follow(ctx context.Context, p *Presence) {
	wait := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		c.stream(ctx, p)
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (c Client) stream(ctx context.Context, p *Presence) {
	// Fixed argument vector; Bin is resolved once at startup. No shell.
	cmd := exec.CommandContext(ctx, c.Bin, "events", "--reconnect", // nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
		"--name", "surface.focused", "--name", "window.keyed", "--name", "window.unkeyed")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Name != "" {
			p.apply(e)
		}
	}
	_ = cmd.Wait()
}
