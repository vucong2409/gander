// Package synth holds gander's unified event model and the per-source parsers
// that populate it. Every signal — execution-trace events, and (in later PRs)
// runtime metrics and /proc/cgroup samples — is reduced to a timestamped Event
// tagged by the entities it concerns, so heterogeneous sources can be merged
// onto one timeline and emitted together (the "synthesis" step).
//
// PR4 implements the first source: the Go execution trace (see ParseTrace).
package synth

// Kind classifies what an Event represents.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindGoState      // a goroutine state transition (point event)
	KindRange        // a runtime range, e.g. GC / stop-the-world (interval)
	KindRegion       // a user runtime/trace region (interval)
	KindLog          // a user runtime/trace.Log line (point event)
	KindMetric       // a runtime metric sample carried in the trace (point event)
	KindSample       // a CPU stack sample carried in the trace (point event)
)

// String returns a short, stable name for the kind.
func (k Kind) String() string {
	switch k {
	case KindGoState:
		return "gostate"
	case KindRange:
		return "range"
	case KindRegion:
		return "region"
	case KindLog:
		return "log"
	case KindMetric:
		return "metric"
	case KindSample:
		return "sample"
	default:
		return "unknown"
	}
}

// Source identifies which collector produced an Event. The trace is the only
// source in PR4; metrics and /proc/cgroup sources land in later PRs.
type Source uint8

const (
	SourceTrace Source = iota // the Go execution trace
	SourceProc                // /proc and cgroup samplers (counter values)
)

// Frame is one symbolized stack frame.
type Frame struct {
	Func string
	File string
	Line uint64
}

// Event is the unit of the unified model.
//
// TS/Dur are nanoseconds on the source clock; for the execution trace that is
// the trace's monotonic clock — see ParsedTrace.Clock for aligning it to wall
// time and to other sources (PR8). Entity tags are set to what the source knows;
// a goroutine of 0 means "not applicable", and Proc/Thread carry the trace's own
// values (negative means none).
type Event struct {
	TS     int64 // start, ns on the source clock
	Dur    int64 // duration in ns; 0 for point events, >0 for intervals
	Kind   Kind
	Source Source

	Goroutine int64 // GoID; 0 = not applicable
	Proc      int64 // ProcID; negative = none
	Thread    int64 // ThreadID; negative = none

	Name   string  // state / range / region / metric name, or log category
	Detail string  // from-state, log message, metric value, etc.
	Value  float64 // numeric value for KindMetric / counter events
	Stack  []Frame // optional
}
