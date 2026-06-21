package emit_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vucong2409/gander/internal/emit"
	"github.com/vucong2409/gander/internal/synth"
)

func TestWriteChromeTrace(t *testing.T) {
	pt := &synth.ParsedTrace{
		Events: []synth.Event{
			{TS: 1000, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
			{TS: 1500, Kind: synth.KindGoState, Goroutine: 1, Name: "Waiting", Detail: "Running"},
			{TS: 1800, Kind: synth.KindGoState, Goroutine: 1, Name: "Running", Detail: "Waiting"},
			{TS: 1200, Dur: 300, Kind: synth.KindRange, Name: "GC mark"},
			{TS: 1100, Dur: 100, Kind: synth.KindRegion, Goroutine: 1, Name: "work-unit"},
			{TS: 1250, Kind: synth.KindMetric, Name: "/gc/heap/goal:bytes", Value: 4096},
		},
		Unblocks: []synth.Unblock{{TS: 1800, Waker: 2, Woken: 1}},
	}

	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, pt); err != nil {
		t.Fatalf("WriteChromeTrace: %v", err)
	}

	var got struct {
		TraceEvents     []map[string]any `json:"traceEvents"`
		DisplayTimeUnit string           `json:"displayTimeUnit"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got.DisplayTimeUnit != "ns" {
		t.Errorf("displayTimeUnit = %q, want ns", got.DisplayTimeUnit)
	}
	if len(got.TraceEvents) == 0 {
		t.Fatal("no trace events emitted")
	}

	names, phs := map[string]int{}, map[string]int{}
	for _, e := range got.TraceEvents {
		if n, ok := e["name"].(string); ok {
			names[n]++
		}
		if p, ok := e["ph"].(string); ok {
			phs[p]++
		}
	}
	for _, want := range []string{"Running", "Waiting", "GC mark", "work-unit", "/gc/heap/goal:bytes"} {
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
}

func TestWriteChromeTraceEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, &synth.ParsedTrace{}); err != nil {
		t.Fatalf("empty: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("empty output is not valid JSON: %v", err)
	}
}
