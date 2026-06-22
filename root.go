package main

import "github.com/spf13/cobra"

// newRootCmd builds the gander command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gander",
		Short: "Latency diagnostic for Go — capture, fuse, and diagnose execution traces",
		Long: `gander captures a Go execution trace plus runtime and kernel/cgroup signals
onto one correlated Perfetto timeline — flight-recorder style, triggered on
latency anomalies — then runs a deterministic rules engine to report what's wrong.

It also converts any Go 1.25 execution trace (a flight-recorder snapshot,
runtime/trace output, "go test -trace", or /debug/pprof/trace) into the fused
Perfetto view — a stand-in for gotraceui, whose parser does not read 1.25 traces.`,
		Version:       version,
		SilenceUsage:  true, // a runtime error shouldn't dump the full usage
		SilenceErrors: false,
	}
	root.SetVersionTemplate("gander {{.Version}}\n")
	root.AddCommand(newDemoCmd(), newEmitCmd(), newDiagCmd())
	return root
}
