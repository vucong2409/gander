package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	fr *trace.FlightRecorder
}

// TraceOption configures the flight-recorder window.
type TraceOption func(*trace.FlightRecorderConfig)

// WithMinAge sets a lower bound on how much history the window retains. 0 keeps
// the runtime default (on the order of seconds).
func WithMinAge(d time.Duration) TraceOption {
	return func(c *trace.FlightRecorderConfig) { c.MinAge = d }
}

// WithMaxBytes caps the window size in bytes (a hint that takes precedence over
// MinAge). 0 keeps the runtime default.
func WithMaxBytes(n uint64) TraceOption {
	return func(c *trace.FlightRecorderConfig) { c.MaxBytes = n }
}

// NewTrace creates a Trace collector and arms the flight recorder so its window
// is already filling before any stall. The caller must Close it to stop tracing
// and release the single active-recorder slot.
func NewTrace(opts ...TraceOption) (*Trace, error) {
	var cfg trace.FlightRecorderConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	fr := trace.NewFlightRecorder(cfg)
	if err := fr.Start(); err != nil {
		return nil, fmt.Errorf("arm flight recorder: %w", err)
	}
	return &Trace{fr: fr}, nil
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
	if t.fr != nil {
		t.fr.Stop()
		t.fr = nil
	}
}
