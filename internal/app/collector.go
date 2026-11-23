// Package app wires the data sources together on a single polling loop.
package app

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/fennih/codeguide/internal/cmux"
	"github.com/fennih/codeguide/internal/model"
	"github.com/fennih/codeguide/internal/statefile"
	"github.com/fennih/codeguide/internal/subagents"
	"github.com/fennih/codeguide/internal/summary"
	"github.com/fennih/codeguide/internal/transcript"
	"github.com/fennih/codeguide/internal/usage"
	"github.com/fennih/codeguide/internal/workstream"
)

const (
	backlogBytes   = 6 << 20
	agentsEvery    = 2 * time.Second
	sessionsEvery  = 15 * time.Second
	subagentWindow = 15 * time.Minute
	followIdle     = 10 * time.Minute

	narrateEvery = 90 * time.Second
	narrateCalls = 8

	lookedSaveEvery = 10 * time.Second
	lookedKeep      = 30 * 24 * time.Hour
)

type follow struct {
	main    *transcript.Follower
	subs    map[string]*transcript.Follower
	lastUse time.Time
}

type narration struct {
	turn  time.Time
	at    time.Time
	calls int
}

type Collector struct {
	Cmux      cmux.Client
	Tail      *workstream.Tailer
	Store     *model.Store
	Subs      *subagents.Reader
	Usage     *usage.Reader
	Sum       *summary.Summarizer // nil when summaries are disabled
	Root      string              // Claude projects directory; transcripts outside it are ignored
	Narrate   bool
	ColdStart int // sessions to summarize at startup
	ColdAge   time.Duration
	Presence  *cmux.Presence // nil when cmux events are unavailable
	SelfPanel string         // codeguide's own cmux surface, from CMUX_SURFACE_ID
	StateDir  string         // where seen.json lives; "" disables it
	Persist   bool           // write seen.json (off for --once and --frame)

	mu           sync.Mutex
	focus        string
	ledger       bool
	err          string
	lastAgents   time.Time
	lastSessions time.Time
	follows      map[string]*follow
	narr         map[string]*narration
	lookedSaved  map[string]time.Time
	lastSave     time.Time
	asked        map[string]bool // subagent ids whose mission was requested
}

// Init loads the backlog and the first cmux snapshot.
func (c *Collector) Init(ctx context.Context) error {
	c.follows = map[string]*follow{}
	c.narr = map[string]*narration{}
	c.asked = map[string]bool{}
	if err := c.Tail.Open(backlogBytes); err != nil {
		return err
	}
	recs, err := c.Tail.Poll()
	if err != nil {
		return err
	}
	c.loadLooked()
	c.Store.Apply(recs)
	if c.Sum != nil {
		for sid, s := range c.Sum.Latest() {
			c.Store.SetSummary(sid, s)
		}
		c.Store.SetHistory(c.Sum.History())
		c.Store.SetMissions(c.Sum.Missions())
	}
	now := time.Now()
	c.refreshCmux(ctx, now, true)
	c.refreshSubagents(now, c.Store.Views(now))
	c.refreshTimelines(now, c.Store.Views(now))
	c.refreshContexts(c.Store.Views(now))
	return nil
}

// SetFocus names the session open in the detail view ("" for none). It is
// called from the UI goroutine.
func (c *Collector) SetFocus(sessionID string) {
	c.mu.Lock()
	c.focus = sessionID
	c.mu.Unlock()
}

// SetLedger tells the collector the external-effects view is open, so it
// follows the transcripts of open sessions that have effects.
func (c *Collector) SetLedger(open bool) {
	c.mu.Lock()
	c.ledger = open
	c.mu.Unlock()
}

func (c *Collector) LedgerOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ledger
}

func (c *Collector) focused() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.focus
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
			// Summaries cached before the brief field existed are redone once.
			if v.Summary.Goal != "" && v.Summary.Brief == "" {
				c.Sum.Forget(j.RecordID)
			}
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
	c.markLooked(now)
	c.refreshSubagents(now, c.Store.Views(now))
	views := c.Store.Views(now)
	c.refreshTimelines(now, views)
	c.refreshContexts(views)
	if c.Sum == nil {
		return
	}
	c.askMissions(now, views)
	if c.Narrate {
		c.narrate(now, c.Store.Views(now))
	}
	if len(events) == 0 {
		return
	}
	byID := map[string]model.View{}
	for _, v := range views {
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

// Look records that the user just looked at a session from codeguide.
func (c *Collector) Look(sessionID string) {
	c.Store.Look(sessionID, time.Now())
}

// markLooked credits the session in front in cmux, or the one open in the
// detail view while codeguide itself is in front, then persists now and then.
func (c *Collector) markLooked(now time.Time) {
	focus := c.focused()
	if c.Presence != nil {
		if surface, _, ok := c.Presence.Front(); ok {
			switch {
			case c.SelfPanel != "" && surface == c.SelfPanel:
				if focus != "" {
					c.Store.Look(focus, now)
				}
			default:
				if sid := c.Store.SessionForPanel(surface); sid != "" {
					c.Store.Look(sid, now)
				}
			}
		}
	} else if focus != "" {
		c.Store.Look(focus, now)
	}
	if c.Persist && c.StateDir != "" && now.Sub(c.lastSave) >= lookedSaveEvery {
		c.lastSave = now
		c.saveLooked(now)
	}
}

func (c *Collector) lookedPath() string { return filepath.Join(c.StateDir, "seen.json") }

func (c *Collector) loadLooked() {
	if c.StateDir == "" {
		return
	}
	m := map[string]time.Time{}
	if err := statefile.Read(c.lookedPath(), &m); err != nil {
		return
	}
	for sid, t := range m {
		if cmux.ValidID(sid) {
			c.Store.Look(sid, t)
		}
	}
	c.lookedSaved = m
}

func (c *Collector) saveLooked(now time.Time) {
	m := c.Store.Looked()
	for sid, t := range m {
		if now.Sub(t) > lookedKeep {
			delete(m, sid)
		}
	}
	if sameTimes(m, c.lookedSaved) {
		return
	}
	if err := statefile.Write(c.lookedPath(), m); err == nil {
		c.lookedSaved = m
	}
}

// Flush persists state before exit.
func (c *Collector) Flush() {
	if c.Persist && c.StateDir != "" {
		c.saveLooked(time.Now())
	}
}

func sameTimes(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !b[k].Equal(v) {
			return false
		}
	}
	return true
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
func (c *Collector) refreshSubagents(now time.Time, views []model.View) {
	focus := c.focused()
	for _, v := range views {
		if v.SessionID != focus && (v.Lane == model.LaneStale || now.Sub(v.LastAt) > subagentWindow && v.Lane != model.LaneWorking) {
			continue
		}
		c.Store.SetSubagents(v.SessionID, c.Subs.List(v.Transcript, v.Cwd, now))
	}
}

// refreshContexts reads how full each open session's context window is. Only
// transcripts that changed since the last tick are read again.
func (c *Collector) refreshContexts(views []model.View) {
	if c.Usage == nil {
		return
	}
	keep := map[string]bool{}
	for _, v := range views {
		if v.Transcript == "" {
			continue
		}
		keep[filepath.Clean(v.Transcript)] = true
		c.Store.SetContext(v.SessionID, c.Usage.Session(v.Transcript))
	}
	c.Usage.Forget(keep)
}

// refreshTimelines follows transcripts of working sessions, sessions that
// recently stopped to wait on the user, and the session open in detail
// (including its subagents). Followers unused for a while are dropped.
func (c *Collector) refreshTimelines(now time.Time, views []model.View) {
	if c.Root == "" {
		return
	}
	focus := c.focused()
	var withEffects map[string]bool
	if c.LedgerOpen() {
		withEffects = c.Store.EffectSessions()
	}
	for _, v := range views {
		isFocus := v.SessionID == focus
		want := isFocus || v.Lane == model.LaneWorking || withEffects[v.SessionID] ||
			(v.Lane == model.LaneNeedsYou && now.Sub(v.LastAt) < subagentWindow)
		if !want || v.Transcript == "" {
			continue
		}
		fl := c.follows[v.SessionID]
		if fl == nil {
			f, err := transcript.Open(c.Root, v.Transcript, v.Cwd)
			if err != nil {
				continue
			}
			fl = &follow{main: f, subs: map[string]*transcript.Follower{}}
			c.follows[v.SessionID] = fl
		}
		fl.lastUse = now
		if changed, err := fl.main.Poll(); err == nil && changed {
			c.Store.SetTimeline(v.SessionID, fl.main.Events())
		}
		if !isFocus {
			continue
		}
		for _, sa := range v.Subagents {
			if sa.Transcript == "" {
				continue
			}
			sf := fl.subs[sa.ID]
			if sf == nil {
				f, err := transcript.Open(c.Root, sa.Transcript, v.Cwd)
				if err != nil {
					continue
				}
				sf = f
				fl.subs[sa.ID] = sf
			}
			if changed, err := sf.Poll(); err == nil && changed {
				c.Store.SetSubTimeline(v.SessionID, sa.ID, sf.Events())
			}
		}
	}
	for sid, fl := range c.follows {
		if now.Sub(fl.lastUse) > followIdle {
			delete(c.follows, sid)
			c.Store.SetTimeline(sid, nil)
		}
	}
}

// narrate asks for a live one-liner once a working turn has run for a while
// and made enough new calls since the last one.
func (c *Collector) narrate(now time.Time, views []model.View) {
	for _, v := range views {
		if v.Lane != model.LaneWorking || len(v.Timeline) == 0 {
			delete(c.narr, v.SessionID)
			continue
		}
		turn, tools := currentTurn(v.Timeline)
		if turn.IsZero() || len(tools) == 0 {
			continue
		}
		n := c.narr[v.SessionID]
		if n == nil || !n.turn.Equal(turn) {
			n = &narration{turn: turn, at: turn}
			c.narr[v.SessionID] = n
		}
		if now.Sub(n.at) < narrateEvery || len(tools)-n.calls < narrateCalls {
			continue
		}
		n.at, n.calls = now, len(tools)
		last := tools[len(tools)-1]
		j := summary.Job{
			SessionID: v.SessionID, Mode: summary.ModeNow,
			RecordID: "now:" + v.SessionID + ":" + last.ID + ":" + strconv.FormatInt(now.Unix(), 10),
			Title:    v.Title, PrevGoal: v.Goal(), Prompt: v.Prompt,
		}
		var notes []string
		for _, e := range v.Timeline {
			if e.Kind == transcript.Say && !e.At.Before(turn) {
				notes = append(notes, e.Text)
			}
		}
		if len(notes) > 3 {
			notes = notes[len(notes)-3:]
		}
		j.Notes = notes
		if len(tools) > 15 {
			tools = tools[len(tools)-15:]
		}
		for _, e := range tools {
			j.Tools = append(j.Tools, describe(e))
		}
		c.Sum.Enqueue(j)
	}
}

// askMissions requests a one-sentence mission for each new subagent of a
// session that is active or open in the detail view. Cached per subagent.
func (c *Collector) askMissions(now time.Time, views []model.View) {
	focus := c.focused()
	for _, v := range views {
		if v.SessionID != focus && v.Lane != model.LaneWorking && now.Sub(v.LastAt) > subagentWindow {
			continue
		}
		for _, sa := range v.Subagents {
			if sa.Task == "" || c.asked[sa.ID] || now.Sub(sa.Started) > 24*time.Hour || c.Store.HasMission(sa.ID) {
				continue
			}
			c.asked[sa.ID] = true
			c.Sum.Enqueue(summary.Job{
				SessionID: v.SessionID, RecordID: "sub:" + sa.ID, Mode: summary.ModeSub,
				PrevGoal: v.Goal(), SubID: sa.ID, SubType: sa.Type, SubDesc: sa.Description, Task: sa.Task,
			})
		}
	}
}

// turnNotes returns the latest things the agent said in the current turn.
func turnNotes(ev []transcript.Event, n int) []string {
	var notes []string
	for _, e := range ev[transcript.TurnIndex(ev)+1:] {
		if e.Kind == transcript.Say || e.Kind == transcript.End {
			notes = append(notes, e.Text)
		}
	}
	if len(notes) > n {
		notes = notes[len(notes)-n:]
	}
	return notes
}

// currentTurn returns the start of the latest user turn and its tool calls.
// When the history starts mid-turn, the first visible event stands in.
func currentTurn(ev []transcript.Event) (time.Time, []transcript.Event) {
	if len(ev) == 0 {
		return time.Time{}, nil
	}
	start := transcript.TurnIndex(ev)
	at := ev[0].At
	if start >= 0 {
		at = ev[start].At
	}
	var tools []transcript.Event
	for _, e := range ev[start+1:] {
		if e.Kind == transcript.Tool {
			tools = append(tools, e)
		}
	}
	return at, tools
}

func describe(e transcript.Event) string {
	s := e.Verb + " " + e.Detail
	if e.Plus+e.Minus > 0 {
		s += " (+" + strconv.Itoa(e.Plus) + " -" + strconv.Itoa(e.Minus) + ")"
	}
	if e.Error {
		s += " [error: " + e.ErrLine + "]"
	}
	return s
}

// jobFor builds the summary job for a session's latest turn boundary.
func jobFor(v model.View) (summary.Job, bool) {
	j := summary.Job{
		SessionID: v.SessionID, Title: v.Title, PrevGoal: v.Summary.Goal, Prompt: v.Prompt,
		Intent: v.Preamble,
	}
	switch {
	case v.StopID != "":
		j.Mode, j.RecordID, j.Final, j.TurnEnd = summary.ModeStop, v.StopID, v.Stop, v.StopAt
	case v.PromptID != "":
		j.Mode, j.RecordID = summary.ModeStart, v.PromptID
	default:
		return j, false
	}
	if len(v.Timeline) > 0 {
		j.Notes = turnNotes(v.Timeline, 3)
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
