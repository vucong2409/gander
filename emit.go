package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vucong2409/gander/internal/collect"
	"github.com/vucong2409/gander/internal/emit"
	"github.com/vucong2409/gander/internal/synth"
)

func newEmitCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "emit <bundle-dir | trace.bin>",
		Short: "Render a trace as a Perfetto timeline (fused.json)",
		Long: `emit converts a captured bundle or a bare Go execution trace into a
Perfetto-loadable Chrome JSON timeline.

Point it at a capture bundle (it finds trace.bin and writes fused.json beside it)
or directly at any Go 1.25 trace .bin — a flight-recorder snapshot, runtime/trace
output, "go test -trace", or /debug/pprof/trace. Open the result at
https://ui.perfetto.dev.

By default the output is written next to the input: <bundle>/fused.json, or
<trace>.fused.json for a bare trace file. Pass "-o -" to write to stdout.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runEmit(args[0], out)
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "",
		`output path (default: <bundle>/fused.json or <trace>.fused.json; "-" = stdout)`)
	return cmd
}

func runEmit(in, out string) error {
	tracePath, procPath, outPath, err := resolvePaths(in, out)
	if err != nil {
		return err
	}

	f, err := os.Open(tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	pt, err := synth.ParseTrace(f)
	if err != nil {
		return fmt.Errorf("parse trace: %w", err)
	}

	if procPath != "" {
		if n, err := mergeProc(pt, procPath); err != nil {
			fmt.Fprintln(os.Stderr, "gander emit: warning: proc.json:", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "gander emit: merged %d cgroup/PSI samples\n", n)
		}
	}

	if err := writeOut(outPath, pt); err != nil {
		return err
	}

	if outPath == "-" {
		fmt.Fprintf(os.Stderr, "gander emit: %d events from %s → stdout\n", len(pt.Events), tracePath)
		return nil
	}
	fmt.Fprintf(os.Stderr, "gander emit: %d events from %s → %s\n", len(pt.Events), tracePath, outPath)
	fmt.Fprintf(os.Stderr, "gander emit: open %s at https://ui.perfetto.dev\n", outPath)
	return nil
}

// resolvePaths derives the input trace.bin and output path. A directory is
// treated as a bundle (trace.bin inside, fused.json written beside it); a bare
// trace file is used as-is, with output defaulting to <trace>.fused.json next to
// it. An explicit out of "-" means stdout and is passed through unchanged.
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
	if out == "" {
		out = fusedPathFor(in)
	}
	return in, "", out, nil
}

// fusedPathFor derives "<trace>.fused.json" from a trace file path, e.g.
// "x/trace.bin" -> "x/trace.fused.json", "x/t" -> "x/t.fused.json".
func fusedPathFor(in string) string {
	return strings.TrimSuffix(in, filepath.Ext(in)) + ".fused.json"
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

// writeOut writes the emitted trace to outPath, or to stdout when outPath is "-".
func writeOut(outPath string, pt *synth.ParsedTrace) error {
	if outPath == "-" {
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
