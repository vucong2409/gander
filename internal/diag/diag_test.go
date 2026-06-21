package diag_test

import (
	"testing"

	"github.com/vucong2409/gander/internal/bundle"
	"github.com/vucong2409/gander/internal/collect"
	"github.com/vucong2409/gander/internal/diag"
	"github.com/vucong2409/gander/internal/synth"
)

func TestDiagnose(t *testing.T) {
	meta := bundle.Meta{Trigger: bundle.Trigger{
		Source: "heartbeat",
		Detail: map[string]any{"elapsed_ms": 50.0, "budget_ms": 10.0},
	}}
	proc := []collect.ProcSample{
		{ThrottledUsec: 0, NrThrottled: 0, CPUPressureSomeUsec: 0},
		{ThrottledUsec: 5000, NrThrottled: 3, CPUPressureSomeUsec: 4000},
	}
	pt := &synth.ParsedTrace{Events: []synth.Event{
		{TS: 0, Dur: 20_000_000, Kind: synth.KindRange, Name: "stop-the-world (GC mark termination)"}, // 20% of a 100ms window
		{TS: 0, Kind: synth.KindGoState, Goroutine: 1, Name: "Waiting", Detail: "chan receive"},
		{TS: 30_000_000, Kind: synth.KindGoState, Goroutine: 1, Name: "Running"},
		{TS: 100_000_000, Kind: synth.KindGoState, Goroutine: 1, Name: "Waiting", Detail: "system goroutine wait"}, // idle, filtered
	}}

	got := map[string]diag.Finding{}
	findings := diag.Diagnose(pt, proc, meta)
	for _, f := range findings {
		got[f.Rule] = f
	}

	for _, want := range []string{"missed-budget", "cfs-throttling", "cpu-pressure", "gc-pressure", "block-reasons"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing rule %q", want)
		}
	}
	if got["missed-budget"].Severity != "critical" {
		t.Errorf("missed-budget severity = %q, want critical", got["missed-budget"].Severity)
	}
	if got["cfs-throttling"].Severity != "critical" {
		t.Errorf("cfs-throttling severity = %q, want critical", got["cfs-throttling"].Severity)
	}
	if got["gc-pressure"].Severity != "critical" {
		t.Errorf("gc-pressure severity = %q, want critical", got["gc-pressure"].Severity)
	}
	// "chan receive" should be the reported block reason; the idle one is filtered.
	if r := got["block-reasons"].Evidence; r == "" || r[:4] != "chan" {
		t.Errorf("block-reasons evidence = %q, want it to start with chan receive", r)
	}
	// Findings must be sorted most-severe first.
	if len(findings) == 0 || findings[0].Severity != "critical" {
		t.Errorf("findings not sorted by severity; first = %+v", findings)
	}
}

func TestDiagnoseEmpty(t *testing.T) {
	// No heartbeat trigger, no proc samples, no events: no findings, no panic.
	if got := diag.Diagnose(&synth.ParsedTrace{}, nil, bundle.Meta{}); len(got) != 0 {
		t.Errorf("expected no findings on an empty bundle, got %v", got)
	}
}
