package collect_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vucong2409/gander/internal/collect"
)

func TestGoroutinesSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := (collect.Goroutines{}).Snapshot(context.Background(), dir); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "goroutines.txt"))
	if err != nil {
		t.Fatalf("read goroutines.txt: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("goroutines.txt is empty")
	}
	if !bytes.Contains(raw, []byte("goroutine")) {
		t.Errorf("dump does not look like a goroutine profile: %.80q", raw)
	}
}

func TestGoroutinesName(t *testing.T) {
	if got := (collect.Goroutines{}).Name(); got != "goroutines" {
		t.Errorf("Name() = %q, want %q", got, "goroutines")
	}
}
