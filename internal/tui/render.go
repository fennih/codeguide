package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/fennih/codeguide/internal/i18n"
	"github.com/fennih/codeguide/internal/model"
	"github.com/fennih/codeguide/internal/sanitize"
	"github.com/fennih/codeguide/internal/subagents"
	"github.com/fennih/codeguide/internal/toolfmt"
	"github.com/fennih/codeguide/internal/transcript"
	"github.com/fennih/codeguide/internal/usage"
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
	return i18n.T("%dd", int(d.Hours()/24))
}

func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return i18n.T("now")
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return i18n.T("%dd", int(d.Hours()/24))
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
	addWrapped(i18n.T("goal   "), sDim, v.Goal(), sBold, 3)
	if v.Summary.Brief != "" {
		addWrapped(i18n.T("why    "), sDim, v.Summary.Brief, lipgloss.NewStyle(), 5)
	}
	if v.Pending != "" {
		addWrapped("▲ ", sYellow, v.Pending, sYellow, 3)
	} else if v.Summary.NeedsUser && v.Summary.Ask != "" {
		addWrapped("▲ ", sYellow, v.Summary.Ask, sYellow, 3)
	}
	if v.News.Any() {
		add(cell(sBlue.Render(i18n.T("news since ")+ago(v.Looked, now)+": "), newsText(v.News), sBlue, inner))
	}
	if v.Lane != model.LaneWorking && v.Summary.Outcome != "" {
		addWrapped("✓ ", sGreen, v.Summary.Outcome, lipgloss.NewStyle(), 3)
	}
	if v.Lane == model.LaneWorking {
		if v.Summary.Now != "" {
			addWrapped(i18n.T("now    "), sAccent, v.Summary.Now, lipgloss.NewStyle(), 2)
		}
		if say := lastSay(v); say != "" {
			addWrapped("▸ ", sDim, say, sIntent, 3)
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
			if c := ctxShort(s.Context); c != "" {
				state = " " + c + state
			}
		}
		head := sCyan.Render("↳ "+sanitize.Truncate(s.Type, 16)) + " "
		mission := wrap(subMission(v, s), max(8, inner-width(head)-width(state)), 2)
		if len(mission) == 0 {
			mission = []string{""}
		}
		for i, l := range mission {
			switch {
			case i == 0 && len(mission) == 1:
				add(lipgloss.NewStyle().MaxWidth(inner).Render(head + l + state))
			case i == 0:
				add(head + l)
			default:
				add(strings.Repeat(" ", width(head)) + l + state)
			}
		}
		pad := "    "
		switch {
		case s.Running && s.Tool != "":
			add(cell(pad+sAccent.Render("● ")+sBold.Render(toolfmt.Verb(s.Tool))+" ", s.Detail, sDim, inner))
		case !s.Running && s.Result != "":
			add(cell(pad+sGreen.Render("→ "), s.Result, sDim, inner))
		}
	}
	if hidden > 0 {
		add(sDim.Render(i18n.T("  +%d subagents", hidden)))
	}

	if v.TurnEffects != "" {
		add(cell(sCyan.Render(i18n.T("effects this turn: ")), v.TurnEffects, sCyan, inner))
	}

	var foot []string
	if v.Lane == model.LaneWorking {
		foot = append(foot, sAccent.Render(spin(now))+" "+dur(now.Sub(v.TurnStart)))
	}
	if calls > 0 {
		foot = append(foot, i18n.N(calls, "1 action", "%d actions"))
	}
	if plus+minus > 0 {
		foot = append(foot, diffAnn(plus, minus))
	}
	if running > 0 {
		foot = append(foot, i18n.N(running, "1 subagent active", "%d subagents active"))
	}
	left := sDim.Render(strings.Join(foot, " · "))
	if c := ctxLong(v.Context); c != "" && width(left)+width(c)+2 <= inner {
		add(left + strings.Repeat(" ", inner-width(left)-width(c)) + c)
	} else if len(foot) > 0 {
		add(left)
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
	if n.Turns > 0 {
		parts = append(parts, i18n.N(n.Turns, "1 turn", "%d turns"))
	}
	if n.Calls > 0 {
		parts = append(parts, i18n.N(n.Calls, "1 action", "%d actions"))
	}
	if n.Errors > 0 {
		parts = append(parts, i18n.N(n.Errors, "1 error", "%d errors"))
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
	if c := ctxShort(v.Context); c != "" && restW > 40 {
		const ctxW = 9
		line += c + strings.Repeat(" ", max(1, ctxW-width(c)))
		restW -= ctxW
	}
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

// tokens formats a token count as 950, 84k or 1.2M.
func tokens(n int) string {
	switch {
	case n >= 1_000_000:
		s := fmt.Sprintf("%.1f", float64(n)/1_000_000)
		return strings.TrimSuffix(s, ".0") + "M"
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

func levelStyle(pct int, severe bool) lipgloss.Style {
	switch {
	case severe || pct >= 85:
		return sRed
	case pct >= 60:
		return sYellow
	}
	return sDim
}

// gauge draws a bar of n cells filled to pct.
func gauge(pct, n int) string {
	full := min(n, (pct*n+50)/100)
	if pct > 0 && full == 0 {
		full = 1
	}
	return strings.Repeat("▰", full) + strings.Repeat("▱", n-full)
}

// ctxLong reads "ctx ▰▰▱▱▱▱▱▱ 23% 46k/200k".
func ctxLong(c usage.Context) string {
	if !c.Known() {
		return ""
	}
	p := c.Percent()
	return levelStyle(p, false).Render(fmt.Sprintf("ctx %s %d%%", gauge(p, 8), p)) +
		sDim.Render(" "+tokens(c.Tokens)+"/"+tokens(c.Window))
}

// ctxShort reads "ctx 23%".
func ctxShort(c usage.Context) string {
	if !c.Known() {
		return ""
	}
	p := c.Percent()
	return levelStyle(p, false).Render(fmt.Sprintf("ctx %d%%", p))
}

// planText lists plan limits, e.g. "5h 12% ↻18:00 · week 6% · Fable 8%".
// A limit whose window already reset shows no percentage, since the cached
// figure belongs to the previous window.
func planText(p usage.Plan, now time.Time, withAge bool) string {
	if len(p.Limits) == 0 {
		return ""
	}
	var parts []string
	for _, l := range p.Limits {
		if !l.ResetsAt.IsZero() && now.After(l.ResetsAt) {
			parts = append(parts, sDim.Render(l.Label+" ↻"))
			continue
		}
		s := levelStyle(l.Percent, l.Severe).Render(fmt.Sprintf("%s %d%%", l.Label, l.Percent))
		if l.Label == "5h" && !l.ResetsAt.IsZero() {
			s += sDim.Render(" ↻" + l.ResetsAt.Local().Format("15:04"))
		}
		parts = append(parts, s)
	}
	out := strings.Join(parts, sDim.Render(" · "))
	if age := now.Sub(p.FetchedAt); withAge && !p.FetchedAt.IsZero() && age > 15*time.Minute {
		out += sDim.Render(i18n.T(" (as of %s ago)", ago(p.FetchedAt, now)))
	}
	return out
}
