// Package effects recognizes tool calls that change something outside the
// machine: tickets, wiki pages, documents, pushes, cloud resources. It is a
// heuristic floor, not a complete list: a script can write anywhere without
// matching any rule here.
package effects

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

type Effect struct {
	Sys     string `json:"sys"`    // Jira, Confluence, Google, git, GitHub, AWS, ...
	Act     string `json:"act"`    // what it did, in Italian; "?" when unknown
	Target  string `json:"target"` // raw, untrusted: callers sanitize before display
	Unknown bool   `json:"unknown,omitempty"`
}

// Classify returns the external effects of one tool call.
func Classify(tool string, input map[string]any) []Effect {
	if tool == "Bash" {
		cmd, _ := input["command"].(string)
		return classifyCommand(cmd)
	}
	if strings.HasPrefix(tool, "mcp__") {
		if e, ok := classifyMCP(tool, input); ok {
			return []Effect{e}
		}
	}
	return nil
}

// ClassifyJSON is Classify for a raw input string. cmux's Feed truncates
// long inputs, so a Bash input that is not valid JSON is matched as text.
func ClassifyJSON(tool, raw string) []Effect {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		if tool != "Bash" {
			m = nil
		} else {
			m = map[string]any{"command": raw}
		}
	}
	return Classify(tool, m)
}

var writeWords = map[string]string{
	"create": "crea", "add": "crea", "generate": "crea", "draft": "crea", "duplicate": "crea",
	"edit": "modifica", "update": "modifica", "set": "modifica", "modify": "modifica", "patch": "modifica",
	"apply": "modifica", "replace": "modifica", "rename": "modifica", "label": "modifica", "unlabel": "modifica",
	"mark": "modifica", "unmark": "modifica", "certify": "modifica", "uncertify": "modifica",
	"transition": "transita", "move": "sposta", "copy": "copia",
	"delete": "elimina", "remove": "elimina", "trash": "elimina", "clear": "elimina", "archive": "elimina",
	"untrash": "ripristina",
	"send":    "invia", "reply": "invia", "forward": "invia", "post": "invia",
	"share": "condivide", "unshare": "condivide",
	"upload": "carica", "publish": "pubblica", "comment": "commenta",
	"append": "scrive", "insert": "scrive", "write": "scrive", "batch": "scrive",
}

// priorityWords name the effect better than a generic verb next to them,
// e.g. addCommentToJiraIssue comments rather than creates.
var priorityWords = map[string]string{
	"comment": "commenta", "worklog": "registra ore", "transition": "transita", "link": "collega",
}

var readWords = map[string]bool{
	"get": true, "search": true, "list": true, "read": true, "fetch": true, "query": true, "lookup": true,
	"download": true, "export": true, "find": true, "describe": true, "check": true, "view": true, "show": true,
	"whoami": true, "authenticate": true, "authentication": true, "auth": true, "scan": true, "findings": true,
	"metadata": true, "info": true, "status": true, "schema": true, "supported": true, "snapshot": true,
	"screenshot": true, "take": true, "analyze": true, "wait": true, "guide": true, "select": true,
	"navigate": true, "new": true, "close": true, "hover": true, "emulate": true, "resize": true,
	"console": true, "network": true, "logs": true, "trace": true, "lighthouse": true, "heapsnapshot": true,
	"skill": true, "tree": true, "state": true, "cursor": true, "config": true, "health": true,
	"permissions": true, "size": true, "follow": true, "transcript": true, "results": true, "dashboard": true,
}

// Browser and computer-use servers act on whatever page or app is open, so
// only input-producing actions count.
var browserActs = map[string]string{
	"click": "clic", "double_click": "clic", "right_click": "clic", "fill": "compila", "fill_form": "compila",
	"type_text": "scrive", "upload_file": "carica", "press_key": "tasto", "hotkey": "tasto",
	"handle_dialog": "conferma", "evaluate_script": "esegue script", "drag": "trascina", "set_value": "compila",
	"perform_actions": "azioni", "kill_app": "chiude app",
}

var browserServers = map[string]bool{"chrome-devtools": true, "cmux-cua": true, "playwright": true, "computer-use": true}

func classifyMCP(tool string, input map[string]any) (Effect, bool) {
	parts := strings.Split(strings.TrimPrefix(tool, "mcp__"), "__")
	if len(parts) < 2 {
		return Effect{}, false
	}
	server, name := parts[0], parts[len(parts)-1]
	if browserServers[server] {
		act, ok := browserActs[name]
		if !ok {
			return Effect{}, false
		}
		return Effect{Sys: "browser", Act: act, Target: target(input)}, true
	}
	sys := system(server, name)
	words := splitWords(name)
	// The leading verb decides: list_loki_label_names is a read.
	if len(words) > 0 && readWords[words[0]] {
		return Effect{}, false
	}
	for _, w := range words {
		if act, ok := priorityWords[w]; ok {
			return Effect{Sys: sys, Act: act, Target: target(input)}, true
		}
	}
	for _, w := range words {
		if act, ok := writeWords[w]; ok {
			return Effect{Sys: sys, Act: act, Target: target(input)}, true
		}
	}
	for _, w := range words {
		if readWords[w] {
			return Effect{}, false
		}
	}
	return Effect{Sys: sys, Act: "?", Target: name, Unknown: true}, true
}

var knownSystems = [][2]string{
	{"gmail", "Gmail"}, {"slack", "Slack"}, {"figma", "Figma"}, {"notion", "Notion"}, {"linear", "Linear"},
	{"asana", "Asana"}, {"hubspot", "HubSpot"}, {"github", "GitHub"}, {"sentry", "Sentry"}, {"zapier", "Zapier"},
	{"n8n", "n8n"}, {"claude_docs", "Claude Docs"}, {"claude_data", "Claude Data"}, {"intercom", "Intercom"},
	{"monday_com", "monday"}, {"canva", "Canva"}, {"webflow", "Webflow"}, {"supabase", "Supabase"},
}

func system(server, name string) string {
	s := strings.ToLower(strings.TrimPrefix(server, "claude_ai_"))
	n := strings.ToLower(name)
	switch {
	case strings.Contains(s, "atlassian"):
		switch {
		case strings.Contains(n, "confluence") || strings.Contains(n, "page"):
			return "Confluence"
		case strings.Contains(n, "compass"):
			return "Compass"
		}
		return "Jira"
	case strings.HasPrefix(s, "google"):
		return "Google"
	}
	for _, k := range knownSystems {
		if strings.Contains(s, k[0]) {
			return k[1]
		}
	}
	if i := strings.LastIndex(s, "_"); i > 0 && strings.HasPrefix(s, "plugin_") {
		s = s[i+1:]
	}
	return s
}

// splitWords breaks createJiraIssue or update_doc into lowercase words.
func splitWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 && !unicode.IsUpper(cur[len(cur)-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

var targetKeys = []string{
	"issueIdOrKey", "issueKey", "key", "title", "summary", "pageId", "documentId", "spreadsheetId",
	"presentationId", "fileId", "fileName", "name", "channel", "channel_id", "to", "subject", "url", "id",
	// cmux's Feed often keeps only a truncated description of an MCP call.
	"description",
}

func target(input map[string]any) string {
	if input == nil {
		return ""
	}
	str := func(k string) string {
		if v, ok := input[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	if p, s := str("projectKey"), str("summary"); s != "" {
		if p != "" {
			return p + " · " + s
		}
		return s
	}
	for _, k := range targetKeys {
		if v := str(k); v != "" {
			return v
		}
	}
	return ""
}

type rule struct {
	re   *regexp.Regexp
	sys  string
	act  string
	skip *regexp.Regexp // matches that are reads despite the verb
}

var rules = []rule{
	{re: regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?push\b[^|;&\n]*`), sys: "git", act: "push"},
	{re: regexp.MustCompile(`\bgh\s+(?:pr|issue|release|repo|gist|secret|variable|label)\s+(?:create|merge|close|edit|delete|comment|review|reopen|set|transfer|archive|rename)\b[^|;&\n]*`), sys: "GitHub", act: "modifica"},
	{
		re:   regexp.MustCompile(`\bgh\s+api\b[^|;&\n]*(?:\s(?:-X|--method)\s*(?:POST|PUT|PATCH|DELETE)\b|\s(?:-f|-F|--field|--raw-field|--input)\s)[^|;&\n]*`),
		sys:  "GitHub",
		act:  "api",
		skip: regexp.MustCompile(`\s(?:-X|--method)\s*GET\b`),
	},
	{re: regexp.MustCompile(`\baws\s+s3\s+(?:rm|rb|mb|mv)\b[^|;&\n]*|\baws\s+s3\s+(?:cp|sync)\s+\S+\s+s3://[^|;&\n]*`), sys: "AWS", act: "s3"},
	{
		re:   regexp.MustCompile(`(?:^|[\s;&|(])aws\s[^|;&\n]*?\s[a-z0-9-]+\s+(?:create|put|delete|update|modify|attach|detach|tag|untag|start|stop|terminate|invoke|publish|send|deploy|run|associate|disassociate|enable|disable|register|deregister|reboot|restore|import|upload|add|remove|revoke|authorize)(?:-[a-z0-9-]+)?\b[^|;&\n]*`),
		sys:  "AWS",
		act:  "modifica",
		skip: regexp.MustCompile(`\s(?:start-query|start-live-tail|get-[a-z-]+|describe-[a-z-]+|list-[a-z-]+)\b`),
	},
	{re: regexp.MustCompile(`\bgcloud\b[^|;&\n]*\s(?:create|delete|update|deploy|patch|import|enable|disable|set-iam-policy|add-iam-policy-binding|remove-iam-policy-binding)\b[^|;&\n]*`), sys: "GCP", act: "modifica"},
	{re: regexp.MustCompile(`\bkubectl\b[^|;&\n]*\s(?:apply|delete|create|patch|scale|edit|replace|annotate|label|cordon|drain|rollout\s+(?:restart|undo))\b[^|;&\n]*`), sys: "k8s", act: "modifica"},
	{re: regexp.MustCompile(`\b(?:terraform|tofu)\s+(?:-chdir=\S+\s+)?(?:apply|destroy|import|state\s+(?:rm|mv|push))\b[^|;&\n]*|\bpulumi\s+(?:up|destroy|import|refresh|state\s+delete|config\s+set)\b[^|;&\n]*`), sys: "IaC", act: "applica"},
	{
		re:   regexp.MustCompile(`\bcurl\b[^|;&\n]*(?:\s-X\s*(?:POST|PUT|PATCH|DELETE)\b|\s(?:-d|--data[a-z-]*|-F|--form|-T|--upload-file)[\s=])[^|;&\n]*`),
		sys:  "HTTP",
		act:  "invia",
		skip: regexp.MustCompile(`\s(?:-G|--get)\b`), // data becomes a query string
	},
	{re: regexp.MustCompile(`\bhttps?\s+(?:POST|PUT|PATCH|DELETE)\s[^|;&\n]*`), sys: "HTTP", act: "invia"},
	{re: regexp.MustCompile(`\bdocker\s+push\b[^|;&\n]*|\b(?:npm|pnpm|yarn|bun|cargo)\s+publish\b[^|;&\n]*|\btwine\s+upload\b[^|;&\n]*`), sys: "registry", act: "pubblica"},
}

var urlRe = regexp.MustCompile(`https?://[^\s'"]+`)

func classifyCommand(cmd string) []Effect {
	if cmd == "" {
		return nil
	}
	var out []Effect
	for _, r := range rules {
		for _, m := range r.re.FindAllString(cmd, 3) {
			if r.skip != nil && r.skip.MatchString(m) {
				continue
			}
			// GraphQL over gh api is a read unless it carries a mutation.
			if r.act == "api" && strings.Contains(m, "graphql") && !strings.Contains(m, "mutation") {
				continue
			}
			t := strings.TrimSpace(m)
			if r.sys == "HTTP" {
				if u := urlRe.FindString(m); u != "" {
					t = u
				}
			}
			out = append(out, Effect{Sys: r.sys, Act: r.act, Target: t})
		}
	}
	return out
}

var issueKeyRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d{1,7}\b`)

// IssueKey finds a Jira key in a tool result, e.g. the issue just created.
func IssueKey(result string) string {
	return issueKeyRe.FindString(result)
}
