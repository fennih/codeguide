package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ccwatch/internal/app"
	"ccwatch/internal/cmux"
	"ccwatch/internal/model"
	"ccwatch/internal/subagents"
	"ccwatch/internal/summary"
	"ccwatch/internal/tui"
	"ccwatch/internal/workstream"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ccwatch:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var (
		once       = flag.Bool("once", false, "print one JSON snapshot and exit (no model calls)")
		frameSize  = flag.String("frame", "", "print one rendered frame of WIDTHxHEIGHT and exit (no model calls)")
		noLLM      = flag.Bool("no-llm", false, "disable goal/outcome summaries and live narration")
		noNarrate  = flag.Bool("no-narrate", false, "disable the live \"ora:\" narration of working sessions")
		nowPerHour = flag.Int("narrate-per-hour", 40, "cap on live narration calls per hour")
		noMouse    = flag.Bool("no-mouse", false, "do not capture the mouse")
		detailID   = flag.String("detail", "", "with --frame: render the detail view of this session id")
		claudeBin  = flag.String("claude-bin", "", "Claude Code binary for summaries (default: first non-cmux-shim claude in PATH)")
		cmuxBin    = flag.String("cmux-bin", "", "cmux CLI (default: $CMUX_BUNDLED_CLI_PATH or cmux in PATH)")
		modelName  = flag.String("model", "haiku", "model for summaries")
		staleAfter = flag.Duration("stale-after", 24*time.Hour, "idle time after which a session is collapsed")
		maxPerHour = flag.Int("max-per-hour", 60, "cap on summary calls per hour")
		coldStart  = flag.Int("cold-start", 20, "recent sessions to summarize at startup")
		stateDir   = flag.String("state-dir", filepath.Join(home, ".local", "state", "ccwatch"), "cache directory")
		logPath    = flag.String("workstream", filepath.Join(home, ".cmuxterm", "workstream.jsonl"), "cmux Feed log")
	)
	flag.Parse()

	cbin, err := resolveCmux(*cmuxBin)
	if err != nil {
		return err
	}
	cm := cmux.Client{Bin: cbin}
	store := model.NewStore(*staleAfter)
	projects := filepath.Join(home, ".claude", "projects")
	col := &app.Collector{
		Cmux:      cm,
		Tail:      workstream.NewTailer(*logPath),
		Store:     store,
		Subs:      subagents.NewReader(projects),
		Root:      projects,
		ColdStart: *coldStart,
		ColdAge:   24 * time.Hour,
	}
	defer col.Tail.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var prog *tea.Program
	var sum *summary.Summarizer
	// --frame shows cached summaries but never starts the worker.
	useLLM := !*noLLM && !*once
	if useLLM {
		bin, err := resolveClaude(*claudeBin)
		if err != nil {
			return err
		}
		sum, err = summary.New(summary.Config{
			Bin: bin, Model: *modelName, StateDir: *stateDir, MaxPerHour: *maxPerHour, NowPerHour: *nowPerHour,
		}, func(sid string, s model.Summary) {
			store.SetSummary(sid, s)
		})
		if err != nil {
			return err
		}
		col.Sum = sum
		col.Narrate = !*noNarrate
	}

	if err := col.Init(ctx); err != nil {
		return err
	}

	if *once {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			At       time.Time    `json:"at"`
			Err      string       `json:"error,omitempty"`
			Sessions []model.View `json:"sessions"`
		}{time.Now(), col.Err(), store.Views(time.Now())})
	}

	snapshot := func() tui.Snapshot {
		now := time.Now()
		s := tui.Snapshot{Views: store.Views(now), CallsToday: store.CallsToday(now), LLM: sum != nil, Err: col.Err(), At: now}
		if sum != nil {
			s.Stats = sum.Stats()
		}
		return s
	}

	if *frameSize != "" {
		var w, h int
		if _, err := fmt.Sscanf(*frameSize, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			return errors.New("--frame wants WIDTHxHEIGHT")
		}
		base := tui.New(cm, tui.Options{Focus: col.SetFocus})
		if *detailID != "" {
			if !cmux.ValidID(*detailID) {
				return errors.New("--detail wants a session id")
			}
			base = base.WithDetail(*detailID)
			col.Tick(ctx)
			col.Tick(ctx) // the second pass reads the subagents found by the first
		}
		var fm tea.Model = base
		fm, _ = fm.Update(tea.WindowSizeMsg{Width: w, Height: h})
		fm, _ = fm.Update(snapshot())
		fmt.Println(fm.View())
		return nil
	}

	if sum != nil {
		go sum.Run(ctx)
		col.WarmUp()
	}

	m := tui.New(cm, tui.Options{Resummarize: col.Resummarize, Focus: col.SetFocus})
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	if !*noMouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	prog = tea.NewProgram(m, opts...)
	go func() {
		prog.Send(snapshot())
		t := time.NewTicker(700 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				col.Tick(ctx)
				prog.Send(snapshot())
			}
		}
	}()

	_, err = prog.Run()
	cancel()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func resolveCmux(flagVal string) (string, error) {
	for _, c := range []string{flagVal, os.Getenv("CMUX_BUNDLED_CLI_PATH")} {
		if c != "" {
			if p, err := exec.LookPath(c); err == nil {
				return p, nil
			}
		}
	}
	p, err := exec.LookPath("cmux")
	if err != nil {
		return "", errors.New("cmux CLI not found; pass --cmux-bin")
	}
	return p, nil
}

// resolveClaude skips cmux's wrapper shims so summary calls are not
// registered as cmux agent sessions.
func resolveClaude(flagVal string) (string, error) {
	if flagVal != "" {
		return exec.LookPath(flagVal)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.Contains(dir, "cmux-cli-shims") || dir == "" {
			continue
		}
		p := filepath.Join(dir, "claude")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			if real, err := filepath.EvalSymlinks(p); err == nil && strings.Contains(real, "cmux-cli-shims") {
				continue
			}
			return p, nil
		}
	}
	return "", errors.New("claude binary not found outside cmux shims; pass --claude-bin or --no-llm")
}
