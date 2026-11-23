// Package model folds cmux state, Feed records, subagents and summaries
// into one view per Claude session.
package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fennih/codeguide/internal/cmux"
	"github.com/fennih/codeguide/internal/effects"
	"github.com/fennih/codeguide/internal/i18n"
	"github.com/fennih/codeguide/internal/sanitize"
	"github.com/fennih/codeguide/internal/subagents"
	"github.com/fennih/codeguide/internal/toolfmt"
	"github.com/fennih/codeguide/internal/transcript"
	"github.com/fennih/codeguide/internal/usage"
	"github.com/fennih/codeguide/internal/workstream"
)

type Lane int

const (
	LaneNeedsYou Lane = iota
	LaneWorking
	LaneIdle
	LaneStale
)

func (l Lane) String() string {
	return [...]string{"needs_you", "working", "idle", "stale"}[l]
}

const (
	maxEffects  = 3000
	effectMatch = 10 * time.Second
	maxActivity = 40
	maxMarks    = 3000
	maxHistory  = 30
)

// mark is one Feed event, kept compact to count what happened since a time.
type mark struct {
	at   time.Time
	kind byte // 'p' prompt, 'c' call, 'e' error, 's' stop
}

// News counts what happened in a session since the user last looked at it.
type News struct {
	Turns  int `json:"turns"`
	Calls  int `json:"calls"`
	Errors int `json:"errors"`
}

func (n News) Any() bool { return n.Turns+n.Calls+n.Errors > 0 }

// EffectRow is one external effect seen in the Feed, enriched from the
// session's transcript when it is followed.
type EffectRow struct {
	effects.Effect
	At        time.Time `json:"at"`
	SessionID string    `json:"sessionId"`
	Tool      string    `json:"tool"`
	Project   string    `json:"project"`
	Session   string    `json:"session"` // tab title, or goal for closed sessions
	Live      bool      `json:"live"`    // still open in cmux
	Status    byte      `json:"-"`       // 0 unknown, 'o' ok, 'e' error
	Detailed  bool      `json:"detailed"`
}

// effectRow comes from the Feed, which records no tool results: outcomes
// only come from the transcript.
type effectRow struct {
	effects.Effect
	at   time.Time
	sid  string
	tool string
	cwd  string
}

type Activity struct {
	At     time.Time `json:"at"`
	Tool   string    `json:"tool"`
	Detail string    `json:"detail"`
	Error  bool      `json:"error,omitempty"`
}

// Summary is model-written text for one session.
type Summary struct {
	Goal      string    `json:"goal,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	NeedsUser bool      `json:"needsUser,omitempty"`
	Ask       string    `json:"ask,omitempty"`
	ForRecord string    `json:"forRecord,omitempty"` // Feed record id it was made from
	Now       string    `json:"now,omitempty"`       // live narration of a running turn
	NowAt     time.Time `json:"nowAt,omitempty"`     // when Now was written
	At        time.Time `json:"at,omitempty"`        // when the summary was written
	Brief     string    `json:"brief,omitempty"`     // why, how far along, what is next
	Mission   string    `json:"mission,omitempty"`   // subagent summaries only
	SubID     string    `json:"subId,omitempty"`     // set on subagent summaries
}

// track is the per-session state built from Feed records.
type track struct {
	cwd        string
	activity   []Activity
	prompt     string
	promptID   string
	turnStart  time.Time
	turnCalls  int
	preamble   string
	stop       string
	stopID     string
	stopAt     time.Time
	lastAt     time.Time
	lastKind   string
	pending    *workstream.Record
	callsToday int
	day        int
	marks      []mark
}

func (t *track) mark(at time.Time, kind byte) {
	t.marks = append(t.marks, mark{at, kind})
	if len(t.marks) > maxMarks+500 {
		t.marks = append([]mark(nil), t.marks[len(t.marks)-maxMarks:]...)
	}
}

// View is what the TUI renders for one session.
type View struct {
	SessionID   string            `json:"sessionId"`
	Title       string            `json:"title"`
	Project     string            `json:"project"`
	Cwd         string            `json:"cwd"`
	State       string            `json:"state"`
	Lane        Lane              `json:"-"`
	LaneName    string            `json:"lane"`
	Unread      bool              `json:"unread"`
	LastAt      time.Time         `json:"lastAt"`
	TurnStart   time.Time         `json:"turnStart"`
	TurnCalls   int               `json:"turnCalls"`
	TurnEffects string            `json:"turnEffects,omitempty"` // e.g. "3 (Jira 2, git 1)"
	Prompt      string            `json:"prompt"`
	PromptID    string            `json:"-"`
	Preamble    string            `json:"preamble"`
	Stop        string            `json:"stop"`
	StopID      string            `json:"-"`
	StopAt      time.Time         `json:"-"`
	Pending     string            `json:"pending,omitempty"`
	Activity    []Activity        `json:"activity"`
	Subagents   []subagents.Agent `json:"subagents"`
	Summary     Summary           `json:"summary"`
	PanelID     string            `json:"-"`
	WorkspaceID string            `json:"-"`
	Transcript  string            `json:"-"`
	// Timeline is read from Claude's transcript, only for sessions that are
	// working, recently waiting on the user, or open in the detail view.
	Timeline  []transcript.Event            `json:"timeline,omitempty"`
	SubEvents map[string][]transcript.Event `json:"-"` // by subagent id, detail view only
	// Looked is when the user last looked at this session; zero if unknown.
	Looked  time.Time `json:"looked,omitempty"`
	News    News      `json:"news"`
	History []Summary `json:"-"` // finished turns, oldest first
	// SubMissions is a one-sentence mission per subagent id, when known.
	SubMissions map[string]string `json:"subMissions,omitempty"`
	// Context is how full the context window was at the latest request.
	Context usage.Context `json:"context"`
}

// Goal is the best available one-line goal.
func (v View) Goal() string {
	if v.Summary.Goal != "" {
		return v.Summary.Goal
	}
	if v.Prompt != "" {
		return v.Prompt
	}
	return v.Title
}

type Store struct {
	mu        sync.Mutex
	tracks    map[string]*track
	agents    []cmux.Agent
	sessions  map[string]cmux.SessionRecord
	subs      map[string][]subagents.Agent
	summaries map[string]Summary
	timelines map[string][]transcript.Event
	subEvents map[string]map[string][]transcript.Event
	looked    map[string]time.Time
	history   map[string][]Summary
	effects   []effectRow
	missions  map[string]string // subagent id -> mission
	contexts  map[string]usage.Context
	seen      map[string]struct{} // Feed record ids already applied
	staleAge  time.Duration
}

func NewStore(staleAge time.Duration) *Store {
	return &Store{
		tracks:    map[string]*track{},
		seen:      map[string]struct{}{},
		sessions:  map[string]cmux.SessionRecord{},
		subs:      map[string][]subagents.Agent{},
		summaries: map[string]Summary{},
		timelines: map[string][]transcript.Event{},
		subEvents: map[string]map[string][]transcript.Event{},
		looked:    map[string]time.Time{},
		history:   map[string][]Summary{},
		missions:  map[string]string{},
		contexts:  map[string]usage.Context{},
		staleAge:  staleAge,
	}
}

// Apply folds Feed records in log order. It returns the records that start
// or end a turn, which drive summarization.
func (s *Store) Apply(recs []workstream.Record) (turnEvents []workstream.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		t := s.tracks[r.SessionID]
		if t == nil {
			t = &track{}
			s.tracks[r.SessionID] = t
		}
		at := r.UpdatedAt
		if at.IsZero() {
			at = r.CreatedAt
		}
		if r.Cwd != "" {
			t.cwd = r.Cwd
		}
		if p := r.Context.AssistantPreamble; p != "" {
			t.preamble = p
		}
		// cmux re-appends a record with the same id when its status changes
		// (pending -> expired/answered); only the status matters then.
		if _, dup := s.seen[r.ID]; dup {
			if t.pending != nil && t.pending.ID == r.ID && !r.Pending() {
				t.pending = nil
			}
			continue
		}
		if len(s.seen) > 50000 {
			s.seen = map[string]struct{}{}
		}
		s.seen[r.ID] = struct{}{}
		if at.After(t.lastAt) {
			t.lastAt = at
		}
		switch r.Kind {
		case "userPrompt":
			if r.Payload.UserPrompt == nil {
				continue
			}
			// Background-task notifications start a turn but are not the user's goal.
			if p := toolfmt.CleanPrompt(r.Payload.UserPrompt.Text); p != "" {
				t.prompt = p
				t.promptID = r.ID
				// Typing a prompt means the user was looking at the session.
				s.lookLocked(r.SessionID, r.CreatedAt)
				t.mark(r.CreatedAt, 'p')
			}
			t.turnStart = r.CreatedAt
			t.turnCalls = 0
			t.stop, t.stopID = "", ""
			t.pending = nil
			turnEvents = append(turnEvents, r)
		case "toolUse":
			if r.Payload.ToolUse == nil {
				continue
			}
			tu := r.Payload.ToolUse
			detail := r.Context.ToolSummary
			if detail == "" || tu.ToolName == "Read" || tu.ToolName == "Edit" || tu.ToolName == "Write" {
				if d := toolfmt.DetailJSON(tu.ToolName, tu.ToolInputJSON, t.cwd); d != "" {
					detail = d
				}
			}
			t.activity = append(t.activity, Activity{At: r.CreatedAt, Tool: toolfmt.ShortTool(tu.ToolName), Detail: detail})
			for _, e := range effects.ClassifyJSON(tu.ToolName, tu.ToolInputJSON) {
				e.Target = sanitize.Line(sanitize.Redact(e.Target), 200)
				s.effects = append(s.effects, effectRow{Effect: e, at: r.CreatedAt, sid: r.SessionID, tool: tu.ToolName, cwd: t.cwd})
			}
			if len(s.effects) > maxEffects+500 {
				s.effects = append([]effectRow(nil), s.effects[len(s.effects)-maxEffects:]...)
			}
			if len(t.activity) > maxActivity {
				t.activity = t.activity[len(t.activity)-maxActivity:]
			}
			t.turnCalls++
			t.mark(r.CreatedAt, 'c')
			if d := r.CreatedAt.YearDay(); d != t.day {
				t.day, t.callsToday = d, 0
			}
			t.callsToday++
			t.pending = nil
		case "toolResult":
			if r.Payload.ToolResult != nil && r.Payload.ToolResult.IsError {
				t.mark(r.CreatedAt, 'e')
			}
			if r.Payload.ToolResult != nil && r.Payload.ToolResult.IsError && len(t.activity) > 0 {
				// Results carry no tool-use id; mark the latest call of that tool.
				for i := len(t.activity) - 1; i >= 0; i-- {
					if t.activity[i].Tool == toolfmt.ShortTool(r.Payload.ToolResult.ToolName) {
						t.activity[i].Error = true
						break
					}
				}
			}
			t.pending = nil
		case "stop":
			if r.Payload.Stop != nil {
				t.stop = r.Payload.Stop.Reason
			}
			t.stopID, t.stopAt = r.ID, r.CreatedAt
			t.pending = nil
			t.mark(r.CreatedAt, 's')
			turnEvents = append(turnEvents, r)
		case "question", "exitPlan", "permissionRequest":
			if r.Pending() {
				rc := r
				t.pending = &rc
			}
		case "sessionEnd":
			t.pending = nil
		}
		t.lastKind = r.Kind
	}
	return turnEvents
}

func (s *Store) SetAgents(a []cmux.Agent) {
	s.mu.Lock()
	s.agents = a
	s.mu.Unlock()
}

func (s *Store) SetSessions(m map[string]cmux.SessionRecord) {
	s.mu.Lock()
	s.sessions = m
	s.mu.Unlock()
}

func (s *Store) SetSubagents(sessionID string, a []subagents.Agent) {
	s.mu.Lock()
	s.subs[sessionID] = a
	s.mu.Unlock()
}

// SetSummary stores a goal/outcome summary, or only the live narration when
// sum carries nothing but Now.
func (s *Store) SetSummary(sessionID string, sum Summary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sum.SubID != "" {
		s.missions[sum.SubID] = sum.Mission
		return
	}
	cur := s.summaries[sessionID]
	if sum.Now != "" && sum.Goal == "" && sum.Outcome == "" {
		cur.Now, cur.NowAt = sum.Now, sum.NowAt
		s.summaries[sessionID] = cur
		return
	}
	if sum.Goal == "" {
		sum.Goal = cur.Goal
	}
	sum.Now, sum.NowAt = cur.Now, cur.NowAt
	s.summaries[sessionID] = sum
	if sum.ForRecord != "" {
		s.addHistoryLocked(sessionID, sum)
	}
}

// Effects returns external effects, newest first. Rows of sessions whose
// transcript is followed get the full target and outcome from it.
func (s *Store) Effects() []EffectRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := map[string]cmux.Agent{}
	for _, a := range s.agents {
		live[a.SessionID] = a
	}
	used := map[string]map[int]bool{}
	out := make([]EffectRow, 0, len(s.effects))
	for i := len(s.effects) - 1; i >= 0; i-- {
		e := s.effects[i]
		row := EffectRow{Effect: e.Effect, At: e.at, SessionID: e.sid, Tool: e.tool, Project: toolfmt.Project(e.cwd)}
		if a, ok := live[e.sid]; ok {
			row.Live, row.Session = true, sanitize.Line(sanitize.Redact(a.Label), 120)
		} else if sum, ok := s.summaries[e.sid]; ok && sum.Goal != "" {
			row.Session = sanitize.Line(sum.Goal, 120)
		}
		if tl := s.timelines[e.sid]; len(tl) > 0 {
			if used[e.sid] == nil {
				used[e.sid] = map[int]bool{}
			}
			if j := matchEvent(tl, e.tool, e.at, used[e.sid]); j >= 0 {
				used[e.sid][j] = true
				ev := tl[j]
				row.Detailed = true
				if ev.Target != "" {
					row.Target = ev.Target
				}
				switch {
				case ev.Error:
					row.Status = 'e'
				case ev.Done:
					row.Status = 'o'
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// matchEvent finds the unused transcript call of the same tool closest in
// time to a Feed record, within effectMatch.
func matchEvent(tl []transcript.Event, tool string, at time.Time, used map[int]bool) int {
	best, bestD := -1, effectMatch+1
	for j, ev := range tl {
		if ev.Kind != transcript.Tool || ev.Tool != tool || used[j] {
			continue
		}
		d := ev.At.Sub(at)
		if d < 0 {
			d = -d
		}
		if d <= effectMatch && d < bestD {
			best, bestD = j, d
		}
	}
	return best
}

// EffectSessions lists the sessions that have external effects on record.
func (s *Store) EffectSessions() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for _, e := range s.effects {
		out[e.sid] = true
	}
	return out
}

func turnEffects(rows []effectRow, sid string, since time.Time) string {
	if since.IsZero() {
		return ""
	}
	by := map[string]int{}
	var order []string
	n := 0
	for i := len(rows) - 1; i >= 0 && !rows[i].at.Before(since.Add(-time.Minute)); i-- {
		e := rows[i]
		if e.sid != sid || e.at.Before(since) {
			continue
		}
		if by[e.Sys] == 0 {
			order = append(order, e.Sys)
		}
		by[e.Sys]++
		n++
	}
	if n == 0 {
		return ""
	}
	parts := make([]string, len(order))
	for i := range order {
		k := order[len(order)-1-i]
		parts[i] = fmt.Sprintf("%s %d", k, by[k])
	}
	return fmt.Sprintf("%d (%s)", n, strings.Join(parts, ", "))
}

// SetMissions loads cached subagent missions.
func (s *Store) SetMissions(m map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range m {
		s.missions[k] = v
	}
}

// HasMission reports whether a subagent already has a mission.
func (s *Store) HasMission(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.missions[agentID]
	return ok
}

// SetHistory loads finished-turn summaries, oldest first.
func (s *Store) SetHistory(h map[string][]Summary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sid, list := range h {
		for _, sum := range list {
			if sum.ForRecord != "" {
				s.addHistoryLocked(sid, sum)
			}
		}
	}
}

func (s *Store) addHistoryLocked(sid string, sum Summary) {
	h := s.history[sid]
	for i, e := range h {
		if e.ForRecord == sum.ForRecord {
			if e.At.IsZero() {
				h[i].At = sum.At
			}
			return
		}
	}
	h = append(h, sum)
	sort.SliceStable(h, func(i, j int) bool { return h[i].At.Before(h[j].At) })
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	s.history[sid] = h
}

// Look records that the user looked at a session at t; earlier times are ignored.
func (s *Store) Look(sessionID string, t time.Time) {
	s.mu.Lock()
	s.lookLocked(sessionID, t)
	s.mu.Unlock()
}

func (s *Store) lookLocked(sid string, t time.Time) {
	if t.After(s.looked[sid]) {
		s.looked[sid] = t
	}
}

// Looked returns a copy of the last-look times, for persistence.
func (s *Store) Looked() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.looked))
	for k, v := range s.looked {
		out[k] = v
	}
	return out
}

// SessionForPanel maps a cmux surface id to the Claude session it hosts.
func (s *Store) SessionForPanel(panel string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		if strings.EqualFold(a.PanelID, panel) {
			return a.SessionID
		}
	}
	return ""
}

// SetTimeline replaces a session's transcript timeline; nil drops it.
func (s *Store) SetTimeline(sessionID string, ev []transcript.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev == nil {
		delete(s.timelines, sessionID)
		delete(s.subEvents, sessionID)
		return
	}
	s.timelines[sessionID] = ev
}

// SetSubTimeline replaces one subagent's timeline. The inner map is copied so
// views already handed out are never mutated.
func (s *Store) SetSubTimeline(sessionID, agentID string, ev []transcript.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string][]transcript.Event, len(s.subEvents[sessionID])+1)
	for k, v := range s.subEvents[sessionID] {
		next[k] = v
	}
	next[agentID] = ev
	s.subEvents[sessionID] = next
}

// SetContext stores how full a session's context window is.
func (s *Store) SetContext(sessionID string, c usage.Context) {
	s.mu.Lock()
	s.contexts[sessionID] = c
	s.mu.Unlock()
}

// TranscriptFor returns cmux's transcript path for a session.
func (s *Store) TranscriptFor(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessionID].TranscriptPath
}

// CallsToday sums tool calls seen today across sessions.
func (s *Store) CallsToday(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.tracks {
		if t.day == now.YearDay() {
			n += t.callsToday
		}
	}
	return n
}

// Views builds one view per live cmux Claude session, sorted by lane then recency.
func (s *Store) Views(now time.Time) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]View, 0, len(s.agents))
	for _, a := range s.agents {
		v := View{
			SessionID: a.SessionID, Title: sanitize.Line(sanitize.Redact(a.Label), 120), State: a.State,
			Unread: a.Unread, LastAt: a.LastActivity, Cwd: a.Cwd,
			PanelID: a.PanelID, WorkspaceID: a.WorkspaceID,
		}
		if rec, ok := s.sessions[a.SessionID]; ok {
			v.Transcript = rec.TranscriptPath
			if v.Cwd == "" {
				v.Cwd = rec.Cwd
			}
		}
		if t := s.tracks[a.SessionID]; t != nil {
			if v.Cwd == "" {
				v.Cwd = t.cwd
			}
			if t.lastAt.After(v.LastAt) {
				v.LastAt = t.lastAt
			}
			v.TurnStart, v.TurnCalls = t.turnStart, t.turnCalls
			v.Prompt = sanitize.Line(t.prompt, 1500)
			v.PromptID = t.promptID
			v.Preamble = sanitize.Line(t.preamble, 300)
			v.Stop = sanitize.Line(t.stop, 3000)
			v.StopID, v.StopAt = t.stopID, t.stopAt
			v.Activity = make([]Activity, len(t.activity))
			for i, ac := range t.activity {
				ac.Detail = sanitize.Line(ac.Detail, 200)
				ac.Tool = sanitize.Line(ac.Tool, 40)
				v.Activity[i] = ac
			}
			if t.pending != nil {
				v.Pending = pendingText(t.pending, v.Cwd)
			}
		}
		for _, sa := range s.subs[a.SessionID] {
			sa.Description = sanitize.Line(sa.Description, 120)
			sa.Type = sanitize.Line(sa.Type, 40)
			sa.Tool = sanitize.Line(sa.Tool, 40)
			sa.Detail = sanitize.Line(sa.Detail, 160)
			sa.Task = sanitize.Line(sa.Task, 4000)
			sa.Result = sanitize.Line(sa.Result, 4000)
			if m, ok := s.missions[sa.ID]; ok && m != "" {
				if v.SubMissions == nil {
					v.SubMissions = map[string]string{}
				}
				v.SubMissions[sa.ID] = sanitize.Line(m, 160)
			}
			v.Subagents = append(v.Subagents, sa)
		}
		if sum, ok := s.summaries[a.SessionID]; ok {
			sum.Goal = sanitize.Line(sum.Goal, 120)
			sum.Outcome = sanitize.Line(sum.Outcome, 160)
			sum.Ask = sanitize.Line(sum.Ask, 160)
			// An outcome belongs to the last stop; drop it once a new turn starts.
			if sum.ForRecord != "" && sum.ForRecord != v.StopID {
				sum.Outcome, sum.NeedsUser, sum.Ask = "", false, ""
			}
			if sum.NowAt.Before(v.TurnStart) {
				sum.Now = ""
			}
			sum.Now = sanitize.Line(sum.Now, 160)
			sum.Brief = sanitize.Line(sum.Brief, 400)
			v.Summary = sum
		}
		v.TurnEffects = turnEffects(s.effects, a.SessionID, v.TurnStart)
		v.Timeline = s.timelines[a.SessionID]
		v.SubEvents = s.subEvents[a.SessionID]
		v.History = s.history[a.SessionID]
		v.Context = s.contexts[a.SessionID]
		if lk := s.looked[a.SessionID]; !lk.IsZero() {
			v.Looked = lk
			if t := s.tracks[a.SessionID]; t != nil {
				v.News = newsSince(t.marks, lk)
			}
		}
		v.Project = toolfmt.Project(v.Cwd)
		v.Lane = s.lane(v, now)
		v.LaneName = v.Lane.String()
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Lane != views[j].Lane {
			return views[i].Lane < views[j].Lane
		}
		return views[i].LastAt.After(views[j].LastAt)
	})
	return views
}

func newsSince(marks []mark, since time.Time) News {
	var n News
	for i := len(marks) - 1; i >= 0 && marks[i].at.After(since); i-- {
		switch marks[i].kind {
		case 'c':
			n.Calls++
		case 'e':
			n.Errors++
		case 's':
			n.Turns++
		}
	}
	return n
}

func (s *Store) lane(v View, now time.Time) Lane {
	switch {
	case v.Pending != "" || v.State == "needs_input":
		return LaneNeedsYou
	case v.State == "working":
		return LaneWorking
	case v.Summary.NeedsUser && v.Stop != "":
		return LaneNeedsYou
	case now.Sub(v.LastAt) > s.staleAge:
		return LaneStale
	}
	return LaneIdle
}

func pendingText(r *workstream.Record, cwd string) string {
	switch r.Kind {
	case "permissionRequest":
		if p := r.Payload.PermissionRequest; p != nil {
			d := r.Context.ToolSummary
			if d == "" {
				d = toolfmt.DetailJSON(p.ToolName, p.ToolInputJSON, cwd)
			}
			return sanitize.Line(i18n.T("permission: ")+toolfmt.ShortTool(p.ToolName)+" "+d, 200)
		}
	case "exitPlan":
		s := r.Context.PlanSummary
		if s == "" {
			s = i18n.T("plan ready for approval")
		}
		return sanitize.Line(i18n.T("plan: ")+s, 200)
	case "question":
		var q struct {
			Questions []struct {
				Question string `json:"question"`
			} `json:"questions"`
		}
		if len(r.Payload.Question) > 0 {
			_ = json.Unmarshal(r.Payload.Question, &q)
		}
		if len(q.Questions) > 0 {
			return sanitize.Line(i18n.T("question: ")+q.Questions[0].Question, 200)
		}
		return i18n.T("question pending")
	}
	return strings.TrimSpace(r.Kind)
}
