// Package tui renders sessions as a grid of tiles grouped by lane.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ccwatch/internal/cmux"
	"ccwatch/internal/model"
	"ccwatch/internal/sanitize"
	"ccwatch/internal/summary"
)

const minTile = 56

// Snapshot is pushed by the collector loop.
type Snapshot struct {
	Views      []model.View
	CallsToday int
	Stats      summary.Stats
	LLM        bool
	Err        string
	At         time.Time
}

type focusDone struct{ err error }
type frame time.Time

type Model struct {
	cmux        cmux.Client
	resummarize func(sessionID string)

	snap     Snapshot
	w, h     int
	selected string // session id
	byProj   bool
	showOld  bool
	filter   string
	editing  bool
	help     bool
	status   string
}

func New(c cmux.Client, resummarize func(string)) Model {
	return Model{cmux: c, resummarize: resummarize, w: 100, h: 40}
}

func (m Model) Init() tea.Cmd { return animate() }

func animate() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg { return frame(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case Snapshot:
		m.snap = msg
	case frame:
		return m, animate()
	case focusDone:
		if msg.err != nil {
			m.status = sanitize.Line("salto non riuscito: "+msg.err.Error(), 120)
		}
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editing {
		switch k.Type {
		case tea.KeyEnter:
			m.editing = false
		case tea.KeyEsc:
			m.editing, m.filter = false, ""
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			if len(m.filter) < 60 {
				m.filter += sanitize.Line(string(k.Runes), 0)
				if k.Type == tea.KeySpace {
					m.filter += " "
				}
			}
		}
		return m, nil
	}
	items := m.items()
	idx := indexOf(items, m.selected)
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "down", "j", "right", "l":
		if len(items) > 0 {
			m.selected = items[min(idx+1, len(items)-1)].SessionID
		}
	case "up", "k", "left", "h":
		if len(items) > 0 {
			m.selected = items[max(idx-1, 0)].SessionID
		}
	case "home":
		if len(items) > 0 {
			m.selected = items[0].SessionID
		}
	case "end":
		if len(items) > 0 {
			m.selected = items[len(items)-1].SessionID
		}
	case "enter":
		if idx >= 0 {
			v := items[idx]
			c := m.cmux
			m.status = ""
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				return focusDone{c.Focus(ctx, v.WorkspaceID, v.PanelID)}
			}
		}
	case "g":
		m.byProj = !m.byProj
	case "tab":
		m.showOld = !m.showOld
	case "/":
		m.editing, m.filter = true, ""
	case "esc":
		m.filter = ""
	case "s":
		if idx >= 0 && m.resummarize != nil {
			m.resummarize(items[idx].SessionID)
			m.status = "riassunto richiesto"
		}
	case "?":
		m.help = !m.help
	}
	return m, nil
}

func indexOf(items []model.View, id string) int {
	for i, v := range items {
		if v.SessionID == id {
			return i
		}
	}
	if len(items) > 0 {
		return 0
	}
	return -1
}

// items returns the selectable sessions in display order.
func (m Model) items() []model.View {
	var out []model.View
	f := strings.ToLower(m.filter)
	for _, v := range m.snap.Views {
		if v.Lane == model.LaneStale && !m.showOld {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(v.Title+" "+v.Project+" "+v.Goal()), f) {
			continue
		}
		out = append(out, v)
	}
	if m.byProj {
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Lane != out[j].Lane {
				return out[i].Lane < out[j].Lane
			}
			return out[i].Project < out[j].Project
		})
	}
	return out
}

func (m Model) View() string {
	if m.w < 30 || m.h < 8 {
		return "ccwatch: finestra troppo piccola"
	}
	now := time.Now()
	items := m.items()
	selIdx := indexOf(items, m.selected)
	selID := ""
	if selIdx >= 0 {
		selID = items[selIdx].SessionID
	}

	var lines []string
	selStart, selEnd := 0, 0
	mark := func(id string, start int) {
		if id == selID {
			selStart, selEnd = start, len(lines)
		}
	}

	byLane := map[model.Lane][]model.View{}
	for _, v := range items {
		byLane[v.Lane] = append(byLane[v.Lane], v)
	}
	cols := max(1, m.w/minTile)
	tileW := m.w / cols

	for _, lane := range []model.Lane{model.LaneNeedsYou, model.LaneWorking} {
		vs := byLane[lane]
		if len(vs) == 0 {
			continue
		}
		label := "SERVE TE"
		if lane == model.LaneWorking {
			label = "AL LAVORO"
		}
		lines = append(lines, section(fmt.Sprintf("%s (%d)", label, len(vs)), m.w))
		for i := 0; i < len(vs); i += cols {
			end := min(i+cols, len(vs))
			var blocks []string
			start := len(lines)
			for _, v := range vs[i:end] {
				blocks = append(blocks, strings.Join(tile(v, tileW, v.SessionID == selID, now), "\n"))
			}
			joined := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, blocks...), "\n")
			lines = append(lines, joined...)
			for _, v := range vs[i:end] {
				mark(v.SessionID, start)
			}
		}
	}
	if vs := byLane[model.LaneIdle]; len(vs) > 0 {
		lines = append(lines, section(fmt.Sprintf("FERME (%d)", len(vs)), m.w))
		for _, v := range vs {
			start := len(lines)
			lines = append(lines, row(v, m.w, v.SessionID == selID, now))
			mark(v.SessionID, start)
		}
	}
	nOld := 0
	for _, v := range m.snap.Views {
		if v.Lane == model.LaneStale {
			nOld++
		}
	}
	if m.showOld {
		if vs := byLane[model.LaneStale]; len(vs) > 0 {
			lines = append(lines, section(fmt.Sprintf("VECCHIE (%d)", len(vs)), m.w))
			for _, v := range vs {
				start := len(lines)
				lines = append(lines, row(v, m.w, v.SessionID == selID, now))
				mark(v.SessionID, start)
			}
		}
	} else if nOld > 0 {
		lines = append(lines, sDim.Render(fmt.Sprintf("  + %d sessioni inattive da tempo  (tab per mostrarle)", nOld)))
	}
	if len(items) == 0 {
		lines = append(lines, "", sDim.Render("  nessuna sessione Claude Code trovata in cmux"))
	}

	bodyH := m.h - 2
	if m.help {
		bodyH -= 3
	}
	// Scroll only as far as needed to keep the selection visible.
	scroll := 0
	if selEnd-scroll > bodyH {
		scroll = selEnd - bodyH
	}
	if selStart < scroll {
		scroll = selStart
	}
	scroll = max(0, min(scroll, max(0, len(lines)-bodyH)))
	visible := lines[scroll:min(len(lines), scroll+bodyH)]
	for len(visible) < bodyH {
		visible = append(visible, "")
	}

	out := []string{m.header(now)}
	out = append(out, visible...)
	if m.help {
		out = append(out,
			sDim.Render(" ↑↓/jk seleziona   ⏎ vai al tab in cmux   g ordina per progetto   / filtra   esc pulisci filtro"),
			sDim.Render(" tab mostra sessioni vecchie   s rigenera goal/esito della selezionata   ? aiuto   q esci"),
			sDim.Render(" ▲ serve te   ● ultima azione   ↳ subagent   ✓ esito ultimo turno   • non letto in cmux"),
		)
	}
	out = append(out, m.footer())
	return strings.Join(out, "\n")
}

func (m Model) header(now time.Time) string {
	var nWork, nYou, nIdle int
	for _, v := range m.snap.Views {
		switch v.Lane {
		case model.LaneWorking:
			nWork++
		case model.LaneNeedsYou:
			nYou++
		case model.LaneIdle:
			nIdle++
		}
	}
	left := sHeader.Render("ccwatch") + "  " +
		sAccent.Render(fmt.Sprintf("%d al lavoro", nWork)) + sDim.Render(" · ") +
		sYellow.Render(fmt.Sprintf("%d servono te", nYou)) + sDim.Render(" · ") +
		sDim.Render(fmt.Sprintf("%d ferme", nIdle))
	right := fmt.Sprintf("%d chiamate oggi", m.snap.CallsToday)
	if m.snap.LLM {
		right += fmt.Sprintf(" · haiku %d ($%.3f)", m.snap.Stats.Calls, m.snap.Stats.CostUSD)
		if m.snap.Stats.Queued > 0 {
			right += fmt.Sprintf(" +%d", m.snap.Stats.Queued)
		}
	}
	right += "  " + now.Format("15:04:05")
	right = sDim.Render(right)
	gap := m.w - width(left) - width(right)
	if gap < 1 {
		return lipgloss.NewStyle().MaxWidth(m.w).Render(left)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) footer() string {
	var parts []string
	if m.editing {
		parts = append(parts, sAccent.Render("filtro: ")+m.filter+"▏")
	} else if m.filter != "" {
		parts = append(parts, sAccent.Render("filtro: ")+m.filter)
	}
	if m.byProj {
		parts = append(parts, sBlue.Render("per progetto"))
	}
	msg := m.status
	if m.snap.Err != "" {
		msg = m.snap.Err
	} else if m.snap.Stats.LastErr != "" {
		msg = "riassunti: " + m.snap.Stats.LastErr
	}
	if msg != "" {
		parts = append(parts, sRed.Render(sanitize.Line(msg, 100)))
	}
	if len(parts) == 0 {
		parts = append(parts, sDim.Render("⏎ vai al tab · / filtra · g progetto · tab vecchie · ? aiuto · q esci"))
	}
	return lipgloss.NewStyle().MaxWidth(m.w).Render(" " + strings.Join(parts, sDim.Render("  ·  ")))
}
