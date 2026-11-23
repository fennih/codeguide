// Package usage reads how full a session's context window is, from the token
// counts Claude Code records on each assistant message, and the plan limits
// (5-hour and weekly) that Claude Code caches in ~/.claude.json. Both formats
// are internal to Claude Code; anything that does not parse is ignored.
package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fennih/codeguide/internal/sanitize"
)

// Context is the size of the prompt sent with a session's latest request.
type Context struct {
	Model  string    `json:"model,omitempty"`
	Tokens int       `json:"tokens"`
	Window int       `json:"window"`
	At     time.Time `json:"at"`
}

func (c Context) Known() bool { return c.Tokens > 0 && c.Window > 0 }

func (c Context) Percent() int {
	if !c.Known() {
		return 0
	}
	return min(100, c.Tokens*100/c.Window)
}

// window guesses the context size: the Claude 5 Opus, Sonnet and Fable models
// run with 1M tokens, the rest with 200k. A request larger than the guess
// proves a 1M window.
func window(model string, tokens int) int {
	m := strings.ToLower(model)
	w := 200_000
	if strings.Contains(m, "[1m]") ||
		(strings.Contains(m, "-5") && (strings.Contains(m, "opus") || strings.Contains(m, "sonnet") || strings.Contains(m, "fable"))) {
		w = 1_000_000
	}
	if tokens > w {
		w = 1_000_000
	}
	return w
}

type line struct {
	Type      string    `json:"type"`
	Sidechain bool      `json:"isSidechain"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Model string `json:"model"`
		Usage *struct {
			Input       int `json:"input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	assistantTag = []byte(`"assistant"`)
	usageTag     = []byte(`"usage"`)
)

// Last returns the context of the latest assistant message in a chunk of
// transcript lines. sidechain selects subagent transcripts, whose lines are
// all marked as sidechain.
func Last(data []byte, sidechain bool) (Context, bool) {
	for len(data) > 0 {
		i := bytes.LastIndexByte(data, '\n')
		raw := data[i+1:]
		if i < 0 {
			data = nil
		} else {
			data = data[:i]
		}
		if !bytes.Contains(raw, usageTag) || !bytes.Contains(raw, assistantTag) {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil || l.Type != "assistant" || l.Message.Usage == nil || l.Sidechain != sidechain {
			continue
		}
		u := l.Message.Usage
		n := u.Input + u.CacheRead + u.CacheCreate
		if n == 0 || l.Message.Model == "<synthetic>" {
			continue
		}
		return Context{Model: sanitize.Line(l.Message.Model, 40), Tokens: n, Window: window(l.Message.Model, n), At: l.Timestamp}, true
	}
	return Context{}, false
}

const (
	tailStart = 256 << 10
	tailMax   = 4 << 20
)

type entry struct {
	size  int64
	mtime time.Time
	ctx   Context
}

// Reader finds the latest context size of session transcripts, re-reading a
// file only when it changed and then only its tail.
type Reader struct {
	root  string
	cache map[string]entry
}

func NewReader(root string) *Reader {
	return &Reader{root: filepath.Clean(root), cache: map[string]entry{}}
}

// Session returns the context of the transcript at path, which must be a
// .jsonl file under the Claude projects directory.
func (r *Reader) Session(path string) Context {
	clean := filepath.Clean(path)
	if !strings.HasSuffix(clean, ".jsonl") || !strings.HasPrefix(clean, r.root+string(filepath.Separator)) {
		return Context{}
	}
	f, err := os.Open(clean)
	if err != nil {
		delete(r.cache, clean)
		return Context{}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return Context{}
	}
	if e, ok := r.cache[clean]; ok && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.ctx
	}
	ctx := r.cache[clean].ctx
	// Large tool results can push the last assistant message far from the
	// end, so the window grows until one is found.
	for n := int64(tailStart); ; n *= 4 {
		off := max(0, st.Size()-n)
		data := make([]byte, st.Size()-off)
		if _, err := f.ReadAt(data, off); err != nil && err != io.EOF {
			break
		}
		if off > 0 {
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				data = data[i+1:]
			}
		}
		if c, ok := Last(data, false); ok {
			ctx = c
			break
		}
		if off == 0 || n >= tailMax {
			break
		}
	}
	r.cache[clean] = entry{size: st.Size(), mtime: st.ModTime(), ctx: ctx}
	return ctx
}

// Forget drops cached files not in keep.
func (r *Reader) Forget(keep map[string]bool) {
	for p := range r.cache {
		if !keep[p] {
			delete(r.cache, p)
		}
	}
}
