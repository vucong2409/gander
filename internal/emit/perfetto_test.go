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

func TestWriteChromeTraceASCIIOnly(t *testing.T) {
	// A stack deeper than the truncation limit (12) must still render as pure
	// ASCII. Perfetto's JSON tokenizer is byte-oriented and rejects multi-byte
	// UTF-8 (a "…" ellipsis) with json_parser_failure — and the marker only
	// appears for deep stacks, so it slips through lighter traces.
	frames := make([]synth.Frame, 20)
	for i := range frames {
		frames[i] = synth.Frame{Func: "main.deep", File: "d.go", Line: uint64(i + 1)}
	}
	pt := &synth.ParsedTrace{Events: []synth.Event{
		{TS: 0, Kind: synth.KindGoState, Goroutine: 1, Name: "Waiting", Detail: "chan receive", Stack: frames},
		{TS: 100, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
	}}

	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, pt); err != nil {
		t.Fatalf("WriteChromeTrace: %v", err)
	}
	for i, c := range buf.Bytes() {
		if c > 0x7f {
			t.Fatalf("non-ASCII byte 0x%02x at offset %d — Perfetto's JSON tokenizer rejects it", c, i)
		}
	}
	if !strings.Contains(buf.String(), "more)") {
		t.Error("expected an ASCII overflow marker for the deep stack")
	}
}

func TestWriteChromeTraceUXMarkers(t *testing.T) {
	pt := &synth.ParsedTrace{Events: []synth.Event{
		// busy goroutine 1 (lots of Running) vs parked goroutine 2 (only Waiting)
		{TS: 0, Dur: 0, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
		{TS: 900, Kind: synth.KindGoState, Goroutine: 1, Name: "Waiting", Detail: "chan receive"},
		{TS: 1000, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
		{TS: 0, Kind: synth.KindGoState, Goroutine: 2, Name: "Waiting", Detail: "system goroutine wait"},
		// a real GC STW (overlaid) and a tracing-induced one (not overlaid)
		{TS: 200, Dur: 30, Kind: synth.KindRange, Name: "stop-the-world (GC sweep termination)"},
		{TS: 400, Dur: 30, Kind: synth.KindRange, Name: "stop-the-world (start trace)"},
		// two work-units; the longer one is the stall
		{TS: 100, Dur: 50, Kind: synth.KindRegion, Goroutine: 1, Name: "work-unit"},
		{TS: 300, Dur: 600, Kind: synth.KindRegion, Goroutine: 1, Name: "work-unit"},
	}}

	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, pt); err != nil {
		t.Fatalf("WriteChromeTrace: %v", err)
	}
	var events []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &events); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	var stwGlobal, stwTracing, stallMarker bool
	sortIdx := map[float64]float64{} // tid -> sort_index
	for _, e := range events {
		name, _ := e["name"].(string)
		if e["ph"] == "i" && e["s"] == "g" {
			switch {
			case strings.Contains(name, "GC sweep termination"):
				stwGlobal = true
			case strings.Contains(name, "start trace"):
				stwTracing = true
			case strings.HasPrefix(name, "slowest work-unit"):
				stallMarker = true
			}
		}
		if name == "thread_sort_index" {
			if args, ok := e["args"].(map[string]any); ok {
				sortIdx[e["tid"].(float64)] = args["sort_index"].(float64)
			}
		}
	}

	if !stwGlobal {
		t.Error("expected a global STW overlay for the GC stop-the-world")
	}
	if stwTracing {
		t.Error("tracing-induced STW should NOT be overlaid")
	}
	if !stallMarker {
		t.Error("expected a global 'slowest work-unit' stall marker")
	}
	// busy G1 must sort above parked G2 (lower index = higher in the UI).
	if !(sortIdx[1] < sortIdx[2]) {
		t.Errorf("expected busy G1 ordered above parked G2, got idx G1=%v G2=%v", sortIdx[1], sortIdx[2])
	}
}

func TestWriteChromeTraceCPUSamples(t *testing.T) {
	pt := &synth.ParsedTrace{Events: []synth.Event{
		{TS: 0, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
		{TS: 50, Kind: synth.KindSample, Goroutine: 1, Stack: []synth.Frame{
			{Func: "github.com/x/pkg.hotLoop", File: "h.go", Line: 9},
			{Func: "main.main", File: "m.go", Line: 3},
		}},
		{TS: 60, Kind: synth.KindSample, Goroutine: 0}, // unattributed -> skipped
	}}
	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, pt); err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &events); err != nil {
		t.Fatal(err)
	}

	var found, skippedUnattributed = false, true
	for _, e := range events {
		if e["cat"] != "sample" {
			continue
		}
		if e["ph"] != "i" {
			t.Errorf("sample should be an instant event, got ph=%v", e["ph"])
		}
		if e["tid"] == float64(0) {
			skippedUnattributed = false // an unattributed sample leaked through
		}
		if e["name"] == "pkg.hotLoop" && e["tid"] == float64(1) {
			args, _ := e["args"].(map[string]any)
			if s, _ := args["stack"].(string); strings.Contains(s, "main.main") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected a CPU-sample marker on G1 with leaf func 'pkg.hotLoop' and full stack")
	}
	if !skippedUnattributed {
		t.Error("samples with goroutine 0 should be skipped")
	}
}

func TestThrottleOverlay(t *testing.T) {
	mk := func(ts int64, usec float64) synth.Event {
		return synth.Event{TS: ts, Kind: synth.KindMetric, Name: "cgroup.cpu.throttled_usec", Value: usec}
	}
	// throttled_usec rises 0 -> 5000 -> 12000 then flat: one throttled run of 12ms.
	pt := &synth.ParsedTrace{Events: []synth.Event{
		{TS: 0, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
		mk(0, 0), mk(100, 0), mk(200, 5000), mk(300, 12000), mk(400, 12000),
	}}
	var buf bytes.Buffer
	if err := emit.WriteChromeTrace(&buf, pt); err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	_ = json.Unmarshal(buf.Bytes(), &events)

	var lines []string
	for _, e := range events {
		if e["ph"] == "i" && e["s"] == "g" {
			if n, _ := e["name"].(string); strings.HasPrefix(n, "CFS throttled") {
				lines = append(lines, n)
			}
		}
	}
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 throttle overlay line, got %v", lines)
	}
	if !strings.Contains(lines[0], "12ms") {
		t.Errorf("expected the run's throttled duration (12ms) in the label, got %q", lines[0])
	}

	// A flat (never-rising) series must produce no overlay.
	flat := &synth.ParsedTrace{Events: []synth.Event{
		{TS: 0, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
		mk(0, 0), mk(100, 0), mk(200, 0),
	}}
	buf.Reset()
	_ = emit.WriteChromeTrace(&buf, flat)
	if strings.Contains(buf.String(), "CFS throttled") {
		t.Error("a flat throttled_usec series must not emit a throttle overlay")
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
