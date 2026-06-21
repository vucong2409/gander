// Command gander is a single-process, low-overhead latency diagnostic for Go.
//
// It captures the execution trace plus runtime and kernel/cgroup signals onto
// one correlated timeline, triggered on latency anomalies (flight-recorder
// style), then runs a deterministic rules engine to report what's wrong.
//
// Subcommands:
//
//	gander demo [flags]              run the synthetic-stall demo, writing capture bundles
//	gander emit [-o out] <bundle>    render a bundle's trace as a Perfetto timeline (fused.json)
//	gander diag <bundle>             score a bundle into findings (what's wrong)
//	gander version                   print version
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the build version, overridable via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "demo":
		err = runDemo(args)
	case "emit":
		err = runEmit(args)
	case "diag":
		err = runDiag(args)
	case "version", "-version", "--version":
		fmt.Println(version)
	case "help", "-h", "-help", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "gander: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `gander %s — latency diagnostic for Go

usage: gander <command> [flags]

commands:
  demo    run the synthetic-stall demo, writing a capture bundle on each stall
  emit    render a bundle's execution trace as a Perfetto timeline (fused.json)
  diag    score a bundle into deterministic findings (what's wrong)
  version print version

run "gander <command> -h" for a command's flags.
`, version)
}

// fail prints err and exits non-zero. Shared by every subcommand.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "gander:", err)
	os.Exit(1)
}
