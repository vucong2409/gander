package synth_test

import (
	"testing"

	"github.com/vucong2409/gander/internal/synth"
)

func TestAddAlignedCounter(t *testing.T) {
	// Offset between wall and trace clocks is TraceNano - WallUnixNano.
	pt := &synth.ParsedTrace{Clock: &synth.ClockRef{TraceNano: 1_000_000, WallUnixNano: 5_000_000}}
	pt.AddAlignedCounter("cgroup.cpu.throttled_usec", 5_000_500, 42)

	if len(pt.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(pt.Events))
	}
	e := pt.Events[0]
	if e.Kind != synth.KindMetric || e.Source != synth.SourceProc {
		t.Errorf("kind/source = %v/%v, want metric/proc", e.Kind, e.Source)
	}
	if want := int64(1_000_500); e.TS != want { // TraceNano + (wall - WallUnixNano)
		t.Errorf("aligned TS = %d, want %d", e.TS, want)
	}
	if e.Value != 42 || e.Name != "cgroup.cpu.throttled_usec" {
		t.Errorf("value/name = %v/%q, want 42/throttled", e.Value, e.Name)
	}
}

func TestAddAlignedCounterNoClock(t *testing.T) {
	pt := &synth.ParsedTrace{} // pre-go1.25 trace: no ClockRef
	pt.AddAlignedCounter("x", 7_777, 1)
	if pt.Events[0].TS != 7_777 {
		t.Errorf("without clock, TS = %d, want 7777 (wall as-is)", pt.Events[0].TS)
	}
}
