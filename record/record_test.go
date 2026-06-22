package record_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vucong2409/gander/record"
)

func discard() *log.Logger { return log.New(io.Discard, "", 0) }

func countBundles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

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
	r.Stop() // idempotent
}

// TestSnapshotManual checks the on-demand capture path.
func TestSnapshotManual(t *testing.T) {
	dir := t.TempDir()
	r, err := record.Start(record.Options{BundleDir: dir, Cooldown: 0, Logger: discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	bdir, err := r.Snapshot("unit test")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bdir == "" {
		t.Fatal("Snapshot returned no dir")
	}
	if _, err := os.Stat(filepath.Join(bdir, "meta.json")); err != nil {
		t.Errorf("bundle missing meta.json: %v", err)
	}
}

// TestHandlerSnapshot checks the HTTP pull endpoint.
func TestHandlerSnapshot(t *testing.T) {
	dir := t.TempDir()
	r, err := record.Start(record.Options{BundleDir: dir, Cooldown: 0, Logger: discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/gander/?reason=probe", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct{ Bundle, Reason string }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, rec.Body.String())
	}
	if resp.Reason != "probe" {
		t.Errorf("reason = %q, want %q", resp.Reason, "probe")
	}
	if _, err := os.Stat(filepath.Join(resp.Bundle, "meta.json")); err != nil {
		t.Errorf("handler bundle missing meta.json: %v", err)
	}
}

// TestContinuousRotation checks the periodic trigger and that Keep prunes.
func TestContinuousRotation(t *testing.T) {
	dir := t.TempDir()
	r, err := record.Start(record.Options{
		Triggers:  record.Continuous,
		Interval:  20 * time.Millisecond,
		Keep:      2,
		Cooldown:  0,
		BundleDir: dir,
		Logger:    discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(220 * time.Millisecond) // ~10 ticks, but Keep=2 caps the directory
	r.Stop()

	switch n := countBundles(t, dir); {
	case n == 0:
		t.Fatal("Continuous produced no bundles")
	case n > 2:
		t.Errorf("Keep=2 not honored: %d bundles on disk", n)
	}
}

// TestBeginWithoutWatchdog ensures Begin is safe when OnBudget is not selected
// (the watchdog is nil) — the zero-instrumentation always-on use.
func TestBeginWithoutWatchdog(t *testing.T) {
	r, err := record.Start(record.Options{
		Triggers:  record.Continuous,
		Interval:  time.Hour, // effectively off for the test
		BundleDir: t.TempDir(),
		Logger:    discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	r.Begin(context.Background())() // must not panic
}

// TestSignalTrigger checks OnSignal: a signal to ourselves writes a bundle.
func TestSignalTrigger(t *testing.T) {
	dir := t.TempDir()
	r, err := record.Start(record.Options{
		Triggers:  record.OnSignal,
		Signal:    syscall.SIGUSR2,
		Cooldown:  0,
		BundleDir: dir,
		Logger:    discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countBundles(t, dir) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no bundle after SIGUSR2")
}
