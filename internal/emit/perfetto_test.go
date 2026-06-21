package emit_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vucong2409/gander/internal/emit"
	"github.com/vucong2409/gander/internal/synth"
)

func TestWriteChromeTrace(t *testing.T) {
	pt := &synth.ParsedTrace{
		Events: []synth.Event{
			{TS: 1000, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
			{TS: 1500, Kind: synth.KindGoState, Goroutine: 1, Name: "Waiting", Detail: "chan receive", Stack: []synth.Frame{{Func: "main.worker", File: "w.go", Line: 10}}},
			{TS: 1800, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
			{TS: 1200, Dur: 300, Kind: synth.KindRange, Name: "GC mark"},
			{TS: 1100, Dur: 100, Kind: synth.KindRegion, Goroutine: 1, Name: "work-unit"},
			{TS: 1250, Kind: synth.KindMetric, Name: "/gc/heap/goal:bytes", Value: 4096},
		},
		Unblocks: []synth.Unblock{{TS: 1800, Waker: 2, Woken: 1}},
		GoNames:  map[int64]string{1: "github.com/x/pkg.worker"},
	}

	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, pt); err != nil {
		t.Fatalf("WriteChromeTrace: %v", err)
	}

	// JSON Array Format: the top level must be a bare array of events.
	if b := bytes.TrimSpace(buf.Bytes()); len(b) == 0 || b[0] != '[' {
		t.Fatalf("output must be a JSON array (Perfetto-compatible), got prefix %.20q", b)
	}
	var events []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &events); err != nil {
		t.Fatalf("output is not a valid JSON array: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no trace events emitted")
	}

	names, phs := map[string]int{}, map[string]int{}
	for _, e := range events {
		if n, ok := e["name"].(string); ok {
			names[n]++
		}
		if p, ok := e["ph"].(string); ok {
			phs[p]++
		}
	}
	for _, want := range []string{"Running", "Waiting: chan receive", "GC mark", "work-unit", "/gc/heap/goal:bytes"} {
		if names[want] == 0 {
			t.Errorf("missing event named %q", want)
		}
	}
	if phs["X"] == 0 {
		t.Error("expected complete (X) slice events")
	}
	if phs["M"] == 0 {
		t.Error("expected metadata (M) events naming the lanes")
	}
	if phs["C"] == 0 {
		t.Error("expected counter (C) events for metrics")
	}
	if phs["s"] == 0 || phs["f"] == 0 {
		t.Errorf("expected flow start/finish (s/f) for wake-ups, got s=%d f=%d", phs["s"], phs["f"])
	}

	var laneNamed bool
	for _, e := range events {
		if e["name"] == "thread_name" {
			if args, ok := e["args"].(map[string]any); ok {
				if n, _ := args["name"].(string); strings.Contains(n, "pkg.worker") {
					laneNamed = true
				}
			}
		}
	}
	if !laneNamed {
		t.Error("expected a goroutine lane labeled with its entry function")
	}

	var reasoned bool
	for _, e := range events {
		if args, ok := e["args"].(map[string]any); ok {
			if args["reason"] == "chan receive" {
				reasoned = true
			}
		}
	}
	if !reasoned {
		t.Error("expected a Waiting slice annotated with its block reason")
	}
}

func TestWriteChromeTraceEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, &synth.ParsedTrace{}); err != nil {
		t.Fatalf("empty: %v", err)
	}
	// Must be an empty JSON array [], never null — Perfetto rejects null.
	if b := bytes.TrimSpace(buf.Bytes()); string(b) != "[]" {
		t.Fatalf("empty trace must encode as [], got %q", b)
	}
	var got []any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("empty output is not a valid JSON array: %v", err)
	}
}
