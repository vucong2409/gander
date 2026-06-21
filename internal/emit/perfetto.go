// Package emit turns a synthesized event stream into a viewer-loadable trace.
//
// The minimal first cut emits the Chrome/Catapult JSON trace format, which the
// Perfetto UI (https://ui.perfetto.org) loads directly — no protobuf needed.
// Goroutine state transitions become filled per-goroutine state lanes, GC/STW
// ranges land on a runtime lane, and user regions get their own lane.
//
// The richer single view (the native Perfetto protobuf format + Go-semantic
// tracks + flow events + a PerfettoSQL module) is the v1 upgrade; this is the
// "see the fused view" milestone.
package emit

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"

	"github.com/vucong2409/gander/internal/synth"
)

// Process lanes in the emitted trace. Keeping goroutine states, runtime ranges,
// and regions in separate process groups avoids improper slice overlap on a
// single track (Chrome "X" slices on one track must nest cleanly).
const (
	pidGoroutines = 1
	pidRuntime    = 2
	pidRegions    = 3
	tidRuntime    = 0
)

type chromeTrace struct {
	TraceEvents     []chromeEvent `json:"traceEvents"`
	DisplayTimeUnit string        `json:"displayTimeUnit"`
}

type chromeEvent struct {
	Name string         `json:"name"`
	Cat  string         `json:"cat,omitempty"`
	Ph   string         `json:"ph"`
	TS   float64        `json:"ts"`            // microseconds
	Dur  float64        `json:"dur,omitempty"` // microseconds (ph:"X")
	PID  int            `json:"pid"`
	TID  int64          `json:"tid"`
	Args map[string]any `json:"args,omitempty"`
}

// WriteChromeTrace emits pt as a Chrome JSON trace to w. Timestamps are
// normalized so the timeline starts at zero.
func WriteChromeTrace(w io.Writer, pt *synth.ParsedTrace) error {
	ct := chromeTrace{DisplayTimeUnit: "ns"}
	if len(pt.Events) == 0 {
		return encode(w, ct)
	}

	base := pt.Events[0].TS
	var maxEnd int64
	for i := range pt.Events {
		e := &pt.Events[i]
		if e.TS < base {
			base = e.TS
		}
		if end := e.TS + e.Dur; end > maxEnd {
			maxEnd = end
		}
	}
	at := func(ns int64) float64 { return float64(ns-base) / 1000.0 }
	durUS := func(ns int64) float64 { return float64(ns) / 1000.0 }
	lanes := newLaneSet()

	// 1) Goroutine state transitions -> filled per-goroutine state lanes. The
	// state a goroutine entered at transition i fills the gap until transition
	// i+1 (and the final one extends to the end of the window).
	byG := map[int64][]int{}
	for i := range pt.Events {
		if pt.Events[i].Kind == synth.KindGoState {
			g := pt.Events[i].Goroutine
			byG[g] = append(byG[g], i)
		}
	}
	gids := make([]int64, 0, len(byG))
	for g := range byG {
		gids = append(gids, g)
	}
	sort.Slice(gids, func(i, j int) bool { return gids[i] < gids[j] })
	for _, g := range gids {
		idxs := byG[g]
		sort.Slice(idxs, func(a, b int) bool { return pt.Events[idxs[a]].TS < pt.Events[idxs[b]].TS })
		lanes.mark(pidGoroutines, g, goName(g))
		for j, idx := range idxs {
			e := &pt.Events[idx]
			end := maxEnd
			if j+1 < len(idxs) {
				end = pt.Events[idxs[j+1]].TS
			}
			ev := chromeEvent{
				Name: e.Name, Cat: "sched", Ph: "X",
				TS: at(e.TS), Dur: durUS(end - e.TS),
				PID: pidGoroutines, TID: g,
			}
			if e.Detail != "" {
				ev.Args = map[string]any{"from": e.Detail}
			}
			ct.TraceEvents = append(ct.TraceEvents, ev)
		}
	}

	// 2) Runtime ranges (GC/STW) and 3) user regions, each in their own group.
	for i := range pt.Events {
		e := &pt.Events[i]
		switch e.Kind {
		case synth.KindRange:
			tid := int64(tidRuntime)
			name := "GC / runtime"
			if e.Goroutine != 0 {
				tid, name = e.Goroutine, goName(e.Goroutine)
			}
			lanes.mark(pidRuntime, tid, name)
			ct.TraceEvents = append(ct.TraceEvents, chromeEvent{
				Name: e.Name, Cat: "gc", Ph: "X",
				TS: at(e.TS), Dur: durUS(e.Dur), PID: pidRuntime, TID: tid,
			})
		case synth.KindRegion:
			lanes.mark(pidRegions, e.Goroutine, goName(e.Goroutine))
			ct.TraceEvents = append(ct.TraceEvents, chromeEvent{
				Name: e.Name, Cat: "region", Ph: "X",
				TS: at(e.TS), Dur: durUS(e.Dur), PID: pidRegions, TID: e.Goroutine,
			})
		}
	}

	ct.TraceEvents = append(ct.TraceEvents, lanes.metadata()...)
	return encode(w, ct)
}

func goName(g int64) string { return "G" + strconv.FormatInt(g, 10) }

func encode(w io.Writer, ct chromeTrace) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(ct)
}

// laneSet records which (pid,tid) lanes are used so it can emit the metadata
// events that give processes and threads human-readable names in the UI.
type laneSet struct {
	threads map[[2]int64]string // {pid, tid} -> thread name
}

func newLaneSet() *laneSet { return &laneSet{threads: map[[2]int64]string{}} }

func (l *laneSet) mark(pid int, tid int64, name string) {
	l.threads[[2]int64{int64(pid), tid}] = name
}

func (l *laneSet) metadata() []chromeEvent {
	keys := make([][2]int64, 0, len(l.threads))
	for k := range l.threads {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	var evs []chromeEvent
	seenPID := map[int]bool{}
	for _, k := range keys {
		pid := int(k[0])
		if !seenPID[pid] {
			seenPID[pid] = true
			evs = append(evs, chromeEvent{Name: "process_name", Ph: "M", PID: pid, Args: map[string]any{"name": procName(pid)}})
		}
		evs = append(evs, chromeEvent{Name: "thread_name", Ph: "M", PID: pid, TID: k[1], Args: map[string]any{"name": l.threads[k]}})
	}
	return evs
}

func procName(pid int) string {
	switch pid {
	case pidGoroutines:
		return "goroutines"
	case pidRuntime:
		return "runtime"
	case pidRegions:
		return "user regions"
	default:
		return "pid " + strconv.Itoa(pid)
	}
}
