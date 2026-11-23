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

// Options are callbacks into the collector.
type Options struct {
	Resummarize func(sessionID string)
	Focus       func(sessionID string) // session shown in the detail view, "" for none
	Look        func(sessionID string) // the user jumped to a session's tab
}

// hit is a clickable screen rectangle for one session.
type hit struct {
	y0, y1, x0, x1 int
	sid            string
}

type Model struct {
	cmux cmux.Client
	opt  Options

	snap     Snapshot
	w, h     int
	selected string // session id
	byProj   bool
	showOld  bool
	filter   string
	editing  bool
	help     bool
	status   string
	inDetail bool
	det      detail
	newsOnly bool
}

func New(c cmux.Client, o Options) Model {
	return Model{cmux: c, opt: o, w: 100, h: 40}
}

// WithDetail opens the detail view of one session (used by --frame).
func (m Model) WithDetail(sid string) Model {
	return m.openDetail(sid)
}

func (m Model) openDetail(sid string) Model {
	m.inDetail, m.det, m.status = true, detail{sid: sid, follow: true}, ""
	// Keep the previous look time: the divider marks what was new on opening.
	if v, ok := m.viewByID(sid); ok {
		m.det.lookedAt = v.Looked
	}
	if m.opt.Focus != nil {
		m.opt.Focus(sid)
	}
	return m
}

func (m Model) closeDetail() Model {
	m.inDetail, m.selected, m.status = false, m.det.sid, ""
	if m.opt.Focus != nil {
		m.opt.Focus("")
	}
	return m
}

func (m Model) jump(v model.View) tea.Cmd {
	if m.opt.Look != nil {
		m.opt.Look(v.SessionID)
	}
	c := m.cmux
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return focusDone{c.Focus(ctx, v.WorkspaceID, v.PanelID)}
	}
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
	case tea.MouseMsg:
		return m.mouse(msg)
	case tea.KeyMsg:
		if m.inDetail {
			return m.detailKey(msg)
		}
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
	case "enter", " ":
		if idx >= 0 {
			return m.openDetail(items[idx].SessionID), nil
		}
	case "o":
		if idx >= 0 {
			m.status = ""
			return m, m.jump(items[idx])
		}
	case "g":
		m.byProj = !m.byProj
	case "tab":
		m.showOld = !m.showOld
	case "r":
		m.newsOnly = !m.newsOnly
	case "/":
		m.editing, m.filter = true, ""
	case "esc":
		m.filter, m.newsOnly = "", false
	case "s":
		if idx >= 0 && m.opt.Resummarize != nil {
			m.opt.Resummarize(items[idx].SessionID)
			m.status = "riassunto richiesto"
		}
	case "?":
		m.help = !m.help
	}
	return m, nil
}

func (m Model) detailKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "backspace", "left", "h":
		return m.closeDetail(), nil
	case "enter", "o":
		if v, ok := m.viewByID(m.det.sid); ok {
			m.status = ""
			return m, m.jump(v)
		}
	case "up", "k":
		return m.detailScroll(-1), nil
	case "down", "j":
		return m.detailScroll(1), nil
	case "pgup", "ctrl+u":
		return m.detailScroll(-(m.h / 2)), nil
	case "pgdown", "ctrl+d", " ":
		return m.detailScroll(m.h / 2), nil
	case "home", "g":
		m.det.follow, m.det.top = false, 0
	case "end", "G", "f":
		m.det.follow = true
	case "d":
		m.det.diffs = !m.det.diffs
	case "t":
		m.det.turns = !m.det.turns
		m.det.follow = !m.det.turns
		m.det.top = 0
	case "u":
		if v, ok := m.viewByID(m.det.sid); ok {
			if _, div := m.detailLines(v, time.Now()); div >= 0 {
				m.det.follow, m.det.top = false, max(0, div-2)
			}
		}
	case "x":
		m.det.subs = !m.det.subs
	case "n", "p":
		items := m.items()
		if len(items) == 0 {
			break
		}
		i := indexOf(items, m.det.sid)
		if k.String() == "n" {
			i = (i + 1) % len(items)
		} else {
			i = (i - 1 + len(items)) % len(items)
		}
		return m.openDetail(items[i].SessionID), nil
	case "s":
		if m.opt.Resummarize != nil {
			m.opt.Resummarize(m.det.sid)
			m.status = "riassunto richiesto"
		}
	}
	return m, nil
}

// detailScroll moves the timeline window; reaching the bottom resumes follow.
func (m Model) detailScroll(d int) Model {
	v, ok := m.viewByID(m.det.sid)
	if !ok {
		return m
	}
	now := time.Now()
	lines, _ := m.detailLines(v, now)
	total := len(lines)
	bodyH := max(1, m.h-len(detailHead(v, m.w, now))-1)
	maxTop := max(0, total-bodyH)
	t := m.detailStart(total, bodyH) + d
	if t >= maxTop {
		m.det.follow = true
		return m
	}
	m.det.follow, m.det.top = false, max(0, t)
	return m
}

func (m Model) mouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if m.inDetail {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m.detailScroll(-3), nil
		case tea.MouseButtonWheelDown:
			return m.detailScroll(3), nil
		}
		return m, nil
	}
	items := m.items()
	if len(items) == 0 {
		return m, nil
	}
	idx := indexOf(items, m.selected)
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.selected = items[max(idx-1, 0)].SessionID
	case tea.MouseButtonWheelDown:
		m.selected = items[min(idx+1, len(items)-1)].SessionID
	case tea.MouseButtonLeft:
		_, hits := m.listFrame(time.Now())
		for _, h := range hits {
			if msg.Y < h.y0 || msg.Y >= h.y1 || msg.X < h.x0 || msg.X >= h.x1 {
				continue
			}
			// A click on the selected session opens it; otherwise it selects.
			if h.sid == items[idx].SessionID {
				return m.openDetail(h.sid), nil
			}
			m.selected = h.sid
			break
		}
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
		if m.newsOnly && !v.News.Any() {
			continue
		}
		out = append(out, v)
	}
	if m.newsOnly {
		score := func(n model.News) int { return n.Turns*10 + n.Errors*3 + n.Calls }
		sort.SliceStable(out, func(i, j int) bool { return score(out[i].News) > score(out[j].News) })
		return out
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
	if m.inDetail {
		return m.detailView(now)
	}
	out, _ := m.listFrame(now)
	return strings.Join(out, "\n")
}

// listFrame renders the session list and the screen rectangle of each session.
func (m Model) listFrame(now time.Time) ([]string, []hit) {
	items := m.items()
	selIdx := indexOf(items, m.selected)
	selID := ""
	if selIdx >= 0 {
		selID = items[selIdx].SessionID
	}

	var lines []string
	var hits []hit
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
			for c, v := range vs[i:end] {
				t := tile(v, tileW, v.SessionID == selID, now)
				blocks = append(blocks, strings.Join(t, "\n"))
				hits = append(hits, hit{y0: start, y1: start + len(t), x0: c * tileW, x1: (c + 1) * tileW, sid: v.SessionID})
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
			hits = append(hits, hit{y0: start, y1: start + 1, x0: 0, x1: m.w, sid: v.SessionID})
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
				hits = append(hits, hit{y0: start, y1: start + 1, x0: 0, x1: m.w, sid: v.SessionID})
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

	// The header takes screen row 0; body line i lands on row 1+i-scroll.
	for i := range hits {
		hits[i].y0 += 1 - scroll
		hits[i].y1 += 1 - scroll
	}

	out := []string{m.header(now)}
	out = append(out, visible...)
	if m.help {
		out = append(out,
			sDim.Render(" ↑↓/jk seleziona   ⏎ o click apre il dettaglio dal vivo   o vai al tab in cmux   / filtra"),
			sDim.Render(" r solo sessioni con novità   g ordina per progetto   tab sessioni vecchie   s rigenera goal/esito   q esci"),
			sDim.Render(" ▲ serve te   ● azione   ✗ errore   +N −M righe modificate   ↳ subagent   ✓ esito   • non letto"),
		)
	}
	out = append(out, m.footer())
	return out, hits
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
	nNews := 0
	for _, v := range m.snap.Views {
		if v.News.Any() && v.Lane != model.LaneStale {
			nNews++
		}
	}
	switch {
	case m.newsOnly:
		parts = append(parts, sBlue.Render("solo sessioni con novità (r per tutte)"))
	case nNews == 1:
		parts = append(parts, sBlue.Render("1 sessione con novità · r per vederla"))
	case nNews > 1:
		parts = append(parts, sBlue.Render(fmt.Sprintf("%d sessioni con novità · r per vederle", nNews)))
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
	if msg == "" {
		parts = append(parts, sDim.Render("⏎ dettaglio · o vai al tab · / filtra · g progetto · tab vecchie · ? aiuto · q esci"))
	}
	return lipgloss.NewStyle().MaxWidth(m.w).Render(" " + strings.Join(parts, sDim.Render("  ·  ")))
}
