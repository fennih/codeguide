package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"ccwatch/internal/model"
	"ccwatch/internal/sanitize"
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

	add(cell(sDim.Render("goal  "), v.Goal(), sBold, inner))
	if v.Pending != "" {
		add(cell(sYellow.Render("▲ "), v.Pending, sYellow, inner))
	} else if v.Summary.NeedsUser && v.Summary.Ask != "" {
		add(cell(sYellow.Render("▲ "), v.Summary.Ask, sYellow, inner))
	}
	if v.Lane != model.LaneWorking && v.Summary.Outcome != "" {
		add(cell(sGreen.Render("✓ "), v.Summary.Outcome, lipgloss.NewStyle(), inner))
	}
	if v.Lane == model.LaneWorking && v.Preamble != "" {
		add(cell(sDim.Render("▸ "), v.Preamble, sIntent, inner))
	}

	// Current-turn activity; fall back to the last calls when the turn is empty.
	var acts []model.Activity
	for _, a := range v.Activity {
		if !a.At.Before(v.TurnStart) {
			acts = append(acts, a)
		}
	}
	nAct := 5
	if v.Lane != model.LaneWorking {
		nAct = 3
	}
	if len(acts) > nAct {
		acts = acts[len(acts)-nAct:]
	}
	toolW := 0
	for _, a := range acts {
		if a.Detail != "" {
			toolW = max(toolW, runewidth.StringWidth(a.Tool))
		}
	}
	toolW = min(toolW, 12)
	for i, a := range acts {
		dot := sGreen.Render("●")
		if a.Error {
			dot = sRed.Render("✗")
		} else if i == len(acts)-1 && v.Lane == model.LaneWorking {
			dot = sAccent.Render("●")
		} else if v.Lane != model.LaneWorking {
			dot = sDim.Render("●")
		}
		if a.Detail == "" {
			add(cell(dot+" ", a.Tool, sBold, inner))
			continue
		}
		prefix := dot + " " + sBold.Render(fit(a.Tool, toolW)) + " "
		add(cell(prefix, a.Detail, sDim, inner))
	}

	// Subagents: running ones, plus those started in this turn.
	var subs []string
	running := 0
	for _, s := range v.Subagents {
		if !s.Running && s.Started.Before(v.TurnStart) {
			continue
		}
		if s.Running {
			running++
		}
		head := sCyan.Render("↳ "+sanitize.Truncate(s.Type, 16)) + " "
		state := sGreen.Render(" ✓")
		if s.Running {
			state = " " + sAccent.Render("●") + " " + s.Tool
			if s.Detail != "" {
				state += " " + sDim.Render(s.Detail)
			}
		}
		descW := inner - width(head) - min(width(state), inner/2)
		line := head + sanitize.Truncate(s.Description, max(4, descW))
		line = lipgloss.NewStyle().MaxWidth(inner).Render(line + state)
		subs = append(subs, line)
	}
	if len(subs) > 4 {
		subs = append(subs[:4], sDim.Render(fmt.Sprintf("  +%d subagent", len(subs)-4)))
	}
	for _, s := range subs {
		add(s)
	}

	var foot []string
	if v.Lane == model.LaneWorking {
		foot = append(foot, sAccent.Render(spin(now))+" "+dur(now.Sub(v.TurnStart)))
	}
	if v.TurnCalls == 1 {
		foot = append(foot, "1 chiamata")
	} else if v.TurnCalls > 1 {
		foot = append(foot, fmt.Sprintf("%d chiamate", v.TurnCalls))
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

// row renders an idle session as one line of exactly w cells.
func row(v model.View, w int, selected bool, now time.Time) string {
	mark := sDim.Render("  · ")
	if v.Unread {
		mark = sAccent.Render("  • ")
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
