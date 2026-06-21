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
// Open the output at https://ui.perfetto.org.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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

	tracePath, outPath, err := resolvePaths(flag.Arg(0), *out)
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

	if err := writeOut(outPath, pt); err != nil {
		fail(err)
	}

	dest := outPath
	if dest == "" {
		dest = "stdout"
	}
	fmt.Fprintf(os.Stderr, "emit: %d events from %s → %s (open in https://ui.perfetto.org)\n",
		len(pt.Events), tracePath, dest)
}

// resolvePaths derives the input trace.bin and output path. A directory is
// treated as a bundle (trace.bin inside, fused.json written beside it); a file
// is used as-is, defaulting to stdout when no -o is given.
func resolvePaths(in, out string) (tracePath, outPath string, err error) {
	info, err := os.Stat(in)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		tracePath = filepath.Join(in, "trace.bin")
		if _, err := os.Stat(tracePath); err != nil {
			return "", "", fmt.Errorf("no trace.bin in bundle %s: %w", in, err)
		}
		if out == "" {
			out = filepath.Join(in, "fused.json")
		}
		return tracePath, out, nil
	}
	return in, out, nil
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
