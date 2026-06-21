// Package diag turns a captured bundle into scored findings — gander's "tell me
// what's wrong" layer (Layer 2).
//
// Every rule is deterministic: arithmetic over the parsed execution trace, the
// cgroup/PSI samples, and the capture trigger, against documented thresholds. No
// LLM, so results are reproducible and CI-able. An LLM may later phrase findings
// in prose, but never decide them.
package diag

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/collect"
	"github.com/vucong2409/gander/internal/synth"
)

// Severity values, ordered by sevRank for sorting.
const (
	sevInfo     = "info"
	sevWarn     = "warn"
	sevCritical = "critical"
)

func sevRank(s string) int {
	switch s {
	case sevCritical:
		return 2
	case sevWarn:
		return 1
	default:
		return 0
	}
}

// Finding is one diagnosed problem (or observation).
type Finding struct {
	Rule       string `json:"rule"`
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Diagnose runs every rule over a captured bundle and returns findings sorted
// most-severe first. proc may be empty (e.g. captured off Linux); rules that
// need it simply don't fire.
func Diagnose(pt *synth.ParsedTrace, proc []collect.ProcSample, meta bundle.Meta) []Finding {
	fs := []Finding{}
	fs = append(fs, missedBudget(meta)...)
	fs = append(fs, cfsThrottling(proc)...)
	fs = append(fs, cpuPressure(proc)...)
	fs = append(fs, gcPressure(pt)...)
	fs = append(fs, blockReasons(pt)...)
	sort.SliceStable(fs, func(i, j int) bool { return sevRank(fs[i].Severity) > sevRank(fs[j].Severity) })
	return fs
}

// missedBudget reports the work-unit that fired the capture (from the heartbeat).
func missedBudget(meta bundle.Meta) []Finding {
	if meta.Trigger.Source != "heartbeat" {
		return nil
	}
	elapsed := floatDetail(meta.Trigger.Detail, "elapsed_ms")
	budget := floatDetail(meta.Trigger.Detail, "budget_ms")
	if elapsed <= 0 {
		return nil
	}
	sev, over := sevWarn, ""
	if budget > 0 {
		if elapsed > 4*budget {
			sev = sevCritical
		}
		over = fmt.Sprintf(" — %.1f× over", elapsed/budget)
	}
	return []Finding{{
		Rule:       "missed-budget",
		Severity:   sev,
		Title:      "a work-unit exceeded its latency budget",
		Evidence:   fmt.Sprintf("work-unit ran %.0fms (budget %.0fms)%s", elapsed, budget, over),
		Suggestion: "the findings below point at the likely cause; open fused.json to see what the hot goroutine was doing in this window",
	}}
}

// cfsThrottling reports cgroup CPU throttling — the classic "slow despite
// headroom" cause that gotraceui can't see.
func cfsThrottling(proc []collect.ProcSample) []Finding {
	if len(proc) < 2 {
		return nil
	}
	first, last := proc[0], proc[len(proc)-1]
	throttledMs := float64(last.ThrottledUsec-first.ThrottledUsec) / 1000.0
	n := last.NrThrottled - first.NrThrottled
	if throttledMs <= 0 && n == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "cfs-throttling",
		Severity:   sevCritical,
		Title:      "cgroup CPU throttling during the window",
		Evidence:   fmt.Sprintf("CFS-throttled %d time(s) for %.1fms — the cgroup CPU quota capped the process", n, throttledMs),
		Suggestion: "raise the cgroup CPU limit, or lower GOMAXPROCS so the runtime doesn't oversubscribe the quota",
	}}
}

// cpuPressure reports time runnable work spent waiting for a CPU (PSI "some").
func cpuPressure(proc []collect.ProcSample) []Finding {
	if len(proc) < 2 {
		return nil
	}
	psiMs := float64(proc[len(proc)-1].CPUPressureSomeUsec-proc[0].CPUPressureSomeUsec) / 1000.0
	if psiMs < 1 { // sub-millisecond pressure is noise
		return nil
	}
	return []Finding{{
		Rule:       "cpu-pressure",
		Severity:   sevWarn,
		Title:      "runnable work stalled waiting for a CPU (PSI)",
		Evidence:   fmt.Sprintf("CPU pressure (some) accrued %.1fms over the window — runnable tasks waited for a core", psiMs),
		Suggestion: "CPU contention (GC workers, noisy neighbors, or too-low a quota); check GOMAXPROCS against available cores",
	}}
}

// gcPressure reports the share of the window spent in GC stop-the-world pauses.
func gcPressure(pt *synth.ParsedTrace) []Finding {
	base, maxEnd := traceSpan(pt)
	window := maxEnd - base
	if window <= 0 {
		return nil
	}
	var stwNs int64
	var count int
	for i := range pt.Events {
		if e := &pt.Events[i]; e.Kind == synth.KindRange && strings.Contains(e.Name, "stop-the-world") {
			stwNs += e.Dur
			count++
		}
	}
	if count == 0 {
		return nil
	}
	frac := float64(stwNs) / float64(window)
	if frac < 0.02 { // <2% of the window is unremarkable
		return nil
	}
	sev := sevWarn
	if frac > 0.15 {
		sev = sevCritical
	}
	return []Finding{{
		Rule:       "gc-pressure",
		Severity:   sev,
		Title:      "GC stop-the-world consumed a notable share of the window",
		Evidence:   fmt.Sprintf("%d stop-the-world pause(s) totaling %.1fms (%.0f%% of the %.0fms window)", count, ms(stwNs), frac*100, ms(window)),
		Suggestion: "raise GOMEMLIMIT/GOGC to GC less often, or cut allocations on the hot path",
	}}
}

// blockReasons reports where goroutines spent their wait time, by block reason,
// excluding idle runtime goroutines that would otherwise dominate the tally.
func blockReasons(pt *synth.ParsedTrace) []Finding {
	_, maxEnd := traceSpan(pt)
	byG := map[int64][]int{}
	for i := range pt.Events {
		if pt.Events[i].Kind == synth.KindGoState {
			byG[pt.Events[i].Goroutine] = append(byG[pt.Events[i].Goroutine], i)
		}
	}
	byReason := map[string]int64{}
	for _, idxs := range byG {
		sort.Slice(idxs, func(a, b int) bool { return pt.Events[idxs[a]].TS < pt.Events[idxs[b]].TS })
		for j, idx := range idxs {
			e := &pt.Events[idx]
			if e.Detail == "" || isIdleReason(e.Detail) {
				continue
			}
			end := maxEnd
			if j+1 < len(idxs) {
				end = pt.Events[idxs[j+1]].TS
			}
			if end > e.TS {
				byReason[e.Detail] += end - e.TS
			}
		}
	}
	if len(byReason) == 0 {
		return nil
	}
	type kv struct {
		reason string
		ns     int64
	}
	top := make([]kv, 0, len(byReason))
	for r, n := range byReason {
		top = append(top, kv{r, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].ns > top[j].ns })
	parts := make([]string, 0, 3)
	for i, t := range top {
		if i >= 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %.0fms", t.reason, ms(t.ns)))
	}
	return []Finding{{
		Rule:     "block-reasons",
		Severity: sevInfo,
		Title:    "wait time by block reason (summed across goroutines, excluding idle runtime)",
		Evidence: strings.Join(parts, "; "),
	}}
}

// isIdleReason filters runtime goroutines parked indefinitely; they're not the
// problem and would otherwise dominate the wait-time tally.
func isIdleReason(reason string) bool {
	return strings.Contains(reason, "system goroutine wait") || strings.HasPrefix(reason, "GC ")
}

func floatDetail(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}

func traceSpan(pt *synth.ParsedTrace) (base, maxEnd int64) {
	if len(pt.Events) == 0 {
		return 0, 0
	}
	base = pt.Events[0].TS
	for i := range pt.Events {
		e := &pt.Events[i]
		if e.TS < base {
			base = e.TS
		}
		if end := e.TS + e.Dur; end > maxEnd {
			maxEnd = end
		}
	}
	return base, maxEnd
}

func ms(ns int64) float64 { return float64(ns) / 1e6 }
