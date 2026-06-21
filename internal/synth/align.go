package synth

// AddAlignedCounter appends a counter sample from an independent source (e.g. a
// cgroup or /proc sampler) to the event stream, converting its wall-clock
// timestamp onto the trace clock via the trace's ClockRef so it lines up with
// the goroutine and GC lanes.
//
// Alignment uses the wall clock captured in the trace's Sync event: the offset
// between wall and trace time is fixed within a capture window, so
// trace_ns ≈ TraceNano + (wall - WallUnixNano). Good to sub-millisecond, which
// is the resolution the fused view needs. If no ClockRef was captured (a
// pre-go1.25 trace), the wall timestamp is used as-is (best effort, unaligned).
func (pt *ParsedTrace) AddAlignedCounter(name string, wallUnixNano int64, value float64) {
	ts := wallUnixNano
	if pt.Clock != nil {
		ts = pt.Clock.TraceNano + (wallUnixNano - pt.Clock.WallUnixNano)
	}
	pt.Events = append(pt.Events, Event{
		TS:     ts,
		Kind:   KindMetric,
		Source: SourceProc,
		Name:   name,
		Value:  value,
	})
}
