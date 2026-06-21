package synth_test

import (
	"bytes"
	"context"
	"runtime"
	rtrace "runtime/trace"
	"sync"
	"testing"

	"github.com/vucong2409/gander/internal/synth"
)

// TestParseTrace generates a real execution trace with a named region, some
// goroutine churn, and a GC, then asserts the parser recovers the expected
// shapes from the unified event model.
func TestParseTrace(t *testing.T) {
	var buf bytes.Buffer
	if err := rtrace.Start(&buf); err != nil {
		t.Fatalf("trace.Start: %v", err)
	}
	rtrace.WithRegion(context.Background(), "gander-test-region", func() {
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() { defer wg.Done(); runtime.Gosched() }()
		}
		wg.Wait()
		runtime.GC()
	})
	rtrace.Stop()

	pt, err := synth.ParseTrace(&buf)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if len(pt.Events) == 0 {
		t.Fatal("no events parsed")
	}

	if c := pt.CountByKind(); c[synth.KindGoState] == 0 {
		t.Errorf("expected goroutine state transitions, got counts %v", c)
	}

	var sawRegion bool
	for _, e := range pt.Events {
		if e.Kind == synth.KindRegion && e.Name == "gander-test-region" {
			sawRegion = true
			if e.Dur <= 0 {
				t.Errorf("region interval has non-positive duration %d", e.Dur)
			}
		}
	}
	if !sawRegion {
		t.Error("did not find the gander-test-region region interval")
	}

	if pt.Clock == nil {
		t.Error("expected a clock snapshot from a go1.25 trace")
	}
	if len(pt.GoNames) == 0 {
		t.Error("expected goroutine entry-function names")
	}
}

func TestParseTraceInvalid(t *testing.T) {
	if _, err := synth.ParseTrace(bytes.NewReader([]byte("definitely not a trace"))); err == nil {
		t.Error("ParseTrace on garbage: want error, got nil")
	}
}
