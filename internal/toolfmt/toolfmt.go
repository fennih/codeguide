// Package toolfmt turns a tool call into a short human-readable argument.
package toolfmt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fennih/codeguide/internal/i18n"
)

var home, _ = os.UserHomeDir()

// Detail picks the most telling argument of a tool call.
func Detail(tool string, input map[string]any, cwd string) string {
	str := func(k string) string {
		if v, ok := input[k].(string); ok {
			return v
		}
		return ""
	}
	switch tool {
	case "Bash":
		if d := str("description"); d != "" {
			return d
		}
		return str("command")
	case "Read", "Write", "Edit", "MultiEdit", "NotebookEdit":
		p := str("file_path")
		if p == "" {
			p = str("notebook_path")
		}
		return ShortPath(p, cwd)
	case "Grep", "Glob":
		s := str("pattern")
		if p := str("path"); p != "" {
			s += "  " + ShortPath(p, cwd)
		}
		return s
	case "Agent", "Task":
		d := str("description")
		if t := str("subagent_type"); t != "" {
			return t + ": " + d
		}
		return d
	case "Skill":
		return str("skill")
	case "WebFetch":
		return str("url")
	case "WebSearch":
		return str("query")
	case "ToolSearch":
		return str("query")
	case "AskUserQuestion":
		if qs, ok := input["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				if s, ok := q["question"].(string); ok {
					return s
				}
			}
		}
	}
	for _, k := range []string{"description", "file_path", "path", "query", "url", "command", "prompt", "name", "message"} {
		if s := str(k); s != "" {
			if strings.HasPrefix(s, "/") {
				return ShortPath(s, cwd)
			}
			return s
		}
	}
	return ""
}

// DetailJSON is Detail for a raw JSON object string.
func DetailJSON(tool, raw, cwd string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	return Detail(tool, m, cwd)
}

var verbs = map[string]string{
	"Bash":            "Runs",
	"Read":            "Reads",
	"Edit":            "Edits",
	"MultiEdit":       "Edits",
	"NotebookEdit":    "Edits",
	"Write":           "Writes",
	"Grep":            "Searches",
	"Glob":            "Finds files",
	"WebSearch":       "Web search",
	"WebFetch":        "Opens",
	"Agent":           "Delegates",
	"Task":            "Delegates",
	"Skill":           "Skill",
	"AskUserQuestion": "Asks you",
	"ExitPlanMode":    "Plan",
	"EnterPlanMode":   "Plans",
	"ToolSearch":      "Loads tools",
	"TodoWrite":       "Todos",
	"Monitor":         "Monitors",
	"SendMessage":     "Message",
}

// Verb names a tool call as an action a person would describe.
func Verb(tool string) string {
	if v, ok := verbs[tool]; ok {
		return i18n.T(v)
	}
	return ShortTool(tool)
}

var (
	pasteTagRe = regexp.MustCompile(`</?pasted_content[^>]*>`)
	systemTags = []string{"<task-notification", "<system-reminder", "<local-command", "<command-name>", "<bash-stdout", "<bash-input"}
)

// CleanPrompt drops harness-injected turns and unwraps pasted-content tags.
func CleanPrompt(p string) string {
	t := strings.TrimSpace(p)
	for _, tag := range systemTags {
		if strings.HasPrefix(t, tag) {
			return ""
		}
	}
	return strings.TrimSpace(pasteTagRe.ReplaceAllString(t, " "))
}

// ShortTool trims MCP prefixes: mcp__claude_ai_Atlassian__search -> Atlassian.search
func ShortTool(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
	if len(parts) < 2 {
		return name
	}
	srv := strings.TrimPrefix(parts[0], "claude_ai_")
	if i := strings.LastIndex(srv, "_"); i > 0 && strings.HasPrefix(srv, "plugin_") {
		srv = srv[i+1:]
	}
	return srv + "." + parts[len(parts)-1]
}

// ShortPath makes a path relative to cwd, or ~-prefixed.
func ShortPath(p, cwd string) string {
	if p == "" {
		return ""
	}
	// cmux stores paths with the home directory already shortened to "~".
	if home != "" && strings.HasPrefix(p, "~/") {
		p = home + p[1:]
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// Project is the last path element of cwd, used as the project label.
func Project(cwd string) string {
	if cwd == "" {
		return "?"
	}
	return filepath.Base(cwd)
}
