package usage

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/fennih/codeguide/internal/sanitize"
)

// Limit is one plan limit, such as the 5-hour session or the weekly cap.
type Limit struct {
	Label    string    `json:"label"`
	Percent  int       `json:"percent"`
	ResetsAt time.Time `json:"resetsAt"`
	Severe   bool      `json:"severe,omitempty"` // Claude Code flags it as close to the cap
}

// Plan is the account's usage as Claude Code last fetched it.
type Plan struct {
	Limits    []Limit   `json:"limits"`
	FetchedAt time.Time `json:"fetchedAt"`
}

const (
	planEvery   = 10 * time.Second
	planMaxSize = 16 << 20
)

// PlanReader rereads the Claude Code config at most every planEvery and only
// when it changed. Only the cached usage is decoded; the rest of the file
// (account details, project history) is skipped.
type PlanReader struct {
	Path    string
	checked time.Time
	mtime   time.Time
	size    int64
	plan    Plan
}

func (p *PlanReader) Get(now time.Time) Plan {
	if p.Path == "" || now.Sub(p.checked) < planEvery {
		return p.plan
	}
	p.checked = now
	st, err := os.Stat(p.Path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > planMaxSize {
		p.plan = Plan{}
		return p.plan
	}
	if st.ModTime().Equal(p.mtime) && st.Size() == p.size {
		return p.plan
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return p.plan
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, planMaxSize))
	if err != nil {
		return p.plan
	}
	if plan, ok := parsePlan(data); ok {
		p.plan = plan
		p.mtime, p.size = st.ModTime(), st.Size()
	}
	return p.plan
}

type window5 struct {
	Utilization *float64  `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

type config struct {
	Cached *struct {
		FetchedAtMs int64 `json:"fetchedAtMs"`
		Utilization struct {
			FiveHour *window5 `json:"five_hour"`
			SevenDay *window5 `json:"seven_day"`
			Limits   []struct {
				Kind     string    `json:"kind"`
				Percent  float64   `json:"percent"`
				Severity string    `json:"severity"`
				ResetsAt time.Time `json:"resets_at"`
				Scope    *struct {
					Model *struct {
						DisplayName string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			} `json:"limits"`
		} `json:"utilization"`
	} `json:"cachedUsageUtilization"`
}

func parsePlan(data []byte) (Plan, bool) {
	var c config
	if json.Unmarshal(data, &c) != nil {
		return Plan{}, false
	}
	if c.Cached == nil {
		return Plan{}, true
	}
	u := c.Cached.Utilization
	plan := Plan{FetchedAt: time.UnixMilli(c.Cached.FetchedAtMs)}
	for _, l := range u.Limits {
		label := ""
		switch l.Kind {
		case "session":
			label = "5h"
		case "weekly_all":
			label = "week"
		default:
			if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
				label = sanitize.Line(l.Scope.Model.DisplayName, 16)
			}
		}
		if label == "" {
			continue
		}
		plan.Limits = append(plan.Limits, Limit{
			Label: label, Percent: int(l.Percent + 0.5), ResetsAt: l.ResetsAt,
			Severe: l.Severity != "" && l.Severity != "normal",
		})
	}
	if len(plan.Limits) == 0 {
		for _, w := range []struct {
			label string
			w     *window5
		}{{"5h", u.FiveHour}, {"week", u.SevenDay}} {
			if w.w != nil && w.w.Utilization != nil {
				plan.Limits = append(plan.Limits, Limit{Label: w.label, Percent: int(*w.w.Utilization + 0.5), ResetsAt: w.w.ResetsAt})
			}
		}
	}
	return plan, true
}
