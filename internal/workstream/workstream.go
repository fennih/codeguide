// Package workstream tails cmux's Feed audit log (~/.cmuxterm/workstream.jsonl),
// which cmux fills from the Claude Code hooks it injects into every session.
package workstream

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

const maxLine = 1 << 20

type Record struct {
	ID           string                     `json:"id"`
	Kind         string                     `json:"kind"`
	Source       string                     `json:"source"`
	Cwd          string                     `json:"cwd"`
	Title        string                     `json:"title"`
	WorkstreamID string                     `json:"workstreamId"`
	CreatedAt    time.Time                  `json:"createdAt"`
	UpdatedAt    time.Time                  `json:"updatedAt"`
	Status       map[string]json.RawMessage `json:"status"`
	Context      struct {
		ToolSummary       string `json:"toolSummary"`
		AssistantPreamble string `json:"assistantPreamble"`
		LastUserMessage   string `json:"lastUserMessage"`
		PermissionMode    string `json:"permissionMode"`
		PlanSummary       string `json:"planSummary"`
	} `json:"context"`
	Payload struct {
		ToolUse *struct {
			ToolName      string `json:"toolName"`
			ToolInputJSON string `json:"toolInputJSON"`
		} `json:"toolUse"`
		ToolResult *struct {
			ToolName string `json:"toolName"`
			IsError  bool   `json:"isError"`
		} `json:"toolResult"`
		UserPrompt *struct {
			Text string `json:"text"`
		} `json:"userPrompt"`
		Stop *struct {
			Reason string `json:"reason"`
		} `json:"stop"`
		PermissionRequest *struct {
			ToolName      string `json:"toolName"`
			ToolInputJSON string `json:"toolInputJSON"`
		} `json:"permissionRequest"`
		ExitPlan *struct {
			Plan string `json:"plan"`
		} `json:"exitPlan"`
		Question json.RawMessage `json:"question"`
	} `json:"payload"`

	SessionID string `json:"-"`
}

func (r Record) Pending() bool {
	_, ok := r.Status["pending"]
	return ok
}

// SessionID decodes "cmux-feed-v1:<b64 source>:<b64 session id>".
func sessionID(workstreamID string) string {
	parts := strings.Split(workstreamID, ":")
	if len(parts) != 3 {
		return ""
	}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(parts[2]); err == nil {
			return string(b)
		}
	}
	return ""
}

// Tailer reads new complete lines from the log, surviving rotation.
type Tailer struct {
	path    string
	f       *os.File
	ino     uint64
	off     int64
	partial []byte
}

func NewTailer(path string) *Tailer { return &Tailer{path: path} }

// Open positions the reader at most backlog bytes before the end.
func (t *Tailer) Open(backlog int64) error {
	f, err := os.Open(t.path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	t.f, t.ino, t.off, t.partial = f, inode(st), 0, nil
	if st.Size() > backlog {
		t.off = st.Size() - backlog
		// Skip the first, probably partial, line.
		if _, err := f.Seek(t.off, io.SeekStart); err != nil {
			return err
		}
		r := bufio.NewReader(f)
		skipped, err := r.ReadBytes('\n')
		if err != nil {
			return nil
		}
		t.off += int64(len(skipped))
	}
	return nil
}

// Poll returns Claude records appended since the last call.
func (t *Tailer) Poll() ([]Record, error) {
	if t.f == nil {
		if err := t.Open(0); err != nil {
			return nil, err
		}
	}
	if st, err := os.Stat(t.path); err == nil {
		cur, _ := t.f.Stat()
		if inode(st) != t.ino || (cur != nil && cur.Size() < t.off) {
			t.f.Close()
			if err := t.Open(0); err != nil {
				return nil, err
			}
		}
	}
	if _, err := t.f.Seek(t.off, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(t.f, 64<<20))
	if err != nil {
		return nil, err
	}
	t.off += int64(len(data))
	if len(t.partial) > 0 {
		data = append(t.partial, data...)
		t.partial = nil
	}
	var recs []Record
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if len(data) <= maxLine {
				t.partial = append([]byte(nil), data...)
			}
			break
		}
		line := data[:i]
		data = data[i+1:]
		if len(line) == 0 || len(line) > maxLine {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.Source != "claude" || r.ID == "" {
			continue
		}
		r.SessionID = sessionID(r.WorkstreamID)
		if r.SessionID == "" {
			continue
		}
		recs = append(recs, r)
	}
	return recs, nil
}

func (t *Tailer) Close() error {
	if t.f == nil {
		return nil
	}
	return t.f.Close()
}
