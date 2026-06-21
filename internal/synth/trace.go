package synth

import (
	"errors"
	"fmt"
	"io"

	exptrace "golang.org/x/exp/trace"
)

// ClockRef is a near-simultaneous reading of the trace clock, the wall clock,
// and the system monotonic clock, taken from the trace's Sync event. It lets a
// later stage (PR8) map trace-clock timestamps onto wall time and align them
// with other sources. Nil for traces older than go1.25.
type ClockRef struct {
	TraceNano    int64
	WallUnixNano int64
	MonoNano     uint64
}

// Unblock records that one goroutine made another runnable — the wake-up edge
// used to draw causality arrows.
type Unblock struct {
	TS    int64 // trace-clock ns of the wake-up
	Waker int64 // GoID that did the unblocking
	Woken int64 // GoID that became runnable
}

// ParsedTrace is the result of reading one execution trace.
type ParsedTrace struct {
	Events   []Event
	Unblocks []Unblock
	GoNames  map[int64]string // GoID -> entry function, for legible lane labels
	Clock    *ClockRef        // from the first Sync event; nil pre-go1.25
	StartTS  int64            // trace-clock ns of the first non-sync event
}

// CountByKind tallies events by Kind — handy for diagnosis and tests.
func (pt *ParsedTrace) CountByKind() map[Kind]int {
	m := make(map[Kind]int, len(pt.Events))
	for i := range pt.Events {
		m[pt.Events[i].Kind]++
	}
	return m
}

// ParseTrace reads a Go execution trace (as produced by collect.Trace or
// runtime/trace) and reduces it to the unified event model: goroutine state
// transitions become point events; GC/STW ranges and user regions become
// intervals; logs, metric samples, and CPU samples become point events. Ranges
// active when the trace window opened are dated from their first observed event.
func ParseTrace(r io.Reader) (*ParsedTrace, error) {
	rd, err := exptrace.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("new trace reader: %w", err)
	}

	pt := &ParsedTrace{GoNames: map[int64]string{}}
	open := make(map[string]int64) // open range/region begins -> start ns
	gotStart := false

	for {
		ev, err := rd.ReadEvent()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read trace event: %w", err)
		}
		ts := int64(ev.Time())

		switch ev.Kind() {
		case exptrace.EventSync:
			if pt.Clock == nil {
				if cs := ev.Sync().ClockSnapshot; cs != nil {
					pt.Clock = &ClockRef{
						TraceNano:    int64(cs.Trace),
						WallUnixNano: cs.Wall.UnixNano(),
						MonoNano:     cs.Mono,
					}
				}
			}
			continue // Sync events are not part of the timeline.

		case exptrace.EventStateTransition:
			st := ev.StateTransition()
			if st.Resource.Kind != exptrace.ResourceGoroutine {
				break // proc/thread transitions are not surfaced yet
			}
			from, to := st.Goroutine()
			woken := int64(st.Resource.Goroutine())
			// A Waiting->Runnable transition is a wake-up; the event's actor
			// goroutine is whoever did the unblocking.
			if from == exptrace.GoWaiting && to == exptrace.GoRunnable {
				if waker := int64(ev.Goroutine()); waker > 0 && waker != woken {
					pt.Unblocks = append(pt.Unblocks, Unblock{TS: ts, Waker: waker, Woken: woken})
				}
			}
			stk := framesOf(st.Stack)
			// The root (last) frame is the goroutine's entry function — capture
			// it once for a legible lane label.
			if len(stk) > 0 {
				if _, ok := pt.GoNames[woken]; !ok {
					if fn := stk[len(stk)-1].Func; fn != "" {
						pt.GoNames[woken] = fn
					}
				}
			}
			pt.Events = append(pt.Events, Event{
				TS:        ts,
				Kind:      KindGoState,
				Source:    SourceTrace,
				Goroutine: woken,
				Proc:      int64(ev.Proc()),
				Thread:    int64(ev.Thread()),
				Name:      to.String(),
				Detail:    from.String(),
				Stack:     stk,
			})

		case exptrace.EventRangeBegin, exptrace.EventRangeActive:
			rng := ev.Range()
			open["r\x00"+rng.Scope.String()+"\x00"+rng.Name] = ts

		case exptrace.EventRangeEnd:
			rng := ev.Range()
			begin, dur := pairBegin(open, "r\x00"+rng.Scope.String()+"\x00"+rng.Name, ts)
			var goid int64
			if rng.Scope.Kind == exptrace.ResourceGoroutine {
				goid = int64(rng.Scope.Goroutine())
			}
			pt.Events = append(pt.Events, Event{
				TS: begin, Dur: dur, Kind: KindRange, Source: SourceTrace,
				Goroutine: goid, Name: rng.Name,
			})

		case exptrace.EventRegionBegin:
			rg := ev.Region()
			open[fmt.Sprintf("g\x00%d\x00%s", ev.Goroutine(), rg.Type)] = ts

		case exptrace.EventRegionEnd:
			rg := ev.Region()
			begin, dur := pairBegin(open, fmt.Sprintf("g\x00%d\x00%s", ev.Goroutine(), rg.Type), ts)
			pt.Events = append(pt.Events, Event{
				TS: begin, Dur: dur, Kind: KindRegion, Source: SourceTrace,
				Goroutine: int64(ev.Goroutine()), Name: rg.Type,
			})

		case exptrace.EventLog:
			l := ev.Log()
			pt.Events = append(pt.Events, Event{
				TS: ts, Kind: KindLog, Source: SourceTrace,
				Goroutine: int64(ev.Goroutine()), Name: l.Category, Detail: l.Message,
			})

		case exptrace.EventMetric:
			m := ev.Metric()
			e := Event{TS: ts, Kind: KindMetric, Source: SourceTrace, Name: m.Name, Detail: m.Value.String()}
			if m.Value.Kind() == exptrace.ValueUint64 {
				e.Value = float64(m.Value.Uint64())
			}
			pt.Events = append(pt.Events, e)

		case exptrace.EventStackSample:
			pt.Events = append(pt.Events, Event{
				TS: ts, Kind: KindSample, Source: SourceTrace,
				Goroutine: int64(ev.Goroutine()),
				Proc:      int64(ev.Proc()),
				Thread:    int64(ev.Thread()),
				Stack:     framesOf(ev.Stack()),
			})
		}

		if !gotStart {
			pt.StartTS = ts
			gotStart = true
		}
	}
	return pt, nil
}

// pairBegin closes an interval: it returns the recorded begin and the duration
// to end, removing the open entry. If no begin was seen (the trace window opened
// mid-range and we missed it), it returns (end, 0) — a zero-width marker.
func pairBegin(open map[string]int64, key string, end int64) (begin, dur int64) {
	if b, ok := open[key]; ok {
		delete(open, key)
		return b, end - b
	}
	return end, 0
}

func framesOf(s exptrace.Stack) []Frame {
	var fs []Frame
	for f := range s.Frames() {
		fs = append(fs, Frame{Func: f.Func, File: f.File, Line: f.Line})
	}
	return fs
}
