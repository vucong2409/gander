// Package collect holds the concrete collectors that snapshot evidence into a
// bundle. Each type satisfies capture.Collector structurally (it implements
// Name and Snapshot), so this package does not import internal/capture.
package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/pprof"
)

// Goroutines dumps the stacks of every goroutine at the moment of capture —
// i.e. what each goroutine is blocked on at the bad instant. It is the cheapest,
// highest-signal artifact in a bundle.
type Goroutines struct{}

// Name implements capture.Collector.
func (Goroutines) Name() string { return "goroutines" }

// Snapshot writes a full goroutine dump to <dir>/goroutines.txt. debug level 2
// requests human-readable stacks for all goroutines.
func (Goroutines) Snapshot(_ context.Context, dir string) error {
	p := pprof.Lookup("goroutine")
	if p == nil {
		return errors.New("goroutine profile unavailable")
	}
	f, err := os.Create(filepath.Join(dir, "goroutines.txt"))
	if err != nil {
		return err
	}
	werr := p.WriteTo(f, 2)
	cerr := f.Close() // surfaces flush errors that WriteTo alone can miss
	if werr != nil {
		return werr
	}
	return cerr
}
