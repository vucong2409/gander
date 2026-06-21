// Command emit converts a captured execution trace into a Perfetto-loadable
// Chrome JSON trace — the "see the fused view" step.
//
// Point it at a capture bundle (it finds trace.bin and writes fused.json beside
// it) or directly at a trace.bin:
//
//	go run ./cmd/emit bundles/20260101T000000.000-123     # -> bundles/.../fused.json
//	go run ./cmd/emit some/trace.bin -o fused.json
//	go run ./cmd/emit some/trace.bin                       # -> stdout
//
// Open the output (fused.json) at https://ui.perfetto.dev.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vucong2409/gander/internal/collect"
	"github.com/vucong2409/gander/internal/emit"
	"github.com/vucong2409/gander/internal/synth"
)

func main() {
	out := flag.String("o", "", "output path (default: <bundle>/fused.json, or stdout for a trace.bin)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: emit [-o out.json] <bundle-dir | trace.bin>")
		os.Exit(2)
	}

	tracePath, procPath, outPath, err := resolvePaths(flag.Arg(0), *out)
	if err != nil {
		fail(err)
	}

	f, err := os.Open(tracePath)
	if err != nil {
		fail(err)
	}
	defer func() { _ = f.Close() }()

	pt, err := synth.ParseTrace(f)
	if err != nil {
		fail(fmt.Errorf("parse trace: %w", err))
	}

	if procPath != "" {
		if n, err := mergeProc(pt, procPath); err != nil {
			fmt.Fprintln(os.Stderr, "emit: warning: proc.json:", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "emit: merged %d cgroup/PSI samples\n", n)
		}
	}

	if err := writeOut(outPath, pt); err != nil {
		fail(err)
	}

	dest := outPath
	if dest == "" {
		dest = "stdout"
	}
	fmt.Fprintf(os.Stderr, "emit: %d events from %s → %s\n", len(pt.Events), tracePath, dest)
	fmt.Fprintf(os.Stderr, "emit: open %s at https://ui.perfetto.dev (load the .json, not trace.bin)\n", dest)
}

// resolvePaths derives the input trace.bin and output path. A directory is
// treated as a bundle (trace.bin inside, fused.json written beside it); a file
// is used as-is, defaulting to stdout when no -o is given.
func resolvePaths(in, out string) (tracePath, procPath, outPath string, err error) {
	info, err := os.Stat(in)
	if err != nil {
		return "", "", "", err
	}
	if info.IsDir() {
		tracePath = filepath.Join(in, "trace.bin")
		if _, err := os.Stat(tracePath); err != nil {
			return "", "", "", fmt.Errorf("no trace.bin in bundle %s: %w", in, err)
		}
		procPath = filepath.Join(in, "proc.json")
		if _, err := os.Stat(procPath); err != nil {
			procPath = "" // optional collector
		}
		if out == "" {
			out = filepath.Join(in, "fused.json")
		}
		return tracePath, procPath, out, nil
	}
	return in, "", out, nil
}

// mergeProc loads cgroup/PSI samples from proc.json and adds them as clock-
// aligned counter events on the trace timeline. Returns the number of samples.
func mergeProc(pt *synth.ParsedTrace, path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var samples []collect.ProcSample
	if err := json.Unmarshal(b, &samples); err != nil {
		return 0, err
	}
	for _, s := range samples {
		pt.AddAlignedCounter("cgroup.cpu.throttled_usec", s.WallUnixNano, float64(s.ThrottledUsec))
		pt.AddAlignedCounter("cgroup.cpu.nr_throttled", s.WallUnixNano, float64(s.NrThrottled))
		pt.AddAlignedCounter("cgroup.cpu.pressure_some_usec", s.WallUnixNano, float64(s.CPUPressureSomeUsec))
	}
	return len(samples), nil
}

// writeOut writes the emitted trace to outPath, or to stdout when empty.
func writeOut(outPath string, pt *synth.ParsedTrace) error {
	if outPath == "" {
		return emit.WriteChromeTrace(os.Stdout, pt)
	}
	of, err := os.Create(outPath)
	if err != nil {
		return err
	}
	werr := emit.WriteChromeTrace(of, pt)
	cerr := of.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "emit:", err)
	os.Exit(1)
}
