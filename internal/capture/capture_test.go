package capture_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/capture"
)

// fakeCollector implements capture.Collector for tests.
type fakeCollector struct {
	name string
	file string // file to write on success
	err  error  // if non-nil, Snapshot fails
}

func (f fakeCollector) Name() string { return f.name }

func (f fakeCollector) Snapshot(_ context.Context, dir string) error {
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(filepath.Join(dir, f.file), []byte("ok"), 0o644)
}

func TestCaptureWritesBundle(t *testing.T) {
	root := t.TempDir()
	c := capture.NewCoordinator(root, capture.WithCooldown(0))
	c.Register(fakeCollector{name: "fake", file: "fake.txt"})

	dir, err := c.Capture(bundle.Trigger{Reason: "r", Source: "test"})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if dir == "" {
		t.Fatal("Capture returned empty dir")
	}
	for _, f := range []string{"meta.json", "fake.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s in bundle: %v", f, err)
		}
	}
}

func TestCooldownDebounces(t *testing.T) {
	root := t.TempDir()
	c := capture.NewCoordinator(root, capture.WithCooldown(time.Hour))

	d1, err := c.Capture(bundle.Trigger{Reason: "first"})
	if err != nil || d1 == "" {
		t.Fatalf("first Capture: dir=%q err=%v", d1, err)
	}
	d2, err := c.Capture(bundle.Trigger{Reason: "second"})
	if err != nil {
		t.Fatalf("second Capture: %v", err)
	}
	if d2 != "" {
		t.Errorf("second Capture within cooldown should debounce, got dir %q", d2)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly 1 bundle on disk, got %d", len(entries))
	}
}

func TestFailingCollectorSwallowed(t *testing.T) {
	root := t.TempDir()
	var logbuf bytes.Buffer
	c := capture.NewCoordinator(root,
		capture.WithCooldown(0),
		capture.WithLogger(log.New(&logbuf, "", 0)),
	)
	c.Register(
		fakeCollector{name: "boom", err: errors.New("kaboom")},
		fakeCollector{name: "ok", file: "ok.txt"},
	)

	dir, err := c.Capture(bundle.Trigger{Reason: "r"})
	if err != nil {
		t.Fatalf("Capture must not fail when a collector errors: %v", err)
	}
	// The bundle and the other collector's output must still be present.
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Errorf("meta.json missing despite collector failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.txt")); err != nil {
		t.Errorf("healthy collector did not run after a failing one: %v", err)
	}
	if !strings.Contains(logbuf.String(), "boom") {
		t.Errorf("expected the failure to be logged, got %q", logbuf.String())
	}
}
