// Package model folds cmux state, Feed records, subagents and summaries
// into one view per Claude session.
package model

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"ccwatch/internal/cmux"
	"ccwatch/internal/sanitize"
	"ccwatch/internal/subagents"
	"ccwatch/internal/toolfmt"
	"ccwatch/internal/transcript"
	"ccwatch/internal/workstream"
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

const maxActivity = 40

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
	Prompt      string            `json:"prompt"`
	PromptID    string            `json:"-"`
	Preamble    string            `json:"preamble"`
	Stop        string            `json:"stop"`
	StopID      string            `json:"-"`
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
	seen      map[string]struct{}
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
			if len(t.activity) > maxActivity {
				t.activity = t.activity[len(t.activity)-maxActivity:]
			}
			t.turnCalls++
			if d := r.CreatedAt.YearDay(); d != t.day {
				t.day, t.callsToday = d, 0
			}
			t.callsToday++
			t.pending = nil
		case "toolResult":
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
			SessionID: a.SessionID, Title: sanitize.Line(a.Label, 120), State: a.State,
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
			v.StopID = t.stopID
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
			v.Summary = sum
		}
		v.Timeline = s.timelines[a.SessionID]
		v.SubEvents = s.subEvents[a.SessionID]
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
			return sanitize.Line("permesso: "+toolfmt.ShortTool(p.ToolName)+" "+d, 200)
		}
	case "exitPlan":
		s := r.Context.PlanSummary
		if s == "" {
			s = "piano pronto da approvare"
		}
		return sanitize.Line("piano: "+s, 200)
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
			return sanitize.Line("domanda: "+q.Questions[0].Question, 200)
		}
		return "domanda in attesa"
	}
	return strings.TrimSpace(r.Kind)
}
