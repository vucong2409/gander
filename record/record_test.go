package record_test

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vucong2409/gander/record"
)

// TestRecorderCapturesOnStall drives the full embed path: Start, run a few
// over-budget work-units, Stop, and assert a bundle landed. This is the contract
// a real service relies on.
func TestRecorderCapturesOnStall(t *testing.T) {
	dir := t.TempDir()
	r, err := record.Start(record.Options{
		Budget:    5 * time.Millisecond,
		BundleDir: dir,
		Cooldown:  time.Millisecond,
		Logger:    log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		end := r.Begin(ctx)
		time.Sleep(25 * time.Millisecond) // exceed the 5ms budget so the watchdog fires
		end()
	}
	r.Stop()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	bundles := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bundles++
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "meta.json")); err != nil {
			t.Errorf("bundle %s missing meta.json: %v", e.Name(), err)
		}
		// trace.bin depends on the flight recorder arming; informational only.
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "trace.bin")); err != nil {
			t.Logf("bundle %s has no trace.bin (flight recorder not armed): %v", e.Name(), err)
		}
	}
	if bundles == 0 {
		t.Fatal("expected at least one capture bundle, got none")
	}
}

// TestStopReleasesWithoutBegin ensures Start followed immediately by Stop (no
// work-units) is safe — a service that exits early must not panic.
func TestStopReleasesWithoutBegin(t *testing.T) {
	r, err := record.Start(record.Options{BundleDir: t.TempDir(), Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Stop()
}
