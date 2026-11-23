package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ccwatch/internal/model"
	"ccwatch/internal/sanitize"
)

func (m Model) ledgerRows() []model.EffectRow {
	f := strings.ToLower(m.filter)
	if f == "" {
		return m.snap.Effects
	}
	var out []model.EffectRow
	for _, r := range m.snap.Effects {
		hay := strings.ToLower(r.Sys + " " + r.Act + " " + r.Target + " " + r.Session + " " + r.Project)
		if strings.Contains(hay, f) {
			out = append(out, r)
		}
	}
	return out
}

func (m Model) ledgerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.ledgerRows()
	page := max(1, m.h/2)
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.filter != "" {
			m.filter = ""
			return m, nil
		}
		return m.closeLedger(), nil
	case "e", "backspace", "left", "h":
		return m.closeLedger(), nil
	case "down", "j":
		m.ledSel++
	case "up", "k":
		m.ledSel--
	case "pgdown", "ctrl+d", " ":
		m.ledSel += page
	case "pgup", "ctrl+u":
		m.ledSel -= page
	case "home", "g":
		m.ledSel = 0
	case "end", "G":
		m.ledSel = len(rows) - 1
	case "/":
		m.editing, m.filter = true, ""
	case "enter":
		if m.ledSel >= 0 && m.ledSel < len(rows) {
			r := rows[m.ledSel]
			if !r.Live {
				m.status = "sessione chiusa: nessun dettaglio"
				return m, nil
			}
			m = m.openDetail(r.SessionID)
			m.det.fromLedger = true
			m.inLedger = false
			return m, nil
		}
	}
	m.ledSel = max(0, min(m.ledSel, len(rows)-1))
	return m, nil
}

var sysColor = map[string]lipgloss.Color{
	"Jira": cBlue, "Confluence": cCyan, "Google": cGreen, "Gmail": cGreen, "Slack": cAccent,
	"git": cAccent, "GitHub": cAccent, "AWS": cYellow, "GCP": cYellow, "k8s": cYellow, "IaC": cYellow,
	"HTTP": cRed, "registry": cRed, "browser": cDim,
}

func sysStyle(sys string) lipgloss.Style {
	if c, ok := sysColor[sys]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	return lipgloss.NewStyle().Foreground(cYellow)
}

func dayLabel(t time.Time, now time.Time) string {
	t, now = t.Local(), now.Local()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "oggi"
	case y1 == y2 && m1 == m2 && d1 == d2-1:
		return "ieri"
	}
	days := []string{"dom", "lun", "mar", "mer", "gio", "ven", "sab"}
	return days[t.Weekday()] + " " + t.Format("02/01")
}

func (m Model) ledgerView(now time.Time) string {
	rows := m.ledgerRows()
	w := m.w

	// Totals by system, largest first; unknown MCP tools always shown.
	tot := map[string]int{}
	unknown := 0
	for _, r := range rows {
		if r.Unknown {
			unknown++
			continue
		}
		tot[r.Sys]++
	}
	var syss []string
	for s := range tot {
		syss = append(syss, s)
	}
	sort.Slice(syss, func(i, j int) bool {
		if tot[syss[i]] != tot[syss[j]] {
			return tot[syss[i]] > tot[syss[j]]
		}
		return syss[i] < syss[j]
	})
	var parts []string
	for _, s := range syss {
		parts = append(parts, sysStyle(s).Render(fmt.Sprintf("%s %d", s, tot[s])))
	}
	parts = append(parts, sYellow.Render(fmt.Sprintf("non classificati %d", unknown)))
	span := ""
	if n := len(rows); n > 0 {
		span = fmt.Sprintf(" · dal %s", rows[n-1].At.Local().Format("02/01 15:04"))
	}
	head := []string{
		m.header(now),
		" " + sHeader.Render("Effetti esterni") + sDim.Render(fmt.Sprintf("  %d%s", len(rows), span)),
		lipgloss.NewStyle().MaxWidth(w).Render(" " + strings.Join(parts, sDim.Render(" · "))),
		sDim.Render(sanitize.Truncate("  elenco minimo: script e comandi non riconosciuti possono scrivere senza comparire qui", w)),
	}

	sessW := min(34, max(14, w/4))
	var lines []string
	selLine := 0
	day := ""
	for i, r := range rows {
		if d := dayLabel(r.At, now); d != day {
			day = d
			lines = append(lines, section(d, w))
		}
		if i == m.ledSel {
			selLine = len(lines)
		}
		lines = append(lines, ledgerLine(r, w, sessW, i == m.ledSel))
	}
	if len(rows) == 0 {
		lines = append(lines, "", sDim.Render("  nessun effetto esterno nel periodo caricato"))
	}

	bodyH := max(1, m.h-len(head)-1)
	top := 0
	if selLine >= bodyH {
		top = selLine - bodyH + 2
	}
	top = max(0, min(top, max(0, len(lines)-bodyH)))
	visible := lines[top:min(len(lines), top+bodyH)]
	for len(visible) < bodyH {
		visible = append(visible, "")
	}
	out := append(head, visible...)
	return strings.Join(append(out, m.ledgerFooter()), "\n")
}

func ledgerLine(r model.EffectRow, w, sessW int, selected bool) string {
	status := sFaint.Render("?")
	switch r.Status {
	case 'o':
		status = sGreen.Render("✓")
	case 'e':
		status = sRed.Render("✗")
	}
	act := sDim.Render(fit(r.Act, 12))
	if r.Unknown {
		act = sYellow.Render(fit("non classif.", 12))
	}
	left := "  " + sFaint.Render(r.At.Local().Format("15:04")) + "  " + sysStyle(r.Sys).Render(fit(r.Sys, 11)) + " " + act + " "
	sess := r.Project
	if r.Session != "" {
		sess += " · " + r.Session
	}
	sessStyled := sDim.Render(fit(sess, sessW))
	if !r.Live {
		sessStyled = sFaint.Render(fit(sess, sessW))
	}
	tw := w - width(left) - sessW - 4
	target := r.Target
	tStyle := lipgloss.NewStyle()
	if target == "" {
		target, tStyle = "(dettagli non disponibili)", sFaint
	}
	line := left + tStyle.Render(fit(target, max(4, tw))) + "  " + sessStyled + " " + status
	line = lipgloss.NewStyle().MaxWidth(w).Render(line)
	if pad := w - width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	if selected {
		return sSelBg.Render(line)
	}
	return line
}

func (m Model) ledgerFooter() string {
	var parts []string
	if m.editing {
		parts = append(parts, sAccent.Render("filtro: ")+m.filter+"▏")
	} else if m.filter != "" {
		parts = append(parts, sAccent.Render("filtro: ")+m.filter)
	}
	if m.status != "" {
		parts = append(parts, sRed.Render(sanitize.Line(m.status, 80)))
	}
	parts = append(parts, sDim.Render("⏎ apri la sessione · / filtra · ↑↓ scorri · ✓ riuscito ✗ errore ? esito sconosciuto · esc elenco"))
	return lipgloss.NewStyle().MaxWidth(m.w).Render(" " + strings.Join(parts, sDim.Render("  ·  ")))
}
