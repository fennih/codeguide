package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"ccwatch/internal/model"
	"ccwatch/internal/sanitize"
	"ccwatch/internal/subagents"
	"ccwatch/internal/toolfmt"
	"ccwatch/internal/transcript"
)

// detail is the state of the single-session view.
type detail struct {
	sid    string
	follow bool // stick to the newest events
	top    int  // first visible timeline line when not following
	diffs  bool
	subs   bool // expand finished subagents too
	turns  bool // show the turn outline instead of the timeline
	// fromLedger returns to the effects view on close.
	fromLedger bool
	// lookedAt is when the user last looked before opening; events after it
	// are new and get an "eri qui" divider.
	lookedAt time.Time
}

const (
	tsW       = 8  // "15:04:05"
	verbW     = 11 // widest verb, "Carica tool"
	diffShown = 12
	subShown  = 6
)

func (m Model) detailView(now time.Time) string {
	v, ok := m.viewByID(m.det.sid)
	if !ok {
		out := []string{m.header(now), "", sDim.Render("  la sessione non è più aperta in cmux  (esc per tornare)")}
		for len(out) < m.h-1 {
			out = append(out, "")
		}
		return strings.Join(append(out, m.detailFooter()), "\n")
	}
	head := detailHead(v, m.w, now)
	body, _ := m.detailLines(v, now)
	bodyH := max(1, m.h-len(head)-1)
	start := m.detailStart(len(body), bodyH)
	visible := body[start:min(len(body), start+bodyH)]
	for len(visible) < bodyH {
		visible = append(visible, "")
	}
	out := append(head, visible...)
	return strings.Join(append(out, m.detailFooter()), "\n")
}

func (m Model) detailStart(total, bodyH int) int {
	maxTop := max(0, total-bodyH)
	if m.det.follow {
		return maxTop
	}
	return max(0, min(m.det.top, maxTop))
}

func (m Model) viewByID(id string) (model.View, bool) {
	for _, v := range m.snap.Views {
		if v.SessionID == id {
			return v, true
		}
	}
	return model.View{}, false
}

func detailHead(v model.View, w int, now time.Time) []string {
	glyph := sDim.Render("·")
	state := "ferma da " + ago(v.LastAt, now)
	switch v.Lane {
	case model.LaneWorking:
		glyph = sAccent.Render(spin(now))
		state = "al lavoro da " + dur(now.Sub(v.TurnStart))
	case model.LaneNeedsYou:
		glyph = sYellow.Render("▲")
		state = "aspetta te"
	}
	right := sDim.Render(sanitize.Truncate(v.Project+" · "+state, max(10, w/2)))
	titleW := w - 4 - width(right)
	top := " " + glyph + " " + sHeader.Render(sanitize.Truncate(v.Title, max(4, titleW)))
	top += strings.Repeat(" ", max(1, w-width(top)-width(right))) + right

	lines := []string{top}
	label := func(name string, st lipgloss.Style, text string, style lipgloss.Style, maxLines int) {
		if text == "" {
			return
		}
		prefix := " " + st.Render(fmt.Sprintf("%-10s", name)) + " "
		for i, l := range wrap(text, w-width(prefix), maxLines) {
			if i > 0 {
				prefix = strings.Repeat(" ", 12)
			}
			lines = append(lines, prefix+style.Render(l))
		}
	}
	label("Obiettivo", sDim, v.Goal(), sBold, 2)
	label("Contesto", sDim, v.Summary.Brief, lipgloss.NewStyle(), 4)
	if v.Lane == model.LaneWorking {
		label("Ora", sAccent, v.Summary.Now, lipgloss.NewStyle(), 2)
	}
	switch {
	case v.Pending != "":
		label("Serve te", sYellow, v.Pending, sYellow, 3)
	case v.Summary.NeedsUser && v.Summary.Ask != "":
		label("Serve te", sYellow, v.Summary.Ask, sYellow, 3)
	}
	if v.Lane != model.LaneWorking {
		label("Esito", sGreen, v.Summary.Outcome, lipgloss.NewStyle(), 2)
	}
	return append(lines, sFaint.Render(strings.Repeat("─", w)))
}

// detailLines renders the whole body; the caller picks the window. div is
// the line index of the "eri qui" divider, or -1.
func (m Model) detailLines(v model.View, now time.Time) (lines []string, div int) {
	if m.det.turns {
		return turnLines(v, m.w, m.det.lookedAt)
	}
	w := m.w
	if len(v.Timeline) == 0 {
		return feedLines(v, w), -1
	}
	div = -1
	subsByCall := map[string]subagents.Agent{}
	for _, sa := range v.Subagents {
		if sa.ToolUseID != "" {
			subsByCall[sa.ToolUseID] = sa
		}
	}
	working := v.Lane == model.LaneWorking
	lastTool := -1
	for i, e := range v.Timeline {
		if e.Kind == transcript.Tool {
			lastTool = i
		}
	}
	textW := w - tsW - 4
	var out []string
	for i, e := range v.Timeline {
		if div < 0 && !m.det.lookedAt.IsZero() && e.At.After(m.det.lookedAt) && i > 0 {
			div = len(out)
			out = append(out, lookedDivider(m.det.lookedAt, w))
		}
		ts := sFaint.Render(e.At.Local().Format("15:04:05"))
		switch e.Kind {
		case transcript.Prompt:
			out = append(out, "", section("tu · "+e.At.Local().Format("15:04"), w))
			for _, l := range wrap(e.Text, w-4, 6) {
				out = append(out, "  "+sBold.Render(l))
			}
			out = append(out, "")
		case transcript.Say:
			out = append(out, textBlock(ts, sAccent.Render("›"), e.Text, lipgloss.NewStyle(), textW, 6)...)
		case transcript.End:
			out = append(out, textBlock(ts, sGreen.Render("■"), e.Text, lipgloss.NewStyle(), textW, 10)...)
		case transcript.Interrupt:
			out = append(out, ts+"  "+sYellow.Render("◼ interrotto da te"))
		case transcript.Compact:
			out = append(out, "", sFaint.Render("  ── contesto compattato ──"), "")
		case transcript.Tool:
			glyph := sGreen.Render("✓")
			switch {
			case e.Error:
				glyph = sRed.Render("✗")
			case !e.Done && working && i == lastTool:
				glyph = sAccent.Render(spin(now))
			case !e.Done && working:
				glyph = sAccent.Render("●")
			case !e.Done:
				glyph = sDim.Render("·")
			}
			ann := ""
			switch {
			case e.Plus+e.Minus > 0:
				ann = diffAnn(e.Plus, e.Minus)
			case !e.Done && working:
				ann = sDim.Render(dur(now.Sub(e.At)))
			case e.Dur >= time.Second && slowTool(e.Tool):
				ann = sFaint.Render(dur(e.Dur))
			}
			out = append(out, toolLine(ts+"  "+glyph+" ", e.Verb, e.Detail, ann, w))
			pad := strings.Repeat(" ", tsW+4)
			if e.Error && e.ErrLine != "" {
				out = append(out, pad+sRed.Render(sanitize.Truncate("→ "+e.ErrLine, w-len(pad))))
			}
			if m.det.diffs && len(e.Diff) > 0 {
				for j, l := range e.Diff {
					if j == diffShown {
						out = append(out, pad+sFaint.Render(fmt.Sprintf("  … altre %d righe", len(e.Diff)-diffShown)))
						break
					}
					st := sGreen
					if strings.HasPrefix(l, "-") {
						st = sRed
					}
					out = append(out, pad+st.Render(sanitize.Truncate(l, w-len(pad))))
				}
			}
			if sa, ok := subsByCall[e.ID]; ok {
				out = append(out, m.subLines(sa, v.SubMissions[sa.ID], v.SubEvents[sa.ID], w, now)...)
			}
		}
	}
	return out, div
}

func lookedDivider(at time.Time, w int) string {
	l := sBlue.Render("── eri qui · " + stamp(at) + " ")
	return l + sBlue.Render(strings.Repeat("─", max(0, w-width(l))))
}

// stamp shows the time, with the date when it is not today.
func stamp(t time.Time) string {
	t = t.Local()
	now := time.Now()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("02/01 15:04")
}

// turnLines is the outline of finished turns from the summary cache.
func turnLines(v model.View, w int, lookedAt time.Time) ([]string, int) {
	out := []string{sSection.Render("  filo dei turni") + sDim.Render("  (t per tornare al racconto)"), ""}
	div := -1
	if len(v.History) == 0 {
		out = append(out, sDim.Render("  nessun turno riassunto per questa sessione"))
	}
	pad := strings.Repeat(" ", 14)
	for i, h := range v.History {
		if div < 0 && !lookedAt.IsZero() && h.At.After(lookedAt) && i > 0 {
			div = len(out)
			out = append(out, lookedDivider(lookedAt, w))
		}
		head := " " + sFaint.Render(fmt.Sprintf("%-11s", stamp(h.At))) + " " + sGreen.Render("✓") + " "
		outcome := h.Outcome
		if outcome == "" {
			outcome = "(nessun esito)"
		}
		out = append(out, head+sanitize.Truncate(outcome, w-width(head)))
		if h.Goal != "" {
			out = append(out, pad+sDim.Render(sanitize.Truncate(h.Goal, w-len(pad))))
		}
		if h.NeedsUser && h.Ask != "" {
			out = append(out, pad+sYellow.Render(sanitize.Truncate("▲ "+h.Ask, w-len(pad))))
		}
	}
	last := time.Time{}
	if n := len(v.History); n > 0 {
		last = v.History[n-1].At
	}
	if v.TurnStart.After(last) && (v.Lane == model.LaneWorking || v.StopID == "") {
		if div < 0 && !lookedAt.IsZero() && v.TurnStart.After(lookedAt) && len(v.History) > 0 {
			div = len(out)
			out = append(out, lookedDivider(lookedAt, w))
		}
		head := " " + sFaint.Render(fmt.Sprintf("%-11s", stamp(v.TurnStart))) + " " + sAccent.Render("●") + " "
		out = append(out, head+sanitize.Truncate("in corso: "+v.Goal(), w-width(head)))
	}
	return out, div
}

// subLines nests a subagent under the Agent call that started it.
func (m Model) subLines(sa subagents.Agent, mission string, ev []transcript.Event, w int, now time.Time) []string {
	pad := strings.Repeat(" ", tsW+4)
	state := sGreen.Render("✓ finito")
	if sa.Running {
		state = sAccent.Render("● al lavoro") + sDim.Render(" da "+dur(now.Sub(sa.Started)))
	}
	head := pad + sCyan.Render("↳ "+sanitize.Truncate(sa.Type, 20)) + " "
	descW := w - width(head) - width(state) - 2
	line := head + sanitize.Truncate(sa.Description, max(4, descW))
	line += strings.Repeat(" ", max(1, w-width(line)-width(state))) + state
	out := []string{line}

	// Mission, brief and report; x shows brief and report in full.
	block := func(label string, st lipgloss.Style, text string, style lipgloss.Style, maxLines int) {
		if text == "" {
			return
		}
		prefix := pad + "  " + st.Render(fmt.Sprintf("%-8s", label)) + " "
		cont := strings.Repeat(" ", width(prefix))
		for i, l := range wrap(text, w-width(prefix), maxLines) {
			if i == 0 {
				out = append(out, prefix+style.Render(l))
			} else {
				out = append(out, cont+style.Render(l))
			}
		}
	}
	long := 4
	if m.det.subs {
		long = 30
	}
	if mission != "" && mission != sa.Description {
		block("missione", sCyan, mission, sBold, 2)
	}
	block("compito", sDim, sa.Task, sDim, long)
	if !sa.Running {
		block("esito", sGreen, sa.Result, lipgloss.NewStyle(), long)
	}

	var tools []transcript.Event
	lastSay := ""
	for _, e := range ev {
		switch e.Kind {
		case transcript.Tool:
			tools = append(tools, e)
		case transcript.Say, transcript.End:
			lastSay = e.Text
		}
	}
	if !sa.Running && !m.det.subs {
		if len(tools) > 0 {
			out = append(out, pad+sFaint.Render(fmt.Sprintf("  %d azioni  (x per vederle, con compito ed esito per intero)", len(tools))))
		}
		return out
	}
	if len(tools) > subShown {
		out = append(out, pad+sFaint.Render(fmt.Sprintf("  … %d azioni prima", len(tools)-subShown)))
		tools = tools[len(tools)-subShown:]
	}
	inner := pad + "  "
	for i, e := range tools {
		glyph := sGreen.Render("✓")
		switch {
		case e.Error:
			glyph = sRed.Render("✗")
		case !e.Done && sa.Running && i == len(tools)-1:
			glyph = sAccent.Render(spin(now))
		case !e.Done:
			glyph = sDim.Render("·")
		}
		ann := ""
		if e.Plus+e.Minus > 0 {
			ann = diffAnn(e.Plus, e.Minus)
		}
		out = append(out, toolLine(inner+glyph+" ", e.Verb, e.Detail, ann, w))
	}
	if lastSay != "" {
		out = append(out, inner+sIntent.Render(sanitize.Truncate("› "+lastSay, w-width(inner))))
	}
	return out
}

// slowTool marks calls whose duration is worth showing. Parallel reads share
// one result timestamp, so their durations would mislead.
func slowTool(name string) bool {
	switch name {
	case "Bash", "Agent", "Task", "WebFetch", "WebSearch":
		return true
	}
	return strings.HasPrefix(name, "mcp__")
}

// feedLines is the fallback when the transcript cannot be read.
func feedLines(v model.View, w int) []string {
	out := []string{sDim.Render("  transcript non disponibile: mostro le ultime azioni dal Feed di cmux"), ""}
	if v.Prompt != "" {
		for _, l := range wrap(v.Prompt, w-4, 4) {
			out = append(out, "  "+sBold.Render(l))
		}
		out = append(out, "")
	}
	for _, a := range v.Activity {
		ts := sFaint.Render(a.At.Local().Format("15:04:05"))
		glyph := sGreen.Render("✓")
		if a.Error {
			glyph = sRed.Render("✗")
		}
		out = append(out, toolLine(ts+"  "+glyph+" ", toolfmt.Verb(a.Tool), a.Detail, "", w))
	}
	return out
}

func toolLine(prefix, verb, detail, ann string, w int) string {
	left := prefix + sBold.Render(fit(verb, verbW)) + " "
	avail := w - width(left) - width(ann) - 2
	line := left + sDim.Render(sanitize.Truncate(detail, max(4, avail)))
	if ann != "" {
		line += strings.Repeat(" ", max(1, w-width(line)-width(ann)-1)) + ann
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(line)
}

func textBlock(ts, mark, text string, style lipgloss.Style, w, maxLines int) []string {
	var out []string
	for i, l := range wrap(text, w, maxLines) {
		if i == 0 {
			out = append(out, ts+"  "+mark+" "+style.Render(l))
		} else {
			out = append(out, strings.Repeat(" ", tsW+4)+style.Render(l))
		}
	}
	return out
}

// wrap breaks plain text into at most maxLines lines of w cells.
func wrap(s string, w, maxLines int) []string {
	if w < 8 {
		w = 8
	}
	var lines []string
	cur := ""
	push := func() {
		lines = append(lines, cur)
		cur = ""
	}
	for _, word := range strings.Fields(s) {
		for runewidth.StringWidth(word) > w {
			if cur != "" {
				push()
			}
			cut := runewidth.Truncate(word, w, "")
			lines = append(lines, cut)
			word = word[len(cut):]
		}
		switch {
		case cur == "":
			cur = word
		case runewidth.StringWidth(cur)+1+runewidth.StringWidth(word) <= w:
			cur += " " + word
		default:
			push()
			cur = word
		}
	}
	if cur != "" {
		push()
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = sanitize.Truncate(lines[maxLines-1]+" …", w)
	}
	return lines
}

func (m Model) detailFooter() string {
	keys := "⏎ vai al tab · u eri qui · t filo · d diff · x subagent · f segui · n/p sessione · esc elenco"
	mode := sAccent.Render("● dal vivo")
	if !m.det.follow {
		mode = sYellow.Render("❚❚ in pausa (f per seguire)")
	}
	parts := []string{mode, sDim.Render(keys)}
	if m.status != "" {
		parts = append(parts, sRed.Render(sanitize.Line(m.status, 80)))
	}
	return lipgloss.NewStyle().MaxWidth(m.w).Render(" " + strings.Join(parts, sDim.Render("  ·  ")))
}
