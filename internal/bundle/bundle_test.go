package bundle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vucong2409/gander/internal/bundle"
)

func TestNewMetaWriteRead(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	m := bundle.NewMeta(bundle.Trigger{
		Reason: "work-unit exceeded budget",
		Source: "heartbeat",
		Detail: map[string]any{"seq": 7},
	}, at)

	if err := bundle.WriteMeta(dir, m); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var got bundle.Meta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("meta.json is not valid JSON: %v", err)
	}

	if got.SchemaVersion != bundle.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, bundle.SchemaVersion)
	}
	if got.Trigger.Reason != "work-unit exceeded budget" || got.Trigger.Source != "heartbeat" {
		t.Errorf("Trigger = %+v, want reason/source preserved", got.Trigger)
	}
	if got.Env.GOMAXPROCS < 1 || got.Env.NumCPU < 1 || got.Env.GoVersion == "" {
		t.Errorf("Env not populated: %+v", got.Env)
	}
	if got.Clock.WallUnixNano != at.UnixNano() {
		t.Errorf("Clock.WallUnixNano = %d, want %d", got.Clock.WallUnixNano, at.UnixNano())
	}
}

func TestCreateDirUnique(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bundles") // does not exist yet
	at := time.Now()

	d1, err := bundle.CreateDir(root, at)
	if err != nil {
		t.Fatalf("CreateDir #1: %v", err)
	}
	d2, err := bundle.CreateDir(root, at) // same timestamp
	if err != nil {
		t.Fatalf("CreateDir #2: %v", err)
	}
	if d1 == d2 {
		t.Fatalf("CreateDir returned the same path twice: %s", d1)
	}
	for _, d := range []string{d1, d2} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist: err=%v", d, err)
		}
	}
}
