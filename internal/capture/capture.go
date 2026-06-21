// Package capture orchestrates turning a trigger into an on-disk bundle.
//
// A Coordinator holds a set of Collectors. When Capture is called (typically
// wired to a heartbeat stall, see hb), it creates a bundle directory, writes
// meta.json, and asks each collector to snapshot itself into that directory. A
// failing collector is logged but never aborts the bundle, and a cooldown keeps
// a sustained stall from writing a flood of bundles.
package capture

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/vucong2409/gander/internal/bundle"
)

// Collector snapshots one source of evidence into a bundle directory. Concrete
// collectors live in internal/collect.
type Collector interface {
	Name() string
	Snapshot(ctx context.Context, dir string) error
}

// Coordinator assembles bundles on demand.
type Coordinator struct {
	root     string
	timeout  time.Duration
	cooldown time.Duration
	logger   *log.Logger

	mu          sync.Mutex
	collectors  []Collector
	lastCapture time.Time
}

// Option configures a Coordinator.
type Option func(*Coordinator)

// WithCooldown sets the minimum interval between bundles. 0 disables debouncing.
func WithCooldown(d time.Duration) Option { return func(c *Coordinator) { c.cooldown = d } }

// WithTimeout bounds how long all collectors get per capture. Default: 5s.
func WithTimeout(d time.Duration) Option { return func(c *Coordinator) { c.timeout = d } }

// WithLogger sets where capture progress/errors are logged. Default: discard.
func WithLogger(l *log.Logger) Option { return func(c *Coordinator) { c.logger = l } }

// NewCoordinator returns a Coordinator that writes bundles under root.
func NewCoordinator(root string, opts ...Option) *Coordinator {
	c := &Coordinator{
		root:     root,
		timeout:  5 * time.Second,
		cooldown: time.Second,
		logger:   log.New(io.Discard, "", 0),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Register adds collectors to run on every capture.
func (c *Coordinator) Register(cols ...Collector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collectors = append(c.collectors, cols...)
}

// Capture assembles a bundle for trigger t and returns its directory. If the
// cooldown has not elapsed since the last capture it is debounced: Capture
// returns ("", nil) and writes nothing.
func (c *Coordinator) Capture(t bundle.Trigger) (string, error) {
	c.mu.Lock()
	now := time.Now()
	if c.cooldown > 0 && !c.lastCapture.IsZero() && now.Sub(c.lastCapture) < c.cooldown {
		c.mu.Unlock()
		return "", nil
	}
	c.lastCapture = now
	cols := append([]Collector(nil), c.collectors...) // snapshot under the lock
	c.mu.Unlock()

	dir, err := bundle.CreateDir(c.root, now)
	if err != nil {
		return "", fmt.Errorf("create bundle dir: %w", err)
	}

	if err := bundle.WriteMeta(dir, bundle.NewMeta(t, now)); err != nil {
		// Non-fatal: a bundle without meta is still useful evidence.
		c.logger.Printf("capture: writing meta.json failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	for _, col := range cols {
		if err := col.Snapshot(ctx, dir); err != nil {
			c.logger.Printf("capture: collector %q failed: %v", col.Name(), err)
		}
	}

	c.logger.Printf("capture: wrote bundle %s (%s)", dir, t.Reason)
	return dir, nil
}
