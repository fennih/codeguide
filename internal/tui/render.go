package tui

import (
	"fmt"
	"regexp"
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

var (
	cAccent = lipgloss.Color("#D97757")
	cGreen  = lipgloss.Color("#5FB76F")
	cRed    = lipgloss.Color("#E06C75")
	cYellow = lipgloss.Color("#E5C07B")
	cBlue   = lipgloss.Color("#61AFEF")
	cCyan   = lipgloss.Color("#56B6C2")
	cDim    = lipgloss.Color("244")
	cFaint  = lipgloss.Color("238")

	sBold    = lipgloss.NewStyle().Bold(true)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sAccent  = lipgloss.NewStyle().Foreground(cAccent)
	sGreen   = lipgloss.NewStyle().Foreground(cGreen)
	sRed     = lipgloss.NewStyle().Foreground(cRed)
	sYellow  = lipgloss.NewStyle().Foreground(cYellow)
	sBlue    = lipgloss.NewStyle().Foreground(cBlue)
	sCyan    = lipgloss.NewStyle().Foreground(cCyan)
	sIntent  = lipgloss.NewStyle().Foreground(cDim).Italic(true)
	sSelBg   = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	sHeader  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sSection = lipgloss.NewStyle().Foreground(cDim).Bold(true)
)

var spinner = []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"}

func spin(now time.Time) string {
	return spinner[(now.UnixMilli()/150)%int64(len(spinner))]
}

func dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dg", int(d.Hours()/24))
}

func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "ora"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dg", int(d.Hours()/24))
}

func width(s string) int { return lipgloss.Width(s) }

// fit pads or truncates plain text to exactly w cells.
func fit(s string, w int) string {
	s = sanitize.Truncate(s, w)
	return s + strings.Repeat(" ", max(0, w-runewidth.StringWidth(s)))
}

// cell joins a styled prefix with plain text truncated to the remaining width.
func cell(prefix string, text string, style lipgloss.Style, w int) string {
	rest := w - width(prefix)
	if rest <= 0 {
		return prefix
	}
	return prefix + style.Render(sanitize.Truncate(text, rest))
}

// tile renders one session as a bordered box of exactly w cells per line.
func tile(v model.View, w int, selected bool, now time.Time) []string {
	inner := w - 4
	border := sFaint
	switch {
	case selected:
		border = sAccent
	case v.Lane == model.LaneNeedsYou:
		border = sYellow
	}

	glyph := sDim.Render("·")
	switch v.Lane {
	case model.LaneWorking:
		glyph = sAccent.Render(spin(now))
	case model.LaneNeedsYou:
		glyph = sYellow.Render("▲")
	}
	right := v.Project
	if !v.TurnStart.IsZero() && v.Lane == model.LaneWorking {
		right += " · " + dur(now.Sub(v.TurnStart))
	} else {
		right += " · " + ago(v.LastAt, now)
	}
	right = sanitize.Truncate(right, max(8, inner/2))
	unread := ""
	if v.Unread {
		unread = sAccent.Render(" •")
	}
	// Top line: "╭─ " glyph " " title unread " " fill " " right " ─╮" == w cells.
	titleMax := w - 10 - width(glyph) - width(unread) - runewidth.StringWidth(right)
	titleStyled := sBold.Render(sanitize.Truncate(v.Title, max(4, titleMax))) + unread
	fill := w - 9 - width(glyph) - width(titleStyled) - runewidth.StringWidth(right)
	top := border.Render("╭─ ") + glyph + " " + titleStyled + " " +
		border.Render(strings.Repeat("─", max(1, fill))) + " " + sDim.Render(right) + border.Render(" ─╮")

	var body []string
	add := func(s string) {
		pad := inner - width(s)
		if pad < 0 {
			s = lipgloss.NewStyle().MaxWidth(inner).Render(s)
			pad = inner - width(s)
		}
		body = append(body, border.Render("│")+" "+s+strings.Repeat(" ", max(0, pad))+" "+border.Render("│"))
	}

	addWrapped := func(label string, labelStyle lipgloss.Style, text string, style lipgloss.Style, maxLines int) {
		prefix := labelStyle.Render(label)
		indent := strings.Repeat(" ", width(prefix))
		for i, l := range wrap(text, inner-width(prefix), maxLines) {
			if i == 0 {
				add(prefix + style.Render(l))
			} else {
				add(indent + style.Render(l))
			}
		}
	}
	addWrapped("goal   ", sDim, v.Goal(), sBold, 2)
	if v.Summary.Brief != "" {
		addWrapped("perché ", sDim, v.Summary.Brief, lipgloss.NewStyle(), 3)
	}
	if v.Pending != "" {
		add(cell(sYellow.Render("▲ "), v.Pending, sYellow, inner))
	} else if v.Summary.NeedsUser && v.Summary.Ask != "" {
		add(cell(sYellow.Render("▲ "), v.Summary.Ask, sYellow, inner))
	}
	if v.News.Any() {
		add(cell(sBlue.Render("novità da "+ago(v.Looked, now)+": "), newsText(v.News), sBlue, inner))
	}
	if v.Lane != model.LaneWorking && v.Summary.Outcome != "" {
		add(cell(sGreen.Render("✓ "), v.Summary.Outcome, lipgloss.NewStyle(), inner))
	}
	if v.Lane == model.LaneWorking {
		if v.Summary.Now != "" {
			add(cell(sAccent.Render("ora   "), v.Summary.Now, lipgloss.NewStyle(), inner))
		}
		if say := lastSay(v); say != "" {
			add(cell(sDim.Render("▸ "), say, sIntent, inner))
		}
	}

	nAct := 5
	if v.Lane != model.LaneWorking {
		nAct = 3
	}
	acts, calls, plus, minus := tileActs(v, nAct)
	verbW := 0
	for _, a := range acts {
		if a.detail != "" {
			verbW = max(verbW, runewidth.StringWidth(a.verb))
		}
	}
	verbW = min(verbW, 12)
	for _, a := range acts {
		if a.detail == "" {
			add(cell(a.dot+" ", a.verb, sBold, inner))
			continue
		}
		prefix := a.dot + " " + sBold.Render(fit(a.verb, verbW)) + " "
		if a.ann == "" {
			add(cell(prefix, a.detail, sDim, inner))
			continue
		}
		rest := inner - width(prefix) - width(a.ann) - 1
		text := sDim.Render(sanitize.Truncate(a.detail, max(4, rest)))
		pad := max(1, inner-width(prefix)-width(text)-width(a.ann))
		add(prefix + text + strings.Repeat(" ", pad) + a.ann)
	}

	// Subagents: running ones, plus those started in this turn. Each gets its
	// mission and, below it, the current tool or the final result.
	running, shown, hidden := 0, 0, 0
	for _, s := range v.Subagents {
		if !s.Running && s.Started.Before(v.TurnStart) {
			continue
		}
		if s.Running {
			running++
		}
		if shown == 3 {
			hidden++
			continue
		}
		shown++
		state := sGreen.Render(" ✓")
		if s.Running {
			state = " " + sAccent.Render("●")
		}
		head := sCyan.Render("↳ "+sanitize.Truncate(s.Type, 16)) + " "
		line := head + sanitize.Truncate(subMission(v, s), max(4, inner-width(head)-width(state)))
		add(lipgloss.NewStyle().MaxWidth(inner).Render(line + state))
		pad := "    "
		switch {
		case s.Running && s.Tool != "":
			add(cell(pad+sAccent.Render("● ")+sBold.Render(toolfmt.Verb(s.Tool))+" ", s.Detail, sDim, inner))
		case !s.Running && s.Result != "":
			add(cell(pad+sGreen.Render("→ "), s.Result, sDim, inner))
		}
	}
	if hidden > 0 {
		add(sDim.Render(fmt.Sprintf("  +%d subagent", hidden)))
	}

	if v.TurnEffects != "" {
		add(cell(sCyan.Render("effetti nel turno: "), v.TurnEffects, sCyan, inner))
	}

	var foot []string
	if v.Lane == model.LaneWorking {
		foot = append(foot, sAccent.Render(spin(now))+" "+dur(now.Sub(v.TurnStart)))
	}
	if calls == 1 {
		foot = append(foot, "1 azione")
	} else if calls > 1 {
		foot = append(foot, fmt.Sprintf("%d azioni", calls))
	}
	if plus+minus > 0 {
		foot = append(foot, diffAnn(plus, minus))
	}
	if running > 0 {
		foot = append(foot, fmt.Sprintf("%d subagent attivi", running))
	}
	if len(foot) > 0 {
		add(sDim.Render(strings.Join(foot, " · ")))
	}

	bottom := border.Render("╰" + strings.Repeat("─", w-2) + "╯")
	return append(append([]string{top}, body...), bottom)
}

type actRow struct {
	dot    string
	verb   string
	detail string
	ann    string // styled, right-aligned
}

// tileActs lists the last n actions of the current turn, from the transcript
// when it is followed and from cmux's Feed otherwise.
func tileActs(v model.View, n int) (rows []actRow, calls, plus, minus int) {
	working := v.Lane == model.LaneWorking
	if len(v.Timeline) > 0 {
		var tools []transcript.Event
		for _, e := range v.Timeline[transcript.TurnIndex(v.Timeline)+1:] {
			if e.Kind == transcript.Tool {
				tools = append(tools, e)
				plus += e.Plus
				minus += e.Minus
			}
		}
		calls = len(tools)
		if len(tools) > n {
			tools = tools[len(tools)-n:]
		}
		for _, e := range tools {
			r := actRow{verb: e.Verb, detail: e.Detail}
			switch {
			case e.Error:
				r.dot = sRed.Render("✗")
			case !e.Done && working:
				r.dot = sAccent.Render("●")
			case working:
				r.dot = sGreen.Render("●")
			default:
				r.dot = sDim.Render("●")
			}
			if e.Plus+e.Minus > 0 {
				r.ann = diffAnn(e.Plus, e.Minus)
			}
			rows = append(rows, r)
		}
		return rows, calls, plus, minus
	}
	var acts []model.Activity
	for _, a := range v.Activity {
		if !a.At.Before(v.TurnStart) {
			acts = append(acts, a)
		}
	}
	if len(acts) > n {
		acts = acts[len(acts)-n:]
	}
	for i, a := range acts {
		r := actRow{verb: toolfmt.Verb(a.Tool), detail: a.Detail, dot: sGreen.Render("●")}
		switch {
		case a.Error:
			r.dot = sRed.Render("✗")
		case i == len(acts)-1 && working:
			r.dot = sAccent.Render("●")
		case !working:
			r.dot = sDim.Render("●")
		}
		rows = append(rows, r)
	}
	return rows, v.TurnCalls, 0, 0
}

// lastSay is the latest thing the agent said in the current turn, falling
// back to cmux's preamble.
func lastSay(v model.View) string {
	if len(v.Timeline) > 0 {
		for i := len(v.Timeline) - 1; i > transcript.TurnIndex(v.Timeline); i-- {
			if e := v.Timeline[i]; e.Kind == transcript.Say {
				return e.Text
			}
		}
	}
	return v.Preamble
}

func diffAnn(plus, minus int) string {
	var parts []string
	if plus > 0 {
		parts = append(parts, sGreen.Render(fmt.Sprintf("+%d", plus)))
	}
	if minus > 0 {
		parts = append(parts, sRed.Render(fmt.Sprintf("−%d", minus)))
	}
	return strings.Join(parts, " ")
}

// subMission is the best one-line purpose of a subagent: the model-written
// mission, else the first sentence of its brief, else the parent's label.
func subMission(v model.View, s subagents.Agent) string {
	if m := v.SubMissions[s.ID]; m != "" {
		return m
	}
	if s.Task != "" {
		t := s.Task
		if loc := sentenceEnd.FindStringIndex(t); loc != nil && loc[0] > 20 && loc[0] < 200 {
			t = t[:loc[0]+1]
		}
		return t
	}
	return s.Description
}

var sentenceEnd = regexp.MustCompile(`[.?!](\s|$)`)

func newsText(n model.News) string {
	var parts []string
	switch {
	case n.Turns == 1:
		parts = append(parts, "1 turno")
	case n.Turns > 1:
		parts = append(parts, fmt.Sprintf("%d turni", n.Turns))
	}
	switch {
	case n.Calls == 1:
		parts = append(parts, "1 azione")
	case n.Calls > 1:
		parts = append(parts, fmt.Sprintf("%d azioni", n.Calls))
	}
	switch {
	case n.Errors == 1:
		parts = append(parts, "1 errore")
	case n.Errors > 1:
		parts = append(parts, fmt.Sprintf("%d errori", n.Errors))
	}
	return strings.Join(parts, " · ")
}

// row renders an idle session as one line of exactly w cells.
func row(v model.View, w int, selected bool, now time.Time) string {
	mark := sDim.Render("  · ")
	if v.Unread {
		mark = sAccent.Render("  • ")
	}
	if v.News.Any() {
		mark = sBlue.Render("  ✦ ")
	}
	if selected {
		mark = sAccent.Render("  › ")
	}
	titleW := min(34, max(12, w/3))
	projW := min(18, max(8, w/6))
	ageW := 4
	text := v.Summary.Outcome
	style := lipgloss.NewStyle()
	if text == "" {
		text = v.Goal()
		style = sDim
	}
	restW := w - 4 - titleW - projW - ageW - 3
	line := mark + sBold.Render(fit(v.Title, titleW)) + " " + sDim.Render(fit(v.Project, projW)) + " " +
		sDim.Render(fit(ago(v.LastAt, now), ageW)) + " "
	if restW > 6 {
		line += style.Render(sanitize.Truncate(text, restW))
	}
	line = lipgloss.NewStyle().MaxWidth(w).Render(line)
	if pad := w - width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	if selected {
		return sSelBg.Render(line)
	}
	return line
}

func section(label string, w int) string {
	l := sSection.Render("── " + label + " ")
	return l + sFaint.Render(strings.Repeat("─", max(0, w-width(l))))
}
