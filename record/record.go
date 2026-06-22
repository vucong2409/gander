// Package record is gander's embeddable entry point: a few lines wire a Go
// service to gander's capture-on-stall machinery. Start it once and choose how
// captures fire — automatically on a latency budget, on a signal, continuously,
// or on demand from your own code or an HTTP endpoint. Each capture writes a
// correlated bundle (execution trace + cgroup/PSI + goroutine dump).
//
//	r, _ := record.Start(record.Options{Budget: 10 * time.Millisecond})
//	defer r.Stop()
//	for msg := range queue {
//		end := r.Begin(ctx) // only needed for the OnBudget watchdog
//		process(msg)
//		end()
//	}
//
// Recording is always on once Start returns; the triggers just decide when a
// bundle is written. Inspect a bundle with `gander emit <dir>` (a Perfetto
// timeline) and `gander diag <dir>` (deterministic findings).
package record

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/vucong2409/gander/hb"
	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/capture"
	"github.com/vucong2409/gander/internal/collect"
)

// Triggers selects which captures a Recorder fires autonomously. OR them
// together. Snapshot and Handler are always available regardless of this.
type Triggers uint8

const (
	// OnBudget captures when a Begin'd work-unit stays in flight longer than
	// Options.Budget (the heartbeat watchdog). Requires calling Begin.
	OnBudget Triggers = 1 << iota
	// OnSignal captures when the process receives Options.Signal (default SIGUSR1).
	OnSignal
	// Continuous captures every Options.Interval, keeping the last Options.Keep
	// bundles (a rolling flight recorder on disk).
	Continuous
)

func (t Triggers) has(x Triggers) bool { return t&x != 0 }

// Options configures a Recorder. The zero value is usable and defaults to
// Triggers=OnBudget, Budget=10ms, BundleDir="bundles", Cooldown=1s,
// Interval=30s, Signal=SIGUSR1, Logger=stderr.
type Options struct {
	// Triggers selects the autonomous capture triggers. 0 means OnBudget.
	Triggers Triggers

	// Window is how much trace history the flight recorder retains — the
	// "look-back" a capture dumps. 0 keeps the runtime default (a few seconds).
	Window time.Duration

	// Budget is the per-work-unit latency budget for OnBudget.
	Budget time.Duration
	// Signal is the signal OnSignal listens for (default SIGUSR1).
	Signal os.Signal
	// Interval is how often Continuous captures (default 30s).
	Interval time.Duration
	// Keep caps how many bundles are retained on disk; older ones are pruned
	// after each capture. 0 means keep everything.
	Keep int

	// BundleDir is the directory capture bundles are written into.
	BundleDir string
	// Cooldown is the minimum interval between bundles, so bursty triggers don't
	// write a flood. It is a floor on every trigger, including Continuous.
	Cooldown time.Duration
	// Logger receives capture and error messages. Nil logs to stderr; set it to
	// route through your service's logger, or log.New(io.Discard, "", 0) to silence.
	Logger *log.Logger
	// CPUSamples runs a concurrent CPU profile so bundles' traces include on-CPU
	// stack samples (per-goroutine markers in the fused view). It claims the
	// process-wide CPU-profile slot and adds profiler overhead, so it defaults off.
	CPUSamples bool
}

// Recorder is a running gander capture session. Construct one with Start.
type Recorder struct {
	coord     *capture.Coordinator
	mon       *hb.Monitor    // nil unless OnBudget is set
	tracer    *collect.Trace // nil if the flight recorder could not be armed
	proc      *collect.Proc
	logger    *log.Logger
	bundleDir string
	keep      int

	pruneMu  sync.Mutex
	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// Start arms the flight recorder, the cgroup/PSI sampler, and the goroutine
// collector, then enables the selected Options.Triggers. Recording begins
// immediately; Stop releases everything at shutdown.
//
// If the flight recorder cannot be armed (for example another execution trace
// is already running) Start still returns a working Recorder — bundles simply
// omit trace.bin — and logs a warning.
func Start(opts Options) (*Recorder, error) {
	if opts.Triggers == 0 {
		opts.Triggers = OnBudget
	}
	if opts.Budget <= 0 {
		opts.Budget = 10 * time.Millisecond
	}
	if opts.BundleDir == "" {
		opts.BundleDir = "bundles"
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = time.Second
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	sig := opts.Signal
	if sig == nil {
		sig = syscall.SIGUSR1
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

	r := &Recorder{
		coord:     coord,
		proc:      collect.NewProc(),
		logger:    logger,
		bundleDir: opts.BundleDir,
		keep:      opts.Keep,
		stop:      make(chan struct{}),
	}
	coord.Register(r.proc)

	var topts []collect.TraceOption
	if opts.Window > 0 {
		topts = append(topts, collect.WithMinAge(opts.Window))
	}
	if opts.CPUSamples {
		topts = append(topts, collect.WithCPUSamples())
	}
	if tracer, err := collect.NewTrace(topts...); err != nil {
		logger.Printf("flight recorder unavailable, bundles will omit trace.bin: %v", err)
	} else {
		r.tracer = tracer
		coord.Register(tracer)
	}

	if opts.Triggers.has(OnBudget) {
		r.mon = hb.New(
			hb.WithBudget(opts.Budget),
			hb.WithCooldown(opts.Budget), // re-arm quickly; bundle rate is bounded by Cooldown
			hb.WithOnStall(func(s hb.StallInfo) {
				_, _ = r.capture(bundle.Trigger{
					Reason: "work-unit exceeded budget",
					Source: "heartbeat",
					Detail: map[string]any{
						"seq":        s.Seq,
						"elapsed_ms": s.Elapsed.Milliseconds(),
						"budget_ms":  s.Budget.Milliseconds(),
					},
				})
			}),
		)
		r.mon.Start()
	}
	if opts.Triggers.has(OnSignal) {
		r.runSignal(sig)
	}
	if opts.Triggers.has(Continuous) {
		r.runContinuous(opts.Interval)
	}
	return r, nil
}

// Begin marks the start of a work-unit: it ticks the OnBudget watchdog (if
// enabled) and opens a "work-unit" trace region on the calling goroutine, so
// goid-aware diagnosis can name the goroutine that stalled. Call the returned
// function when the unit ends (commonly `defer r.Begin(ctx)()`). Begin is only
// required for the OnBudget trigger; other triggers work without it.
func (r *Recorder) Begin(ctx context.Context) (end func()) {
	if r.mon != nil {
		r.mon.Tick()
	}
	reg := trace.StartRegion(ctx, "work-unit")
	return reg.End
}

// Snapshot captures a bundle right now from the current flight-recorder window
// and returns its directory. Call it from your own logic (a 5xx, a slow query,
// a breaker trip). It returns ("", nil) if debounced by Cooldown.
func (r *Recorder) Snapshot(reason string) (dir string, err error) {
	if reason == "" {
		reason = "manual snapshot"
	}
	return r.capture(bundle.Trigger{Reason: reason, Source: "manual"})
}

// Handler returns an http.Handler that captures a bundle on each request and
// responds with its path as JSON — a pull-on-demand endpoint à la net/http/pprof.
// Mount it on an admin mux, e.g. mux.Handle("/debug/gander/", r.Handler()).
func (r *Recorder) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reason := req.URL.Query().Get("reason")
		if reason == "" {
			reason = "http snapshot"
		}
		dir, err := r.capture(bundle.Trigger{Reason: reason, Source: "http"})
		switch {
		case err != nil:
			http.Error(w, "capture failed: "+err.Error(), http.StatusInternalServerError)
		case dir == "":
			http.Error(w, "debounced: within capture cooldown", http.StatusTooManyRequests)
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"bundle": dir, "reason": reason})
		}
	})
}

// capture funnels every trigger through the coordinator and prunes old bundles
// when Keep is set. A debounced capture returns ("", nil).
func (r *Recorder) capture(t bundle.Trigger) (string, error) {
	dir, err := r.coord.Capture(t)
	if err != nil {
		r.logger.Printf("capture error: %v", err)
		return "", err
	}
	if dir != "" && r.keep > 0 {
		r.prune()
	}
	return dir, nil
}

// prune removes the oldest bundle directories beyond Keep. Bundle dirs are
// timestamp-named, so lexical order is chronological.
func (r *Recorder) prune() {
	r.pruneMu.Lock()
	defer r.pruneMu.Unlock()
	entries, err := os.ReadDir(r.bundleDir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) <= r.keep {
		return
	}
	sort.Strings(names)
	for _, old := range names[:len(names)-r.keep] {
		if err := os.RemoveAll(filepath.Join(r.bundleDir, old)); err != nil {
			r.logger.Printf("prune: %v", err)
		}
	}
}

func (r *Recorder) runSignal(sig os.Signal) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sig)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer signal.Stop(ch)
		for {
			select {
			case <-r.stop:
				return
			case <-ch:
				_, _ = r.capture(bundle.Trigger{Reason: "signal " + sig.String(), Source: "signal"})
			}
		}
	}()
}

func (r *Recorder) runContinuous(interval time.Duration) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				_, _ = r.capture(bundle.Trigger{Reason: "continuous", Source: "continuous"})
			}
		}
	}()
}

// Stop halts the triggers and releases the flight recorder and samplers. The
// Recorder must not be used after Stop. It is safe to call more than once.
func (r *Recorder) Stop() {
	r.stopOnce.Do(func() {
		close(r.stop)
		r.wg.Wait()
		if r.mon != nil {
			r.mon.Stop()
		}
		if r.tracer != nil {
			r.tracer.Close()
		}
		r.proc.Close()
	})
}
