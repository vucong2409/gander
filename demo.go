package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vucong2409/gander/record"
)

// runDemo exercises the embeddable record API: a tight work loop that marks each
// unit with record.Begin and, on demand, deliberately stalls so you can watch
// the watchdog auto-capture a bundle. Stall injection is deterministic (gated on
// the iteration counter, not random) so the demo behaves the same on every run.
//
//	gander demo --stall-sleep=50ms --budget=10ms
//	gander demo --stall-chan --budget=10ms   # wake-up arrows in the fused view
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		budget     = fs.Duration("budget", 10*time.Millisecond, "work-unit budget; the watchdog fires when an iteration exceeds it")
		work       = fs.Duration("work", time.Millisecond, "simulated on-CPU work per iteration")
		duration   = fs.Duration("duration", 3*time.Second, "how long to run (0 = until interrupted)")
		stallSleep = fs.Duration("stall-sleep", 0, "if >0, inject a sleep of this length to force an off-CPU stall")
		stallEvery = fs.Int("stall-every", 50, "inject the stall on every Nth iteration (<=0 disables)")
		stallAlloc = fs.Int("stall-alloc", 0, "if >0, allocate this many bytes on stall iterations to add GC pressure")
		stallChan  = fs.Bool("stall-chan", false, "on stall iterations, block on a channel a helper goroutine feeds — produces a wake-up arrow")
		chanDelay  = fs.Duration("stall-chan-delay", 40*time.Millisecond, "helper reply delay for --stall-chan")
		bundleDir  = fs.String("bundle-dir", "bundles", "directory to write capture bundles into")
		capCool    = fs.Duration("capture-cooldown", time.Second, "minimum interval between capture bundles")
	)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	logger := log.New(os.Stderr, "demo: ", log.Ltime|log.Lmicroseconds)

	// The whole gander wiring — flight recorder, cgroup/PSI sampler, goroutine
	// dump, and the heartbeat watchdog that auto-captures a bundle on a budget
	// miss — is exactly the few lines a real service would add.
	rec, err := record.Start(record.Options{
		Budget:    *budget,
		BundleDir: *bundleDir,
		Cooldown:  *capCool,
		Logger:    logger,
	})
	if err != nil {
		return err
	}
	defer rec.Stop()

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
		end := rec.Begin(ctx) // marks this work-unit for the watchdog + trace

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
		end()
	}

	logger.Printf("done: %d iterations, last alloc %d bytes (bundles in %s; inspect with `gander emit`/`gander diag`)",
		iter, len(gcSink), *bundleDir)
	return nil
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
