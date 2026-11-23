// Package subagents reads Claude Code's per-subagent transcript files.
// cmux does not attribute tool calls to subagents, so this is the one place
// ccwatch reads Claude's internal (unstable) transcript format. Any parse
// failure degrades to label-only rows.
package subagents

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ccwatch/internal/toolfmt"
)

const (
	tailBytes  = 128 << 10
	idleCutoff = 90 * time.Second
)

type Agent struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Model       string    `json:"model,omitempty"`
	Tool        string    `json:"tool,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Running     bool      `json:"running"`
	Started     time.Time `json:"started"`
	LastWrite   time.Time `json:"lastWrite"`
	Calls       int       `json:"calls"`
}

type meta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Model       string `json:"model"`
}

type cacheEntry struct {
	size  int64
	mtime time.Time
	agent Agent
}

// Reader caches parsed subagents by file size and mtime.
type Reader struct {
	Root  string // only transcripts under this directory are read
	cache map[string]cacheEntry
}

func NewReader(root string) *Reader {
	return &Reader{Root: filepath.Clean(root), cache: map[string]cacheEntry{}}
}

// List returns the subagents of a session, newest first.
func (r *Reader) List(transcriptPath, cwd string, now time.Time) []Agent {
	if transcriptPath == "" || !strings.HasSuffix(transcriptPath, ".jsonl") {
		return nil
	}
	clean := filepath.Clean(transcriptPath)
	if !strings.HasPrefix(clean, r.Root+string(filepath.Separator)) {
		return nil
	}
	dir := filepath.Join(strings.TrimSuffix(clean, ".jsonl"), "subagents")
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	var out []Agent
	for _, mp := range metas {
		base := strings.TrimSuffix(mp, ".meta.json")
		a, ok := r.read(base, cwd, now)
		if ok {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

func (r *Reader) read(base, cwd string, now time.Time) (Agent, bool) {
	jsonl := base + ".jsonl"
	st, err := os.Stat(jsonl)
	if err != nil || !st.Mode().IsRegular() {
		return Agent{}, false
	}
	if c, ok := r.cache[jsonl]; ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		a := c.agent
		if now.Sub(a.LastWrite) > idleCutoff {
			a.Running = false
		}
		return a, true
	}
	a := Agent{ID: strings.TrimPrefix(filepath.Base(base), "agent-"), LastWrite: st.ModTime()}
	if mst, err := os.Stat(base + ".meta.json"); err == nil {
		a.Started = mst.ModTime()
		if b, err := readLimited(base+".meta.json", 64<<10); err == nil {
			var m meta
			if json.Unmarshal(b, &m) == nil {
				a.Type, a.Description, a.Model = m.AgentType, m.Description, m.Model
			}
		}
	}
	handedBack, endTurn := r.scanTail(jsonl, st.Size(), cwd, &a)
	a.Running = !handedBack && now.Sub(a.LastWrite) < idleCutoff && !(endTurn && now.Sub(a.LastWrite) > 10*time.Second)
	r.cache[jsonl] = cacheEntry{size: st.Size(), mtime: st.ModTime(), agent: a}
	return a, true
}

type line struct {
	Type    string `json:"type"`
	Message struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// scanTail finds the latest tool call and whether the subagent has finished.
func (r *Reader) scanTail(path string, size int64, cwd string, a *Agent) (handedBack, endTurn bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	off := int64(0)
	if size > tailBytes {
		off = size - tailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return false, false
	}
	data, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return false, false
	}
	if off > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		if len(raw) == 0 || !bytes.Contains(raw, []byte(`"assistant"`)) {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil || l.Type != "assistant" {
			continue
		}
		endTurn = l.Message.StopReason == "end_turn"
		for _, c := range l.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			a.Calls++
			if c.Name == "SubagentHandback" {
				handedBack = true
				continue
			}
			a.Tool = toolfmt.ShortTool(c.Name)
			a.Detail = toolfmt.Detail(c.Name, c.Input, cwd)
		}
	}
	return handedBack, endTurn
}

func readLimited(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}
