package collect_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/vucong2409/gander/internal/collect"
)

func TestTraceName(t *testing.T) {
	tr, err := collect.NewTrace()
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	defer tr.Close()

	if got := tr.Name(); got != "trace" {
		t.Errorf("Name() = %q, want %q", got, "trace")
	}
}

func TestTraceSnapshot(t *testing.T) {
	tr, err := collect.NewTrace()
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	defer tr.Close()

	// Generate a little trace activity so the window is non-trivial.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); runtime.Gosched() }()
	}
	wg.Wait()
	runtime.GC()

	dir := t.TempDir()
	if err := tr.Snapshot(context.Background(), dir); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "trace.bin"))
	if err != nil {
		t.Fatalf("read trace.bin: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("trace.bin is empty")
	}
}

func TestTraceSnapshotAfterClose(t *testing.T) {
	tr, err := collect.NewTrace()
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	tr.Close()

	if err := tr.Snapshot(context.Background(), t.TempDir()); err == nil {
		t.Error("Snapshot after Close: want error, got nil")
	}
}
