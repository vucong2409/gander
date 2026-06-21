package collect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProcSample is one timestamped reading of the process's cgroup CPU controller.
// Counters are cumulative (monotonically increasing); the viewer plots them, so
// the steps where they climb mark throttling / CPU-pressure episodes.
type ProcSample struct {
	WallUnixNano        int64  `json:"wall_unix_nano"`         // for clock-aligning onto the trace
	ThrottledUsec       uint64 `json:"throttled_usec"`         // cgroup cpu.stat: total CFS-throttled time
	NrThrottled         uint64 `json:"nr_throttled"`           // cgroup cpu.stat: throttle events
	CPUPressureSomeUsec uint64 `json:"cpu_pressure_some_usec"` // PSI cpu.pressure: cumulative "some" stall
}

// Proc samples cgroup CPU throttling and PSI into a rolling window so the fused
// view can show "slow because the kernel throttled/starved us" alongside the
// goroutine and GC lanes.
//
// It is Linux-only; on other platforms readProcSample collects nothing, so
// Snapshot writes an empty proc.json and the demo still builds and runs
// everywhere.
type Proc struct {
	interval time.Duration
	maxAge   time.Duration

	mu        sync.Mutex
	samples   []ProcSample
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// ProcOption configures the sampler.
type ProcOption func(*Proc)

// WithProcInterval sets the sampling period (default 25ms).
func WithProcInterval(d time.Duration) ProcOption { return func(p *Proc) { p.interval = d } }

// WithProcMaxAge bounds how much history the window retains (default 5s).
func WithProcMaxAge(d time.Duration) ProcOption { return func(p *Proc) { p.maxAge = d } }

// NewProc starts a background sampler. The caller must Close it to stop sampling.
func NewProc(opts ...ProcOption) *Proc {
	p := &Proc{
		interval: 25 * time.Millisecond,
		maxAge:   5 * time.Second,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(p)
	}
	go p.run()
	return p
}

func (p *Proc) run() {
	defer close(p.done)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			s, ok := readProcSample()
			if !ok {
				continue
			}
			p.mu.Lock()
			p.samples = append(p.samples, s)
			cutoff := time.Now().Add(-p.maxAge).UnixNano()
			drop := 0
			for drop < len(p.samples) && p.samples[drop].WallUnixNano < cutoff {
				drop++
			}
			p.samples = p.samples[drop:]
			p.mu.Unlock()
		}
	}
}

// Name implements capture.Collector.
func (*Proc) Name() string { return "proc" }

// Snapshot writes the current window of samples to <dir>/proc.json.
func (p *Proc) Snapshot(_ context.Context, dir string) error {
	p.mu.Lock()
	out := append([]ProcSample{}, p.samples...)
	p.mu.Unlock()

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, "proc.json"), b, 0o644)
}

// Close stops the sampler. Safe to call more than once.
func (p *Proc) Close() {
	p.closeOnce.Do(func() {
		close(p.stop)
		<-p.done
	})
}

// parseCPUStat extracts the throttle counters from a cgroup v2 cpu.stat file.
func parseCPUStat(data string) (throttledUsec, nrThrottled uint64) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "throttled_usec":
			throttledUsec = v
		case "nr_throttled":
			nrThrottled = v
		}
	}
	return throttledUsec, nrThrottled
}

// parsePSISome extracts the cumulative "some" stall time in microseconds from a
// PSI pressure file (e.g. cgroup cpu.pressure).
func parsePSISome(data string) uint64 {
	for _, line := range strings.Split(data, "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if rest, ok := strings.CutPrefix(f, "total="); ok {
				if v, err := strconv.ParseUint(rest, 10, 64); err == nil {
					return v
				}
			}
		}
	}
	return 0
}
