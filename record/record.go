// Package record is gander's embeddable entry point: a few lines wire a Go
// service to gander's capture-on-stall machinery. Start it once, mark each
// work-unit with Begin, and gander snapshots a correlated bundle (execution
// trace + cgroup/PSI + goroutine dump) whenever a unit exceeds its budget.
//
//	r, _ := record.Start(record.Options{Budget: 10 * time.Millisecond, BundleDir: "/var/gander"})
//	defer r.Stop()
//	for msg := range queue {
//		end := r.Begin(ctx)
//		process(msg)
//		end()
//	}
//
// Inspect a bundle with `gander emit <dir>` (a Perfetto timeline) and
// `gander diag <dir>` (deterministic findings).
package record

import (
	"context"
	"log"
	"os"
	"runtime/trace"
	"time"

	"github.com/vucong2409/gander/hb"
	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/capture"
	"github.com/vucong2409/gander/internal/collect"
)

// Options configures a Recorder. The zero value is usable: Budget defaults to
// 10ms, BundleDir to "bundles", Cooldown to 1s, and Logger to stderr.
type Options struct {
	// Budget is the per-work-unit latency budget. When a unit (the span the
	// watchdog observes between a Begin and its end) exceeds Budget, a bundle is
	// captured.
	Budget time.Duration
	// BundleDir is the directory capture bundles are written into.
	BundleDir string
	// Cooldown is the minimum interval between captured bundles, so a sustained
	// stall doesn't write a bundle on every work-unit.
	Cooldown time.Duration
	// Logger receives capture and error messages. Nil logs to stderr; set it to
	// route through your service's logger, or to log.New(io.Discard, ...) to
	// silence.
	Logger *log.Logger
}

// Recorder is a running gander capture session. Construct one with Start.
type Recorder struct {
	mon    *hb.Monitor
	tracer *collect.Trace // nil if the flight recorder could not be armed
	proc   *collect.Proc
}

// Start arms the flight recorder, the cgroup/PSI sampler, and the goroutine
// collector behind a heartbeat watchdog, and begins capturing bundles when a
// work-unit exceeds Options.Budget. Call Begin at the top of each work-unit and
// Stop at shutdown.
//
// If the flight recorder cannot be armed (for example another execution trace is
// already running) Start still returns a working Recorder — bundles simply omit
// trace.bin — and logs a warning.
func Start(opts Options) (*Recorder, error) {
	if opts.Budget <= 0 {
		opts.Budget = 10 * time.Millisecond
	}
	if opts.BundleDir == "" {
		opts.BundleDir = "bundles"
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "gander: ", log.LstdFlags)
	}

	coord := capture.NewCoordinator(opts.BundleDir,
		capture.WithLogger(logger),
		capture.WithCooldown(opts.Cooldown),
	)
	coord.Register(collect.Goroutines{})

	r := &Recorder{proc: collect.NewProc()}
	coord.Register(r.proc)

	if tracer, err := collect.NewTrace(); err != nil {
		logger.Printf("flight recorder unavailable, bundles will omit trace.bin: %v", err)
	} else {
		r.tracer = tracer
		coord.Register(tracer)
	}

	r.mon = hb.New(
		hb.WithBudget(opts.Budget),
		hb.WithCooldown(opts.Budget), // re-arm quickly so repeated stalls are seen; bundle rate is bounded by capture Cooldown
		hb.WithOnStall(func(s hb.StallInfo) {
			if _, err := coord.Capture(bundle.Trigger{
				Reason: "work-unit exceeded budget",
				Source: "heartbeat",
				Detail: map[string]any{
					"seq":        s.Seq,
					"elapsed_ms": s.Elapsed.Milliseconds(),
					"budget_ms":  s.Budget.Milliseconds(),
				},
			}); err != nil {
				logger.Printf("capture error: %v", err)
			}
		}),
	)
	r.mon.Start()
	return r, nil
}

// Begin marks the start of a work-unit: it ticks the watchdog and opens a
// "work-unit" trace region on the calling goroutine, so goid-aware diagnosis can
// name the goroutine that stalled. Call the returned function when the unit ends
// (commonly `defer r.Begin(ctx)()`).
func (r *Recorder) Begin(ctx context.Context) (end func()) {
	r.mon.Tick()
	reg := trace.StartRegion(ctx, "work-unit")
	return reg.End
}

// Stop halts the watchdog and releases the flight recorder and samplers. The
// Recorder must not be used after Stop.
func (r *Recorder) Stop() {
	r.mon.Stop()
	if r.tracer != nil {
		r.tracer.Close()
	}
	r.proc.Close()
}
