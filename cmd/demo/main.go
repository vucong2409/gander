// Command demo exercises the hb heartbeat library: it runs a tight work loop
// that calls hb.Tick() each iteration and, on demand, deliberately stalls so you
// can watch the watchdog fire — before any of gander's capture machinery exists.
//
// Example (prints STALL lines to stderr):
//
//	go run ./cmd/demo --stall-sleep=50ms --budget=10ms
//	go run ./cmd/demo --stall-chan --budget=10ms   # wake-up arrows in the fused view
//
// Stall injection is deterministic (gated on the iteration counter, not random)
// so the demo behaves the same on every run.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime/trace"
	"syscall"
	"time"

	"github.com/vucong2409/gander/hb"
	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/capture"
	"github.com/vucong2409/gander/internal/collect"
)

func main() {
	var (
		budget     = flag.Duration("budget", 10*time.Millisecond, "work-unit budget; the watchdog fires when an iteration exceeds it")
		work       = flag.Duration("work", time.Millisecond, "simulated on-CPU work per iteration")
		duration   = flag.Duration("duration", 3*time.Second, "how long to run (0 = until interrupted)")
		stallSleep = flag.Duration("stall-sleep", 0, "if >0, inject a sleep of this length to force an off-CPU stall")
		stallEvery = flag.Int("stall-every", 50, "inject the stall on every Nth iteration (<=0 disables)")
		stallAlloc = flag.Int("stall-alloc", 0, "if >0, allocate this many bytes on stall iterations to add GC pressure")
		stallChan  = flag.Bool("stall-chan", false, "on stall iterations, block on a channel a helper goroutine feeds — produces a wake-up arrow")
		chanDelay  = flag.Duration("stall-chan-delay", 40*time.Millisecond, "helper reply delay for --stall-chan")
		coarse     = flag.Bool("coarse", false, "use hb coarse-clock mode on the hot path")
		bundleDir  = flag.String("bundle-dir", "bundles", "directory to write capture bundles into")
		capCool    = flag.Duration("capture-cooldown", time.Second, "minimum interval between capture bundles")
	)
	flag.Parse()

	logger := log.New(os.Stderr, "demo: ", log.Ltime|log.Lmicroseconds)

	// On a stall, assemble a bundle (meta.json + goroutine dump + execution trace).
	coord := capture.NewCoordinator(*bundleDir,
		capture.WithLogger(logger),
		capture.WithCooldown(*capCool),
	)
	coord.Register(collect.Goroutines{})

	// Arm the flight recorder up front so its in-memory window is already filling
	// before any stall fires; on a stall its Snapshot persists the lead-up to
	// trace.bin. A bundle without a trace is still useful, so failure is non-fatal.
	tracer, err := collect.NewTrace()
	if err != nil {
		logger.Printf("flight recorder unavailable, continuing without trace.bin: %v", err)
	} else {
		coord.Register(tracer)
		defer tracer.Close()
	}

	// Sample cgroup CPU throttling + PSI into a rolling window (Linux; a no-op
	// on other platforms) so the fused view can show kernel stalls beside the
	// goroutine lanes.
	procSampler := collect.NewProc()
	coord.Register(procSampler)
	defer procSampler.Close()

	var stalls int
	opts := []hb.Option{
		hb.WithBudget(*budget),
		hb.WithCooldown(*budget), // short cooldown so repeated stalls surface in the demo
		hb.WithOnStall(func(s hb.StallInfo) {
			stalls++
			logger.Printf("STALL seq=%d elapsed=%s budget=%s",
				s.Seq, s.Elapsed.Round(time.Microsecond), s.Budget)
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
	}
	if *coarse {
		opts = append(opts, hb.WithCoarseClock(true))
	}

	m := hb.New(opts...)
	m.Start()
	defer m.Stop()

	ctx, cancel := runContext(*duration)
	defer cancel()

	logger.Printf("running: budget=%s work=%s stall-sleep=%s stall-chan=%t stall-every=%d bundle-dir=%s",
		*budget, *work, *stallSleep, *stallChan, *stallEvery, *bundleDir)

	// Optional channel-based stall: a helper replies after a delay, so each stall
	// is a real goroutine-to-goroutine wake-up (a flow arrow) rather than a timer
	// wake-up (which has no waker and so draws no arrow).
	var wakeReq chan chan struct{}
	if *stallChan {
		wakeReq = make(chan chan struct{})
		go func() {
			for done := range wakeReq {
				time.Sleep(*chanDelay)
				close(done) // wakes the requesting goroutine
			}
		}()
		defer close(wakeReq)
	}

	var iter uint64
	for ctx.Err() == nil {
		iter++
		m.Tick()                                   // mark the start of this work-unit
		reg := trace.StartRegion(ctx, "work-unit") // names this work-unit's goroutine + span in the trace

		busySpin(*work) // simulated work

		// Deterministic stall injection.
		if *stallEvery > 0 && iter%uint64(*stallEvery) == 0 {
			if *stallChan {
				done := make(chan struct{})
				wakeReq <- done
				<-done // block until the helper wakes us — a real wake-up edge
			}
			if *stallSleep > 0 {
				time.Sleep(*stallSleep)
			}
			if *stallAlloc > 0 {
				gcSink = make([]byte, *stallAlloc) // churns garbage to pressure the GC
			}
		}
		reg.End()
	}

	logger.Printf("done: %d iterations, %d stalls detected (last alloc %d bytes)",
		iter, stalls, len(gcSink))
}

// gcSink retains the most recent allocation so the optimizer cannot elide the
// make() in the loop; each overwrite turns the previous block into garbage.
var gcSink []byte

// busySpin keeps the goroutine on-CPU for approximately d, mimicking real work.
// The repeated time.Now() calls have observable side effects, so the loop is not
// elided despite the empty body.
func busySpin(d time.Duration) {
	if d <= 0 {
		return
	}
	end := time.Now().Add(d)
	for time.Now().Before(end) {
	}
}

// runContext returns a context cancelled on SIGINT/SIGTERM, and additionally
// after d if d > 0.
func runContext(d time.Duration) (context.Context, context.CancelFunc) {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if d <= 0 {
		return ctx, stopSignals
	}
	timed, cancelTimeout := context.WithTimeout(ctx, d)
	return timed, func() {
		cancelTimeout()
		stopSignals()
	}
}
