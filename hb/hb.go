// Package hb implements a low-overhead heartbeat for latency-sensitive work loops.
//
// An application calls [Monitor.Tick] at the top of each work-unit (a loop
// iteration or a request). A background watchdog notices when the current
// work-unit has been in flight longer than a budget and invokes a callback.
//
// This is the trigger primitive the rest of gander hangs off. It measures wall
// time, not CPU time, so it catches both CPU-bound stalls (e.g. GC mark-assist
// keeping a goroutine on-CPU) and off-CPU stalls (blocked on I/O, a lock, or the
// run queue) — the two failure modes a CPU profiler alone cannot both see.
//
// The hot path ([Monitor.Tick]) only touches atomics: one store plus one add,
// and — in coarse-clock mode — a single atomic load instead of reading the
// system clock.
package hb

import (
	"sync"
	"sync/atomic"
	"time"
)

// base anchors a process-wide monotonic clock. time.Since(base) reads the
// monotonic component of the system clock, so the result never goes backwards
// (unlike subtracting wall-clock times, which can jump with NTP).
var base = time.Now()

func monoNanos() int64 { return int64(time.Since(base)) }

// StallInfo describes a work-unit that exceeded its budget. It is passed to the
// OnStall callback from the watchdog goroutine.
type StallInfo struct {
	Seq     uint64        // sequence number of the stuck work-unit
	Elapsed time.Duration // how long it had been in flight when detected
	Budget  time.Duration // the configured budget it exceeded
	At      time.Time     // wall-clock time of detection
}

// Option configures a Monitor. Apply options at construction via [New].
type Option func(*Monitor)

// WithBudget sets the in-flight duration after which a work-unit is considered
// stalled. Default: 100ms.
func WithBudget(d time.Duration) Option { return func(m *Monitor) { m.budget = d } }

// WithOnStall sets the callback invoked, from the watchdog goroutine, when a
// work-unit exceeds the budget. It should be cheap and must not block.
func WithOnStall(fn func(StallInfo)) Option { return func(m *Monitor) { m.onStall = fn } }

// WithCooldown sets the minimum interval between consecutive stall callbacks so
// a single long stall does not fire repeatedly. Default: 1s.
func WithCooldown(d time.Duration) Option { return func(m *Monitor) { m.cooldown = d } }

// WithSampleInterval sets how often the watchdog polls in-flight progress.
// Default: budget/4, clamped to at least 1ms.
func WithSampleInterval(d time.Duration) Option { return func(m *Monitor) { m.sample = d } }

// WithCoarseClock trades timestamp precision for speed on the hot path. When
// enabled, a background goroutine refreshes a coarse clock every ~1ms and Tick
// reads that (a single atomic load) instead of calling the system clock.
// Recommended for very tight loops where even a vDSO clock read matters.
func WithCoarseClock(enabled bool) Option { return func(m *Monitor) { m.coarse = enabled } }

// Monitor tracks per-work-unit progress and fires a callback on stalls. The
// zero value is not usable; construct one with [New].
type Monitor struct {
	// Hot-path state — accessed concurrently by Tick and the watchdog, so only
	// ever via sync/atomic.
	startNanos atomic.Int64  // monotonic ns at which the current work-unit started
	seq        atomic.Uint64 // bumped on every Tick
	coarseNow  atomic.Int64  // coarse monotonic ns, refreshed by the clock goroutine

	// Config — set once before Start.
	budget   time.Duration
	cooldown time.Duration
	sample   time.Duration
	coarse   bool
	onStall  func(StallInfo)

	// coarseTick is the coarse-clock refresh period (overridable in tests).
	coarseTick time.Duration

	// Lifecycle.
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

// New constructs a Monitor. Call [Monitor.Start] to begin watching and
// [Monitor.Tick] at the top of each work-unit.
func New(opts ...Option) *Monitor {
	m := &Monitor{
		budget:     100 * time.Millisecond,
		cooldown:   time.Second,
		coarseTick: time.Millisecond,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.sample <= 0 {
		m.sample = m.budget / 4
	}
	if m.sample < time.Millisecond {
		m.sample = time.Millisecond
	}
	// Seed the coarse clock so a Tick issued before the clock goroutine starts
	// still observes a sane timestamp.
	m.coarseNow.Store(monoNanos())
	return m
}

// now returns the current monotonic nanos using the configured clock source.
func (m *Monitor) now() int64 {
	if m.coarse {
		return m.coarseNow.Load()
	}
	return monoNanos()
}

// Tick marks the start of a new work-unit. It is safe for concurrent use but is
// designed to be called from a single hot loop. Cost: one atomic store and one
// atomic add (plus one atomic load in coarse-clock mode).
func (m *Monitor) Tick() {
	m.startNanos.Store(m.now())
	m.seq.Add(1)
}

// Start launches the watchdog goroutine (and, in coarse-clock mode, the clock
// goroutine). It is idempotent.
func (m *Monitor) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}
	m.running = true
	m.stop = make(chan struct{})
	m.startNanos.Store(m.now())

	stop := m.stop
	if m.coarse {
		m.wg.Add(1)
		go m.runClock(stop)
	}
	m.wg.Add(1)
	go m.runWatchdog(stop)
}

// Stop signals the background goroutine(s) to exit and waits for them. It is
// idempotent and safe to call even if Start was never called.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stop)
	m.mu.Unlock()
	m.wg.Wait()
}

// runClock refreshes the coarse clock until stopped.
func (m *Monitor) runClock(stop <-chan struct{}) {
	defer m.wg.Done()
	t := time.NewTicker(m.coarseTick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.coarseNow.Store(monoNanos())
		}
	}
}

// runWatchdog polls in-flight progress and fires OnStall on budget breaches.
func (m *Monitor) runWatchdog(stop <-chan struct{}) {
	defer m.wg.Done()
	t := time.NewTicker(m.sample)
	defer t.Stop()

	var lastFiredSeq uint64
	var lastFireNanos int64
	var hasFired bool

	for {
		select {
		case <-stop:
			return
		case <-t.C:
			// Read seq, then start, then re-read seq. If a Tick raced between
			// the two seq reads, the (seq,start) pair may be torn, so skip this
			// round and re-sample next tick.
			seq := m.seq.Load()
			start := m.startNanos.Load()
			if m.seq.Load() != seq {
				continue
			}
			if seq == 0 {
				continue // no work-units observed yet
			}

			now := monoNanos()
			elapsed := now - start
			if elapsed <= int64(m.budget) {
				continue // current work-unit is within budget
			}
			if seq == lastFiredSeq {
				continue // already reported this stuck work-unit
			}
			if hasFired && now-lastFireNanos < int64(m.cooldown) {
				continue // within the cooldown window
			}

			lastFiredSeq = seq
			lastFireNanos = now
			hasFired = true
			if m.onStall != nil {
				m.onStall(StallInfo{
					Seq:     seq,
					Elapsed: time.Duration(elapsed),
					Budget:  m.budget,
					At:      time.Now(),
				})
			}
		}
	}
}
