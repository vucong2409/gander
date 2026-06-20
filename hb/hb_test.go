package hb_test

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vucong2409/gander/hb"
)

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// TestStallFires verifies that a work-unit held longer than the budget triggers
// OnStall, and that the StallInfo reflects the offending unit (Seq tracks the
// number of Ticks, Elapsed exceeds the budget).
func TestStallFires(t *testing.T) {
	got := make(chan hb.StallInfo, 1)
	m := hb.New(
		hb.WithBudget(20*time.Millisecond),
		hb.WithCooldown(10*time.Millisecond),
		hb.WithSampleInterval(2*time.Millisecond),
		hb.WithOnStall(func(s hb.StallInfo) {
			select {
			case got <- s:
			default:
			}
		}),
	)
	m.Start()
	defer m.Stop()

	// Three quick work-units, then stall on the fourth by not ticking again.
	for i := 0; i < 3; i++ {
		m.Tick()
	}
	m.Tick() // seq == 4; this unit never completes

	select {
	case s := <-got:
		if s.Seq != 4 {
			t.Errorf("StallInfo.Seq = %d, want 4 (Tick count)", s.Seq)
		}
		if s.Elapsed < 20*time.Millisecond {
			t.Errorf("StallInfo.Elapsed = %v, want >= budget (20ms)", s.Elapsed)
		}
		if s.Budget != 20*time.Millisecond {
			t.Errorf("StallInfo.Budget = %v, want 20ms", s.Budget)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not fire within 1s")
	}
}

// TestNoFireUnderBudget verifies that a loop ticking faster than the budget
// never trips the watchdog.
func TestNoFireUnderBudget(t *testing.T) {
	var fires atomic.Int64
	m := hb.New(
		hb.WithBudget(60*time.Millisecond),
		hb.WithSampleInterval(5*time.Millisecond),
		hb.WithOnStall(func(hb.StallInfo) { fires.Add(1) }),
	)
	m.Start()

	// Tick every ~2ms for ~120ms: every work-unit is far under the 60ms budget.
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.Tick()
		time.Sleep(2 * time.Millisecond)
	}
	m.Stop() // stop promptly so a post-loop gap can't be seen as a stall

	if n := fires.Load(); n != 0 {
		t.Errorf("OnStall fired %d times under budget, want 0", n)
	}
}

// TestCooldownSuppressesRepeats verifies that a second stall on a *different*
// work-unit within the cooldown window is suppressed.
func TestCooldownSuppressesRepeats(t *testing.T) {
	var fires atomic.Int64
	m := hb.New(
		hb.WithBudget(20*time.Millisecond),
		hb.WithCooldown(500*time.Millisecond),
		hb.WithSampleInterval(2*time.Millisecond),
		hb.WithOnStall(func(hb.StallInfo) { fires.Add(1) }),
	)
	m.Start()
	defer m.Stop()

	m.Tick()                          // unit A (seq 1)
	time.Sleep(50 * time.Millisecond) // A stalls -> fires once
	m.Tick()                          // unit B (seq 2)
	time.Sleep(50 * time.Millisecond) // B stalls, but within cooldown -> suppressed

	if n := fires.Load(); n != 1 {
		t.Errorf("OnStall fired %d times, want exactly 1 (cooldown should suppress the second)", n)
	}
}

// TestStopIsIdempotentAndDoesNotLeak verifies clean lifecycle: Stop without
// Start is a no-op, double Stop is safe, and goroutines are reclaimed.
func TestStopIsIdempotentAndDoesNotLeak(t *testing.T) {
	// Stop without Start must not panic or hang.
	hb.New().Stop()

	baseline := runtime.NumGoroutine()
	m := hb.New(
		hb.WithCoarseClock(true), // exercises both the clock and watchdog goroutines
		hb.WithBudget(10*time.Millisecond),
	)
	m.Start()
	m.Start() // idempotent

	for i := 0; i < 100; i++ {
		m.Tick()
	}
	m.Stop()
	m.Stop() // idempotent

	if !waitFor(time.Second, func() bool { return runtime.NumGoroutine() <= baseline }) {
		t.Errorf("goroutines not reclaimed: have %d, baseline %d", runtime.NumGoroutine(), baseline)
	}
}

// TestCoarseClockTriggers verifies the watchdog also fires when the hot path
// uses the coarse clock source.
func TestCoarseClockTriggers(t *testing.T) {
	got := make(chan hb.StallInfo, 1)
	m := hb.New(
		hb.WithCoarseClock(true),
		hb.WithBudget(20*time.Millisecond),
		hb.WithSampleInterval(2*time.Millisecond),
		hb.WithOnStall(func(s hb.StallInfo) {
			select {
			case got <- s:
			default:
			}
		}),
	)
	m.Start()
	defer m.Stop()

	m.Tick()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not fire in coarse-clock mode within 1s")
	}
}

func BenchmarkTickCoarse(b *testing.B) {
	m := hb.New(hb.WithCoarseClock(true))
	m.Start()
	defer m.Stop()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Tick()
	}
}

func BenchmarkTickPrecise(b *testing.B) {
	m := hb.New()
	m.Start()
	defer m.Stop()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Tick()
	}
}
