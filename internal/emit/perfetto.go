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
	"fmt"
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
	S    string         `json:"s,omitempty"`  // instant scope: "g" = global vertical line (ph:"i")
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
	activity := map[int64]int64{} // per-goroutine on-CPU ns, for lane ordering
	var slowestWU *synth.Event    // the longest "work-unit" region — the stall
	var throttle []throttleSample // cgroup throttled_usec samples, for the freeze overlay

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
		if _, ok := activity[g]; !ok {
			activity[g] = 0 // rank parked goroutines too (they sink to the bottom)
		}
		for j, idx := range idxs {
			e := &pt.Events[idx]
			end := maxEnd
			if j+1 < len(idxs) {
				end = pt.Events[idxs[j+1]].TS
			}
			if strings.HasPrefix(e.Name, "Running") || strings.HasPrefix(e.Name, "Syscall") {
				activity[g] += end - e.TS // on-CPU time, for lane ordering
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
			// Overlay real GC stop-the-world pauses as a global vertical line
			// across every lane (skip tracing-induced STWs like "start trace").
			if isGCStopTheWorld(e.Name) {
				events = append(events, chromeEvent{
					Name: "STW: " + stwShort(e.Name), Cat: "stw", Ph: "i", S: "g",
					TS: at(e.TS), PID: pidRuntime, TID: tid,
				})
			}
		case synth.KindRegion:
			lanes.mark(pidRegions, e.Goroutine, goName(e.Goroutine, pt.GoNames))
			events = append(events, chromeEvent{
				Name: e.Name, Cat: "region", Ph: "X",
				TS: at(e.TS), Dur: durUS(e.Dur), PID: pidRegions, TID: e.Goroutine,
			})
			if e.Name == "work-unit" && (slowestWU == nil || e.Dur > slowestWU.Dur) {
				slowestWU = e
			}
		case synth.KindMetric:
			// Counter track: one series per metric name under the metrics group.
			// Heap/GC come from the trace; cgroup/PSI from the proc sampler.
			lanes.mark(pidMetrics, tidRuntime, "runtime / kernel metrics")
			events = append(events, chromeEvent{
				Name: e.Name, Cat: "metric", Ph: "C",
				TS: at(e.TS), PID: pidMetrics, TID: tidRuntime,
				Args: map[string]any{"value": e.Value},
			})
			if e.Name == "cgroup.cpu.throttled_usec" {
				throttle = append(throttle, throttleSample{ts: at(e.TS), usec: e.Value})
			}
		case synth.KindSample:
			// CPU stack sample -> an instant marker on the sampled goroutine's
			// lane (leaf function as the label, full stack in args), so you can
			// see what was on-CPU right next to its state.
			if e.Goroutine == 0 {
				continue
			}
			lanes.mark(pidGoroutines, e.Goroutine, goName(e.Goroutine, pt.GoNames))
			ev := chromeEvent{
				Name: sampleName(e.Stack), Cat: "sample", Ph: "i", S: "t",
				TS: at(e.TS), PID: pidGoroutines, TID: e.Goroutine,
			}
			if s := stackString(e.Stack); s != "" {
				ev.Args = map[string]any{"stack": s}
			}
			events = append(events, ev)
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

	// Stall marker: a global vertical line at the slowest work-unit so you know
	// where to zoom.
	if slowestWU != nil {
		events = append(events, chromeEvent{
			Name: fmt.Sprintf("slowest work-unit (%.1fms)", float64(slowestWU.Dur)/1e6),
			Cat:  "stall", Ph: "i", S: "g",
			TS: at(slowestWU.TS), PID: pidGoroutines, TID: slowestWU.Goroutine,
		})
	}

	// CFS-throttle overlay: a global vertical line at the start of each throttled
	// run (the kernel froze the whole process here), labeled with the duration.
	events = append(events, throttleOverlay(throttle)...)

	events = append(events, lanes.metadata()...)
	events = append(events, sortIndexEvents(activity)...) // busiest goroutines on top
	return encode(w, events)
}

// sampleName returns the leaf function (top frame) of a CPU sample's stack,
// trimmed of its package path, for the marker label.
func sampleName(frames []synth.Frame) string {
	if len(frames) == 0 {
		return "sample"
	}
	return shortFunc(frames[0].Func)
}

type throttleSample struct {
	ts   float64 // microseconds on the normalized timeline
	usec float64 // cumulative cgroup throttled_usec
}

// throttleOverlay coalesces throttled intervals (where cumulative throttled_usec
// rose) into runs and emits one global vertical line at each run's start,
// labeled with the throttled duration — so a kernel freeze shows across every
// lane, like the STW overlay.
func throttleOverlay(s []throttleSample) []chromeEvent {
	if len(s) < 2 {
		return nil
	}
	sort.Slice(s, func(i, j int) bool { return s[i].ts < s[j].ts })
	var out []chromeEvent
	for i := 1; i < len(s); {
		if s[i].usec <= s[i-1].usec {
			i++
			continue
		}
		start := i - 1
		for i < len(s) && s[i].usec > s[i-1].usec {
			i++
		}
		deltaMs := (s[i-1].usec - s[start].usec) / 1000.0
		out = append(out, chromeEvent{
			Name: fmt.Sprintf("CFS throttled (%.0fms)", deltaMs),
			Cat:  "throttle", Ph: "i", S: "g",
			TS: s[start].ts, PID: pidMetrics, TID: tidRuntime,
		})
	}
	return out
}

// isGCStopTheWorld reports whether a runtime range is a real GC stop-the-world
// pause (worth overlaying), as opposed to a tracing-induced one such as
// "stop-the-world (start trace)" / "(all goroutines stack trace)".
func isGCStopTheWorld(name string) bool {
	return strings.Contains(name, "stop-the-world") && strings.Contains(name, "GC")
}

// stwShort extracts the parenthetical reason from a stop-the-world range name:
// "stop-the-world (GC sweep termination)" -> "GC sweep termination".
func stwShort(name string) string {
	if i := strings.IndexByte(name, '('); i >= 0 {
		return strings.TrimSuffix(name[i+1:], ")")
	}
	return name
}

// sortIndexEvents emits thread_sort_index metadata so goroutine lanes are
// ordered by on-CPU activity — the work goroutine and watchdog rise to the top
// and parked runtime goroutines sink to the bottom.
func sortIndexEvents(activity map[int64]int64) []chromeEvent {
	gids := make([]int64, 0, len(activity))
	for g := range activity {
		gids = append(gids, g)
	}
	sort.Slice(gids, func(i, j int) bool {
		if activity[gids[i]] != activity[gids[j]] {
			return activity[gids[i]] > activity[gids[j]]
		}
		return gids[i] < gids[j]
	})
	evs := make([]chromeEvent, 0, len(gids))
	for idx, g := range gids {
		evs = append(evs, chromeEvent{
			Name: "thread_sort_index", Ph: "M", PID: pidGoroutines, TID: g,
			Args: map[string]any{"sort_index": idx},
		})
	}
	return evs
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
//
// Output is kept pure ASCII on purpose: Perfetto's JSON trace tokenizer is
// byte-oriented and rejects multi-byte UTF-8 in string values (it surfaces as
// json_parser_failure), so the overflow marker is "..." rather than a "…".
func stackString(frames []synth.Frame) string {
	const maxFrames = 12
	var b strings.Builder
	for i, f := range frames {
		if i >= maxFrames {
			b.WriteString("\n... (")
			b.WriteString(strconv.Itoa(len(frames) - maxFrames))
			b.WriteString(" more)")
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
