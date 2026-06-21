package record

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/vucong2409/gander/hb"
)

// BenchmarkBeginTracingOff measures the per-work-unit floor: Begin/end with no
// execution trace active — just the heartbeat tick plus a (near-noop)
// trace.StartRegion. This is the cost an embedder always pays.
func BenchmarkBeginTracingOff(b *testing.B) {
	r := &Recorder{mon: hb.New(hb.WithBudget(time.Hour))}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Begin(ctx)() // open and immediately close one work-unit
	}
}

// BenchmarkBeginArmed measures the realistic embedded cost: the flight recorder
// is armed, so each work-unit region is actually recorded into the in-memory
// ring buffer. The delta from BenchmarkBeginTracingOff is the price of having
// the recorder live.
func BenchmarkBeginArmed(b *testing.B) {
	r, err := Start(Options{
		Budget:    time.Hour, // never trip the watchdog during the benchmark
		BundleDir: b.TempDir(),
		Logger:    log.New(io.Discard, "", 0),
	})
	if err != nil {
		b.Fatal(err)
	}
	defer r.Stop()
	if r.tracer == nil {
		b.Skip("flight recorder unavailable in this environment")
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Begin(ctx)()
	}
}
