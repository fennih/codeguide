// Package cmux reads cmux's own view of terminals and agent sessions and
// focuses a tab. It never sends input to a terminal.
package cmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

type Client struct {
	Bin string
}

// Agent is one coding-agent session hosted by a cmux terminal surface.
type Agent struct {
	SessionID    string
	State        string // idle, working, needs_input, ended, unknown
	LastActivity time.Time
	Label        string // tab title
	Cwd          string
	PanelID      string
	WorkspaceID  string
	Unread       bool
}

// SessionRecord comes from cmux's saved hook records.
type SessionRecord struct {
	SessionID      string
	TranscriptPath string
	SurfaceID      string
	WorkspaceID    string
	Cwd            string
	RuntimeStatus  string
}

var uuidRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func ValidID(s string) bool { return uuidRe.MatchString(s) }

func (c Client) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Bin is resolved once at startup from the --cmux-bin flag or PATH; args are
	// a fixed verb set plus UUIDs validated by ValidID. No shell is involved.
	out, err := exec.CommandContext(ctx, c.Bin, args...).Output() // nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("cmux %s: %w: %.200s", args[0], err, ee.Stderr)
		}
		return nil, fmt.Errorf("cmux %s: %w", args[0], err)
	}
	return out, nil
}

type currentResp struct {
	Items []struct {
		Label     string `json:"label"`
		Cwd       string `json:"cwd"`
		Lifecycle string `json:"lifecycle"`
		Attention []struct {
			Kind string `json:"kind"`
		} `json:"attention"`
		Projections []struct {
			PanelID     string `json:"panel_id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"projections"`
		Agents []struct {
			Kind         string `json:"kind"`
			SessionID    string `json:"session_id"`
			State        string `json:"state"`
			LastActivity string `json:"last_activity_at"`
		} `json:"agents"`
	} `json:"items"`
}

// Agents returns one entry per Claude session visible in a live cmux surface.
func (c Client) Agents(ctx context.Context) ([]Agent, error) {
	out, err := c.run(ctx, 10*time.Second, "current", "--json", "--limit", "200")
	if err != nil {
		return nil, err
	}
	var resp currentResp
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("cmux current: %w", err)
	}
	seen := map[string]int{}
	var agents []Agent
	for _, it := range resp.Items {
		if it.Lifecycle != "" && it.Lifecycle != "running" {
			continue
		}
		var panel, ws string
		if len(it.Projections) > 0 {
			panel, ws = it.Projections[0].PanelID, it.Projections[0].WorkspaceID
		}
		unread := false
		for _, a := range it.Attention {
			if a.Kind == "unread" {
				unread = true
			}
		}
		// Agents are most recent first; the first Claude one owns the tab.
		for _, a := range it.Agents {
			if a.Kind != "claude" || !ValidID(a.SessionID) {
				continue
			}
			t, _ := time.Parse(time.RFC3339, a.LastActivity)
			ag := Agent{
				SessionID: a.SessionID, State: a.State, LastActivity: t,
				Label: it.Label, Cwd: it.Cwd, PanelID: panel, WorkspaceID: ws, Unread: unread,
			}
			if i, ok := seen[a.SessionID]; ok {
				if t.After(agents[i].LastActivity) {
					agents[i] = ag
				}
			} else {
				seen[a.SessionID] = len(agents)
				agents = append(agents, ag)
			}
			break
		}
	}
	return agents, nil
}

type sessionsResp struct {
	Sessions []struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		SurfaceID      string `json:"surface_id"`
		WorkspaceID    string `json:"workspace_id"`
		Cwd            string `json:"cwd"`
		RuntimeStatus  string `json:"runtime_status"`
	} `json:"sessions"`
}

// Sessions returns cmux's saved Claude hook records keyed by session id.
func (c Client) Sessions(ctx context.Context) (map[string]SessionRecord, error) {
	out, err := c.run(ctx, 10*time.Second, "sessions", "list", "--agent", "claude", "--json", "--limit", "1000")
	if err != nil {
		return nil, err
	}
	var resp sessionsResp
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("cmux sessions: %w", err)
	}
	m := make(map[string]SessionRecord, len(resp.Sessions))
	for _, s := range resp.Sessions {
		if !ValidID(s.SessionID) {
			continue
		}
		m[s.SessionID] = SessionRecord{
			SessionID: s.SessionID, TranscriptPath: s.TranscriptPath, SurfaceID: s.SurfaceID,
			WorkspaceID: s.WorkspaceID, Cwd: s.Cwd, RuntimeStatus: s.RuntimeStatus,
		}
	}
	return m, nil
}

// Focus brings the given tab to the front.
func (c Client) Focus(ctx context.Context, workspaceID, panelID string) error {
	if !ValidID(workspaceID) || !ValidID(panelID) {
		return errors.New("invalid cmux id")
	}
	if _, err := c.run(ctx, 5*time.Second, "workspace", "select", "--workspace", workspaceID); err != nil {
		return err
	}
	_, err := c.run(ctx, 5*time.Second, "focus-panel", "--panel", panelID, "--workspace", workspaceID)
	return err
}
