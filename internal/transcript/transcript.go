// Package transcript follows one Claude Code transcript file and turns it
// into a readable timeline. The format is internal to Claude Code and may
// change; lines that do not parse are skipped. All text is sanitized and
// redacted here, so events are safe to render as they are.
package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"ccwatch/internal/effects"
	"ccwatch/internal/sanitize"
	"ccwatch/internal/toolfmt"
)

const (
	tailBytes = 3 << 20  // history read when a file is first opened
	maxRead   = 16 << 20 // bytes read per poll
	maxLine   = 4 << 20
	maxEvents = 500
	maxDiff   = 40
)

type Kind uint8

const (
	Prompt Kind = iota + 1
	Say
	Tool
	End
	Interrupt
	Compact
)

type Event struct {
	At      time.Time     `json:"at"`
	Kind    Kind          `json:"kind"`
	Text    string        `json:"text,omitempty"`
	Tool    string        `json:"tool,omitempty"`
	Verb    string        `json:"verb,omitempty"`
	Detail  string        `json:"detail,omitempty"`
	ID      string        `json:"id,omitempty"`
	Done    bool          `json:"done,omitempty"`
	Error   bool          `json:"error,omitempty"`
	ErrLine string        `json:"errLine,omitempty"`
	Dur     time.Duration `json:"dur,omitempty"`
	Plus    int           `json:"plus,omitempty"`
	Minus   int           `json:"minus,omitempty"`
	Diff    []string      `json:"diff,omitempty"` // "+ text" / "- text"
	// External effect of a tool call, if any (see package effects).
	Sys    string `json:"sys,omitempty"`
	Act    string `json:"act,omitempty"`
	Target string `json:"target,omitempty"`
}

type Follower struct {
	path     string
	cwd      string
	off      int64
	ino      uint64
	partial  []byte
	skipping bool // inside a line that is too long or cut by the initial seek
	events   []Event
	byID     map[string]int
	ver      int
}

// TurnIndex returns the index of the latest user prompt, or -1 when the
// visible history starts mid-turn.
func TurnIndex(ev []Event) int {
	for i := len(ev) - 1; i >= 0; i-- {
		if ev[i].Kind == Prompt {
			return i
		}
	}
	return -1
}

// Open accepts only .jsonl files under root.
func Open(root, path, cwd string) (*Follower, error) {
	root = filepath.Clean(root)
	clean := filepath.Clean(path)
	if !strings.HasSuffix(clean, ".jsonl") || !within(root, clean) {
		return nil, errors.New("transcript outside the Claude projects directory")
	}
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		realRoot, rerr := filepath.EvalSymlinks(root)
		if rerr != nil || !within(realRoot, real) {
			return nil, errors.New("transcript resolves outside the Claude projects directory")
		}
	}
	return &Follower{path: clean, cwd: cwd, off: -1, byID: map[string]int{}}, nil
}

func within(root, p string) bool {
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// Poll reads what was appended since the last call and reports whether the
// timeline changed.
func (f *Follower) Poll() (bool, error) {
	fh, err := os.Open(f.path)
	if err != nil {
		return false, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() {
		return false, errors.New("transcript is not a regular file")
	}
	size, ino := st.Size(), inode(st)
	before := f.ver
	if f.off < 0 || ino != f.ino || size < f.off {
		f.events, f.byID, f.partial, f.skipping = nil, map[string]int{}, nil, false
		f.ino, f.off = ino, 0
		f.ver++
		if size > tailBytes {
			f.off, f.skipping = size-tailBytes, true
		}
	}
	if size > f.off {
		if _, err := fh.Seek(f.off, io.SeekStart); err != nil {
			return false, err
		}
		data, err := io.ReadAll(io.LimitReader(fh, maxRead))
		if err != nil {
			return false, err
		}
		f.off += int64(len(data))
		f.feed(data)
	}
	return f.ver != before, nil
}

// Events returns a copy of the timeline.
func (f *Follower) Events() []Event {
	out := make([]Event, len(f.events))
	copy(out, f.events)
	return out
}

func (f *Follower) feed(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if !f.skipping {
				if len(f.partial)+len(data) > maxLine {
					f.partial, f.skipping = nil, true
				} else {
					f.partial = append(f.partial, data...)
				}
			}
			return
		}
		chunk := data[:i]
		data = data[i+1:]
		if f.skipping {
			f.skipping, f.partial = false, nil
			continue
		}
		line := chunk
		if len(f.partial) > 0 {
			line = append(f.partial, chunk...)
			f.partial = nil
		}
		if len(line) > 0 && len(line) <= maxLine {
			f.parse(line)
		}
	}
}

type rawLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	IsMeta    bool      `json:"isMeta"`
	IsCompact bool      `json:"isCompactSummary"`
	Message   struct {
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

var (
	assistantTag = []byte(`"type":"assistant"`)
	userTag      = []byte(`"type":"user"`)
)

func (f *Follower) parse(line []byte) {
	if !bytes.Contains(line, assistantTag) && !bytes.Contains(line, userTag) {
		return
	}
	var r rawLine
	if json.Unmarshal(line, &r) != nil {
		return
	}
	switch r.Type {
	case "assistant":
		f.assistant(r)
	case "user":
		f.user(r)
	}
}

func (f *Follower) assistant(r rawLine) {
	var blocks []block
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return
	}
	first := len(f.events)
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				f.add(Event{At: r.Timestamp, Kind: Say, Text: prose(t, 2000)})
			}
		case "tool_use":
			e := Event{
				At: r.Timestamp, Kind: Tool, Tool: b.Name, Verb: toolfmt.Verb(b.Name), ID: b.ID,
				Detail: clean(toolfmt.Detail(b.Name, b.Input, f.cwd), 300),
			}
			if effs := effects.Classify(b.Name, b.Input); len(effs) > 0 {
				e.Sys, e.Act, e.Target = effs[0].Sys, effs[0].Act, clean(effs[0].Target, 200)
				if len(effs) > 1 {
					e.Target += fmt.Sprintf(" (+%d)", len(effs)-1)
				}
			}
			// Rough counts until the result brings the real patch.
			if b.Name == "Edit" {
				if s, ok := b.Input["old_string"].(string); ok {
					e.Minus = lineCount(s)
				}
				if s, ok := b.Input["new_string"].(string); ok {
					e.Plus = lineCount(s)
				}
			}
			f.add(e)
			if b.ID != "" {
				f.byID[b.ID] = len(f.events) - 1
			}
		}
	}
	if r.Message.StopReason == "end_turn" {
		for i := first; i < len(f.events); i++ {
			if f.events[i].Kind == Say {
				f.events[i].Kind = End
			}
		}
	}
}

func (f *Follower) user(r rawLine) {
	if r.IsCompact {
		f.add(Event{At: r.Timestamp, Kind: Compact})
		return
	}
	if r.IsMeta || len(r.Message.Content) == 0 {
		return
	}
	if r.Message.Content[0] == '"' {
		var s string
		if json.Unmarshal(r.Message.Content, &s) == nil {
			f.prompt(r.Timestamp, s)
		}
		return
	}
	var blocks []block
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return
	}
	var texts []string
	results := 0
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results++
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			t := strings.TrimSpace(b.Text)
			switch {
			case strings.HasPrefix(t, "[Request interrupted"):
				f.add(Event{At: r.Timestamp, Kind: Interrupt})
			case strings.HasPrefix(t, "[Image"), t == "":
			default:
				texts = append(texts, t)
			}
		case "tool_result":
			var tur json.RawMessage
			if results == 1 {
				tur = r.ToolUseResult
			}
			f.result(b, r.Timestamp, tur)
		}
	}
	if len(texts) > 0 {
		f.prompt(r.Timestamp, strings.Join(texts, " "))
	}
}

func (f *Follower) prompt(at time.Time, text string) {
	if p := toolfmt.CleanPrompt(text); p != "" {
		f.add(Event{At: at, Kind: Prompt, Text: clean(p, 2000)})
	}
}

type patchResult struct {
	Type            string `json:"type"`
	Content         string `json:"content"`
	StructuredPatch []struct {
		Lines []string `json:"lines"`
	} `json:"structuredPatch"`
}

func (f *Follower) result(b block, at time.Time, tur json.RawMessage) {
	i, ok := f.byID[b.ToolUseID]
	if !ok || i >= len(f.events) {
		return
	}
	e := &f.events[i]
	e.Done, e.Error = true, b.IsError
	if at.After(e.At) {
		e.Dur = at.Sub(e.At)
	}
	if b.IsError {
		e.ErrLine = errLine(resultText(b.Content))
	} else if e.Sys == "Jira" && e.Act == "crea" {
		if key := effects.IssueKey(resultText(b.Content)); key != "" && !strings.HasPrefix(e.Target, key) {
			e.Target = strings.TrimSpace(key + " " + e.Target)
		}
	}
	if len(tur) > 0 && tur[0] == '{' {
		var p patchResult
		if json.Unmarshal(tur, &p) == nil {
			switch {
			case len(p.StructuredPatch) > 0:
				var lines []string
				for _, h := range p.StructuredPatch {
					lines = append(lines, h.Lines...)
				}
				e.Plus, e.Minus, e.Diff = diff(lines)
			case p.Type == "create" && p.Content != "":
				lines := strings.Split(strings.TrimRight(p.Content, "\n"), "\n")
				for j := range lines {
					lines[j] = "+" + lines[j]
				}
				e.Plus, e.Minus, e.Diff = diff(lines)
			}
		}
	}
	f.ver++
}

func (f *Follower) add(e Event) {
	f.events = append(f.events, e)
	f.ver++
	if len(f.events) <= maxEvents+100 {
		return
	}
	f.events = append([]Event(nil), f.events[len(f.events)-maxEvents:]...)
	f.byID = map[string]int{}
	for i, ev := range f.events {
		if ev.Kind == Tool && ev.ID != "" {
			f.byID[ev.ID] = i
		}
	}
}

func clean(s string, n int) string {
	return sanitize.Line(sanitize.Redact(s), n)
}

var (
	mdHeading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	mdRule    = regexp.MustCompile(`(?m)^\s*\|?(\s*:?-{3,}:?\s*\|)+\s*:?-{0,}:?\s*$`)
	mdEmph    = strings.NewReplacer("**", "", "__", "", "`", "")
)

// Prose turns Markdown into redacted, sanitized plain text of at most n runes.
func Prose(s string, n int) string { return prose(s, n) }

// prose turns the agent's Markdown into plain text for one-line display.
func prose(s string, n int) string {
	s = mdRule.ReplaceAllString(s, "")
	s = mdHeading.ReplaceAllString(s, "")
	return clean(mdEmph.Replace(s), n)
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

// diff counts changed lines and keeps a redacted, display-safe excerpt.
// Private key blocks are collapsed because Redact works line by line here.
func diff(lines []string) (plus, minus int, out []string) {
	inKey := false
	for _, l := range lines {
		if l == "" || (l[0] != '+' && l[0] != '-') {
			continue
		}
		if l[0] == '+' {
			plus++
		} else {
			minus++
		}
		if len(out) >= maxDiff {
			continue
		}
		body := l[1:]
		switch {
		case strings.Contains(body, "BEGIN") && strings.Contains(body, "PRIVATE KEY"):
			inKey = true
			out = append(out, l[:1]+" [REDACTED PRIVATE KEY]")
			continue
		case inKey:
			if strings.Contains(body, "END") && strings.Contains(body, "PRIVATE KEY") {
				inKey = false
			}
			continue
		}
		trimmed := strings.TrimLeft(body, " \t")
		indent := 0
		for _, c := range body[:len(body)-len(trimmed)] {
			if c == '\t' {
				indent += 2
			} else {
				indent++
			}
		}
		out = append(out, l[:1]+" "+strings.Repeat(" ", min(indent, 16))+sanitize.Line(sanitize.Redact(trimmed), 300))
	}
	return plus, minus, out
}

func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
				b.WriteByte('\n')
			}
		}
		return b.String()
	}
	return ""
}

// errLine keeps the first useful line of a failed result, e.g.
// "Exit code 1 · FAIL: TestClose".
func errLine(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "<tool_use_error>") && strings.TrimSpace(strings.TrimPrefix(l, "<tool_use_error>")) == "" {
			continue
		}
		l = strings.TrimSuffix(strings.TrimPrefix(l, "<tool_use_error>"), "</tool_use_error>")
		keep = append(keep, l)
		if !strings.HasPrefix(l, "Exit code") || len(keep) == 2 {
			break
		}
	}
	return clean(strings.Join(keep, " · "), 300)
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
