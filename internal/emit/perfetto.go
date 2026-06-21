// Package emit turns a synthesized event stream into a viewer-loadable trace.
//
// The minimal first cut emits the Chrome/Catapult JSON trace format, which the
// Perfetto UI (https://ui.perfetto.dev) loads directly — no protobuf needed.
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
	"strings"

	"github.com/vucong2409/gander/internal/synth"
)

// Process lanes in the emitted trace. Keeping goroutine states, runtime ranges,
// and regions in separate process groups avoids improper slice overlap on a
// single track (Chrome "X" slices on one track must nest cleanly).
const (
	pidGoroutines = 1
	pidRuntime    = 2
	pidRegions    = 3
	pidMetrics    = 4
	tidRuntime    = 0
)

type chromeEvent struct {
	Name string         `json:"name"`
	Cat  string         `json:"cat,omitempty"`
	Ph   string         `json:"ph"`
	TS   float64        `json:"ts"`            // microseconds
	Dur  float64        `json:"dur,omitempty"` // microseconds (ph:"X")
	PID  int            `json:"pid"`
	TID  int64          `json:"tid"`
	ID   int            `json:"id,omitempty"` // flow id (ph:"s"/"f")
	BP   string         `json:"bp,omitempty"` // flow binding point ("e" = enclosing slice)
	Args map[string]any `json:"args,omitempty"`
}

// WriteChromeTrace emits pt as a Chrome/Catapult JSON trace to w. Timestamps are
// normalized so the timeline starts at zero.
//
// It uses the JSON *Array* Format (a bare [ ... ] of events). Perfetto's JSON
// tokenizer imports this most reliably; the object form
// ({"traceEvents": [...]}) can trip it (json_parser_failure). The slice is
// non-nil so an empty stream still encodes as [], never null.
func WriteChromeTrace(w io.Writer, pt *synth.ParsedTrace) error {
	events := make([]chromeEvent, 0, len(pt.Events))
	if len(pt.Events) == 0 {
		return encode(w, events)
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
		lanes.mark(pidGoroutines, g, goName(g, pt.GoNames))
		for j, idx := range idxs {
			e := &pt.Events[idx]
			end := maxEnd
			if j+1 < len(idxs) {
				end = pt.Events[idxs[j+1]].TS
			}
			label := e.Name
			if e.Detail != "" { // the block reason, e.g. "chan receive"
				label = e.Name + ": " + e.Detail
			}
			ev := chromeEvent{
				Name: label, Cat: "sched", Ph: "X",
				TS: at(e.TS), Dur: durUS(end - e.TS),
				PID: pidGoroutines, TID: g,
			}
			args := map[string]any{}
			if e.Detail != "" {
				args["reason"] = e.Detail
			}
			if s := stackString(e.Stack); s != "" {
				args["stack"] = s
			}
			if len(args) > 0 {
				ev.Args = args
			}
			events = append(events, ev)
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
				tid, name = e.Goroutine, goName(e.Goroutine, pt.GoNames)
			}
			lanes.mark(pidRuntime, tid, name)
			events = append(events, chromeEvent{
				Name: e.Name, Cat: "gc", Ph: "X",
				TS: at(e.TS), Dur: durUS(e.Dur), PID: pidRuntime, TID: tid,
			})
		case synth.KindRegion:
			lanes.mark(pidRegions, e.Goroutine, goName(e.Goroutine, pt.GoNames))
			events = append(events, chromeEvent{
				Name: e.Name, Cat: "region", Ph: "X",
				TS: at(e.TS), Dur: durUS(e.Dur), PID: pidRegions, TID: e.Goroutine,
			})
		case synth.KindMetric:
			// Counter track: one series per metric name under the metrics group.
			// Heap/GC come from the trace; cgroup/PSI from the proc sampler.
			lanes.mark(pidMetrics, tidRuntime, "runtime / kernel metrics")
			events = append(events, chromeEvent{
				Name: e.Name, Cat: "metric", Ph: "C",
				TS: at(e.TS), PID: pidMetrics, TID: tidRuntime,
				Args: map[string]any{"value": e.Value},
			})
		}
	}

	// 4) Wake-up arrows: a flow edge from the waker goroutine to the woken one,
	// bound to whatever slice encloses each endpoint.
	for i := range pt.Unblocks {
		u := &pt.Unblocks[i]
		ts := at(u.TS)
		events = append(events,
			chromeEvent{Name: "wakeup", Cat: "wakeup", Ph: "s", ID: i + 1, TS: ts, PID: pidGoroutines, TID: u.Waker},
			chromeEvent{Name: "wakeup", Cat: "wakeup", Ph: "f", BP: "e", ID: i + 1, TS: ts, PID: pidGoroutines, TID: u.Woken},
		)
	}

	events = append(events, lanes.metadata()...)
	return encode(w, events)
}

func goName(g int64, names map[int64]string) string {
	label := "G" + strconv.FormatInt(g, 10)
	if fn := names[g]; fn != "" {
		label += " " + shortFunc(fn)
	}
	return label
}

// shortFunc trims the package path from a fully-qualified function name:
// "github.com/x/y/pkg.Func" -> "pkg.Func".
func shortFunc(fn string) string {
	if i := strings.LastIndexByte(fn, '/'); i >= 0 {
		return fn[i+1:]
	}
	return fn
}

// stackString renders up to 12 frames of a stack (leaf first) for a slice's
// details panel — enough to see where a goroutine blocked.
func stackString(frames []synth.Frame) string {
	const maxFrames = 12
	var b strings.Builder
	for i, f := range frames {
		if i >= maxFrames {
			b.WriteString("\n…")
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(f.Func)
		if f.File != "" {
			b.WriteString(" (")
			b.WriteString(f.File)
			b.WriteByte(':')
			b.WriteString(strconv.FormatUint(f.Line, 10))
			b.WriteByte(')')
		}
	}
	return b.String()
}

func encode(w io.Writer, events []chromeEvent) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(events)
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
