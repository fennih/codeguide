// Package app wires the data sources together on a single polling loop.
package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"ccwatch/internal/cmux"
	"ccwatch/internal/model"
	"ccwatch/internal/subagents"
	"ccwatch/internal/summary"
	"ccwatch/internal/workstream"
)

const (
	backlogBytes   = 6 << 20
	agentsEvery    = 2 * time.Second
	sessionsEvery  = 15 * time.Second
	subagentWindow = 15 * time.Minute
)

type Collector struct {
	Cmux      cmux.Client
	Tail      *workstream.Tailer
	Store     *model.Store
	Subs      *subagents.Reader
	Sum       *summary.Summarizer // nil when summaries are disabled
	ColdStart int                 // sessions to summarize at startup
	ColdAge   time.Duration

	mu           sync.Mutex
	lastAgents   time.Time
	lastSessions time.Time
	err          string
}

// Init loads the backlog and the first cmux snapshot.
func (c *Collector) Init(ctx context.Context) error {
	if err := c.Tail.Open(backlogBytes); err != nil {
		return err
	}
	recs, err := c.Tail.Poll()
	if err != nil {
		return err
	}
	c.Store.Apply(recs)
	if c.Sum != nil {
		for sid, s := range c.Sum.Latest() {
			c.Store.SetSummary(sid, s)
		}
	}
	now := time.Now()
	c.refreshCmux(ctx, now, true)
	c.refreshSubagents(now)
	return nil
}

// WarmUp summarizes the most recent sessions that have no cached summary.
func (c *Collector) WarmUp() {
	if c.Sum == nil || c.ColdStart <= 0 {
		return
	}
	now := time.Now()
	views := c.Store.Views(now)
	sort.SliceStable(views, func(i, j int) bool { return views[i].LastAt.After(views[j].LastAt) })
	n := 0
	for _, v := range views {
		if n >= c.ColdStart || now.Sub(v.LastAt) > c.ColdAge {
			break
		}
		if j, ok := jobFor(v); ok {
			c.Sum.Enqueue(j)
			n++
		}
	}
}

// Tick polls every source that is due.
func (c *Collector) Tick(ctx context.Context) {
	now := time.Now()
	recs, err := c.Tail.Poll()
	if err != nil {
		c.setErr("workstream: " + err.Error())
	}
	events := c.Store.Apply(recs)
	c.refreshCmux(ctx, now, false)
	c.refreshSubagents(now)
	if c.Sum == nil || len(events) == 0 {
		return
	}
	byID := map[string]model.View{}
	for _, v := range c.Store.Views(now) {
		byID[v.SessionID] = v
	}
	for _, e := range events {
		v, ok := byID[e.SessionID]
		if !ok {
			continue
		}
		if j, ok := jobFor(v); ok && j.RecordID == e.ID {
			c.Sum.Enqueue(j)
		}
	}
}

// Resummarize regenerates the latest summary of one session.
func (c *Collector) Resummarize(sessionID string) {
	if c.Sum == nil {
		return
	}
	for _, v := range c.Store.Views(time.Now()) {
		if v.SessionID != sessionID {
			continue
		}
		if j, ok := jobFor(v); ok {
			c.Sum.Forget(j.RecordID)
			c.Sum.Enqueue(j)
		}
	}
}

func (c *Collector) Err() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Collector) setErr(s string) {
	c.mu.Lock()
	c.err = s
	c.mu.Unlock()
}

func (c *Collector) refreshCmux(ctx context.Context, now time.Time, force bool) {
	if force || now.Sub(c.lastAgents) >= agentsEvery {
		c.lastAgents = now
		agents, err := c.Cmux.Agents(ctx)
		if err != nil {
			c.setErr(err.Error())
		} else {
			c.Store.SetAgents(agents)
			c.setErr("")
		}
	}
	if force || now.Sub(c.lastSessions) >= sessionsEvery {
		c.lastSessions = now
		if m, err := c.Cmux.Sessions(ctx); err == nil {
			c.Store.SetSessions(m)
		}
	}
}

// refreshSubagents only reads sessions that were active recently.
func (c *Collector) refreshSubagents(now time.Time) {
	for _, v := range c.Store.Views(now) {
		if v.Lane == model.LaneStale || now.Sub(v.LastAt) > subagentWindow && v.Lane != model.LaneWorking {
			continue
		}
		c.Store.SetSubagents(v.SessionID, c.Subs.List(v.Transcript, v.Cwd, now))
	}
}

// jobFor builds the summary job for a session's latest turn boundary.
func jobFor(v model.View) (summary.Job, bool) {
	j := summary.Job{
		SessionID: v.SessionID, Title: v.Title, PrevGoal: v.Summary.Goal, Prompt: v.Prompt,
		Intent: v.Preamble,
	}
	switch {
	case v.StopID != "":
		j.Mode, j.RecordID, j.Final = summary.ModeStop, v.StopID, v.Stop
	case v.PromptID != "":
		j.Mode, j.RecordID = summary.ModeStart, v.PromptID
	default:
		return j, false
	}
	// Recent calls give context when the prompt itself is a terse follow-up.
	for _, a := range v.Activity {
		j.Tools = append(j.Tools, a.Tool+" "+a.Detail)
	}
	if len(j.Tools) > 15 {
		j.Tools = j.Tools[len(j.Tools)-15:]
	}
	return j, true
}
