package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"runtime/trace"
	"time"
)

// Trace captures a Go execution trace via runtime/trace.FlightRecorder.
//
// Unlike a one-shot profile, the flight recorder keeps a continuously
// overwritten window of the most recent trace data in memory — a "dashcam loop".
// Snapshot persists that window (the seconds leading up to a stall) to
// <dir>/trace.bin, a standard Go execution trace openable with `go tool trace`.
//
// The recorder must be armed before a stall fires for the window to contain the
// lead-up, so NewTrace starts it immediately: construct the collector at startup
// and Close it at shutdown. At most one flight recorder may be active per process.
type Trace struct {
	fr  *trace.FlightRecorder
	cpu bool // a CPU profile was started (for stack samples); stop it on Close
}

type traceOpts struct {
	fr  trace.FlightRecorderConfig
	cpu bool
}

// TraceOption configures the trace collector.
type TraceOption func(*traceOpts)

// WithMinAge sets a lower bound on how much history the window retains. 0 keeps
// the runtime default (on the order of seconds).
func WithMinAge(d time.Duration) TraceOption {
	return func(o *traceOpts) { o.fr.MinAge = d }
}

// WithMaxBytes caps the window size in bytes (a hint that takes precedence over
// MinAge). 0 keeps the runtime default.
func WithMaxBytes(n uint64) TraceOption {
	return func(o *traceOpts) { o.fr.MaxBytes = n }
}

// WithCPUSamples runs a concurrent CPU profile so the trace window also carries
// periodic on-CPU stack samples (the emitter renders them as per-goroutine
// sample markers). It claims the process-wide CPU-profile slot and adds the
// profiler's overhead on top of tracing, so it is opt-in.
func WithCPUSamples() TraceOption {
	return func(o *traceOpts) { o.cpu = true }
}

// NewTrace creates a Trace collector and arms the flight recorder so its window
// is already filling before any stall. The caller must Close it to stop tracing
// and release the single active-recorder slot.
func NewTrace(opts ...TraceOption) (*Trace, error) {
	var o traceOpts
	for _, opt := range opts {
		opt(&o)
	}
	fr := trace.NewFlightRecorder(o.fr)
	if err := fr.Start(); err != nil {
		return nil, fmt.Errorf("arm flight recorder: %w", err)
	}
	t := &Trace{fr: fr}
	if o.cpu {
		// Best effort: if another CPU profile is already running, proceed
		// without samples rather than failing the whole trace.
		if err := pprof.StartCPUProfile(io.Discard); err == nil {
			t.cpu = true
		}
	}
	return t, nil
}

// Name implements capture.Collector.
func (*Trace) Name() string { return "trace" }

// Snapshot writes the current flight-recorder window to <dir>/trace.bin.
func (t *Trace) Snapshot(_ context.Context, dir string) error {
	if t.fr == nil {
		return errors.New("trace collector is closed")
	}
	f, err := os.Create(filepath.Join(dir, "trace.bin"))
	if err != nil {
		return err
	}
	_, werr := t.fr.WriteTo(f)
	cerr := f.Close() // surfaces flush errors WriteTo alone can miss
	if werr != nil {
		return werr
	}
	return cerr
}

// Close stops the flight recorder, releasing the single active-recorder slot.
// After Close, Snapshot returns an error. Close is safe to call more than once.
func (t *Trace) Close() {
	if t.cpu {
		pprof.StopCPUProfile()
		t.cpu = false
	}
	if t.fr != nil {
		t.fr.Stop()
		t.fr = nil
	}
}
